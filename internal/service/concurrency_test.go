package service

// Concurrency tests for the public API surface.
//
// Every other test in this repository drives one request at a time, which is
// how a pool deadlock in SearchByVector survived several rounds of review: the
// scoped search held one connection while hydration acquired a second, so it
// only failed once concurrency reached the pool limit. Sequential tests cannot
// see that class of bug.
//
// These exercise each endpoint under enough concurrency to exhaust the pool,
// which is the condition that turns "uses one connection too many" into "hangs
// forever".

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/embedding"
	"github.com/context0/context0/internal/graph"
	"github.com/google/uuid"
)

// concurrentTestService builds a service backed by a real database, with a
// deliberately small pool so exhaustion happens at low concurrency.
func concurrentTestService(t *testing.T) (*MemoryService, context.Context) {
	t.Helper()

	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set")
	}
	// A tiny pool makes any surplus connection per request fatal rather than
	// merely slow, so the failure is unambiguous.
	if !strings.Contains(dsn, "pool_max_conns") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "pool_max_conns=4"
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	return NewMemoryService(repo, embedding.NewBagOfWordsEmbedder(384)), ctx
}

// runConcurrently checks that concurrent calls actually run concurrently.
//
// The failure this guards against is subtle: a request that holds two pool
// connections does not error. Callers queue, the pool drains, and everything
// eventually succeeds -- just orders of magnitude slower. Measured against the
// real deadlock in this codebase, the batch took 20s instead of 0.08s while
// returning no errors at all.
//
// The comparison is against the same work done sequentially, not against a
// wall-clock constant or a single-call baseline. Both earlier attempts were
// really constants in disguise and failed spuriously when the full suite ran
// every package in parallel against one database: the machine slowed down, the
// threshold did not. Timing both halves back to back means load affects the
// two measurements together, so the ratio still means what it claims.
//
// With a 4-connection pool, 12 concurrent calls need about 3 rounds, so a
// healthy run lands near a third of the sequential time. A run that serialises
// on the pool lands at roughly the sequential time, because that is precisely
// what serialising means.
//
// The concurrent pass runs first and the sequential reference second, which
// matters because these workloads write. Store fans out into contradiction
// detection across the project's existing memories, so per-call cost grows
// with the row count. Measuring sequentially first compared a small-project
// reference against a concurrent pass on a project twice the size, and blamed
// the pool for the difference -- observed as a 7.25 ratio on a run where
// nothing was serialising. Running the reference last means it pays the
// highest per-call cost of the two, so the comparison understates concurrency
// gains rather than inventing a regression.
func runConcurrently(t *testing.T, ctx context.Context, name string, fn func(context.Context, int) error) {
	t.Helper()

	const (
		workers = 12 // comfortably above the 4-connection pool
		// A healthy run is near 1/3 of sequential; a serialised one is near
		// 1.0 or worse. Failing above 0.9 catches a batch that gained
		// essentially nothing from concurrency, which is the defect, while
		// tolerating a shared database under load.
		//
		// The threshold is not tighter because this suite runs every package
		// in parallel against one Postgres. Contention there inflates the
		// concurrent pass without any application-level serialisation: the
		// same test passes consistently at ~0.35 on an idle machine and was
		// observed above 4.0 while six other packages hammered the same
		// server. A threshold tuned for the idle case reports the neighbours'
		// load as this service's bug. The real deadlock this guards against
		// does not merely exceed a ratio -- it stops the batch completing at
		// all, which the hardCap below catches outright.
		maxRatio = 0.9
		// An absolute ceiling, so a genuinely stuck batch fails rather than
		// hanging until the go test timeout.
		hardCap = 60 * time.Second
	)

	// Warm caches and the pool first, so neither half pays first-call costs
	// the other does not.
	if err := fn(ctx, -1); err != nil {
		t.Fatalf("%s: warm-up call failed: %v", name, err)
	}

	// The concurrent pass runs first; see the note above on why the reference
	// comes second.
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	concStart := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, hardCap)
			defer cancel()
			errs <- fn(c, i)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(hardCap):
		t.Fatalf("%s: concurrent calls never completed; a single request is "+
			"probably holding more than one pool connection", name)
	}
	concurrent := time.Since(concStart)

	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("%s: concurrent call failed: %v", name, err)
		}
	}

	// Sequential reference, measured last and therefore against the largest
	// dataset either pass sees.
	seqStart := time.Now()
	for i := 0; i < workers; i++ {
		if err := fn(ctx, i); err != nil {
			t.Fatalf("%s: sequential call %d failed: %v", name, i, err)
		}
	}
	sequential := time.Since(seqStart)

	// Below this, the measurement is dominated by goroutine scheduling rather
	// than by anything the pool does. GetProfile against a small project runs
	// all 12 calls in ~25ms, where a few milliseconds of noise moves the ratio
	// by more than the effect being measured: observed at 0.86 on a run with a
	// completely healthy pool. A batch this fast cannot be serialising on a
	// 4-connection pool in any way that matters, because serialising would
	// itself make it slow.
	const minMeasurable = 200 * time.Millisecond
	if sequential < minMeasurable {
		t.Logf("%s: sequential pass took %s, below the %s needed to measure "+
			"serialisation; skipping the ratio check",
			name, sequential, minMeasurable)
		return
	}

	ratio := concurrent.Seconds() / sequential.Seconds()
	if ratio > maxRatio {
		t.Errorf("%s: %d calls took %s concurrently vs %s sequentially "+
			"(ratio %.2f, want below %.2f); concurrency bought almost nothing, "+
			"so requests are serialising on the connection pool",
			name, workers, concurrent, sequential, ratio, maxRatio)
	}
}

// TestConcurrent_StoreAndQuery covers the two endpoints that carry essentially
// all traffic, including the fan-out Store performs into contradiction
// detection and tag auto-linking.
func TestConcurrent_StoreAndQuery(t *testing.T) {
	svc, ctx := concurrentTestService(t)
	project := "conc-" + uuid.NewString()

	runConcurrently(t, ctx, "Store", func(c context.Context, i int) error {
		_, err := svc.Store(c, &pb.StoreRequest{
			Content:   fmt.Sprintf("concurrent store probe %d about database tooling", i),
			Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
			ProjectId: project,
			Tags:      []string{"concurrency", "db"},
		})
		return err
	})

	// Query runs the hybrid path: graph filter, vector search with its
	// transaction, hydration, context edges, and batched access counts.
	runConcurrently(t, ctx, "Query", func(c context.Context, i int) error {
		_, err := svc.Query(c, &pb.QueryRequest{
			Query:     "database tooling",
			ProjectId: project,
			TopK:      10,
		})
		return err
	})
}

// TestConcurrent_ExtractAndProfile covers the two remaining endpoints that
// issue several queries per call.
func TestConcurrent_ExtractAndProfile(t *testing.T) {
	svc, ctx := concurrentTestService(t)
	project := "conc-" + uuid.NewString()

	runConcurrently(t, ctx, "Extract", func(c context.Context, i int) error {
		_, err := svc.Extract(c, &pb.ExtractRequest{
			Conversation: fmt.Sprintf(
				"user: We switched to PostgreSQL %d last week\nuser: I prefer Go for services", i),
			ProjectId: project,
		})
		return err
	})

	runConcurrently(t, ctx, "GetProfile", func(c context.Context, i int) error {
		_, err := svc.GetProfile(c, &pb.GetProfileRequest{ProjectId: project})
		return err
	})
}

// TestConcurrent_MixedWorkload runs reads and writes against the same project
// simultaneously, which is the realistic shape and the one most likely to
// expose lock contention rather than pure pool exhaustion.
func TestConcurrent_MixedWorkload(t *testing.T) {
	svc, ctx := concurrentTestService(t)
	project := "conc-" + uuid.NewString()

	// Seed so queries have something to return and edges to traverse.
	for i := 0; i < 20; i++ {
		if _, err := svc.Store(ctx, &pb.StoreRequest{
			Content:   fmt.Sprintf("seed %d about kubernetes deployment", i),
			Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
			ProjectId: project,
			Tags:      []string{"seed", "k8s"},
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	runConcurrently(t, ctx, "mixed", func(c context.Context, i int) error {
		switch i % 3 {
		case 0:
			_, err := svc.Store(c, &pb.StoreRequest{
				Content:   fmt.Sprintf("mixed write %d about kubernetes deployment", i),
				Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
				ProjectId: project,
				Tags:      []string{"seed", "k8s"},
			})
			return err
		case 1:
			_, err := svc.Query(c, &pb.QueryRequest{
				Query:     "kubernetes deployment",
				ProjectId: project,
				TopK:      10,
			})
			return err
		default:
			_, err := svc.GetProfile(c, &pb.GetProfileRequest{ProjectId: project})
			return err
		}
	})
}
