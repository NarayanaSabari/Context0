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

// runConcurrently fires fn from more goroutines than the pool has connections
// and asserts that the batch finishes promptly.
//
// The deadline matters as much as the success check. A request that holds two
// connections does not necessarily fail: callers queue, the pool drains, and
// everything eventually succeeds -- just orders of magnitude slower. Measured
// against the real deadlock, this batch took 20s instead of 0.08s while still
// returning no errors. Asserting on elapsed time is what turns that into a
// detectable failure.
func runConcurrently(t *testing.T, ctx context.Context, name string, fn func(context.Context, int) error) {
	t.Helper()

	const (
		workers = 12 // comfortably above the 4-connection pool
		// Generous against real work (the healthy case is milliseconds) and far
		// below the seconds-scale stall that connection starvation produces.
		budget = 5 * time.Second
	)

	var wg sync.WaitGroup
	errs := make(chan error, workers)

	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			errs <- fn(c, i)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(budget * 3):
		t.Fatalf("%s: concurrent calls never completed; a single request is "+
			"probably holding more than one pool connection", name)
	}
	elapsed := time.Since(start)

	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("%s: concurrent call failed: %v", name, err)
		}
	}

	if elapsed > budget {
		t.Errorf("%s: %d concurrent calls took %s, well past the %s budget; "+
			"requests are serialising on the connection pool",
			name, workers, elapsed, budget)
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
