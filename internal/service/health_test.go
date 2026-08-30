package service

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"errors"
	"github.com/NarayanaSabari/Kora/internal/auth"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
)

// countingRepo records how often the expensive count queries actually run.
type countingRepo struct {
	nodeCalls atomic.Int64
	edgeCalls atomic.Int64
	pingCalls atomic.Int64

	mu      sync.Mutex
	delay   time.Duration
	failing error
}

func (r *countingRepo) Ping(context.Context) error {
	r.pingCalls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failing
}

// NodeCount returns a sentinel alongside its error rather than a zero value.
// A zero is what a caller that swallowed the error would also report, so a
// test asserting on the response body could not tell the two apart.
func (r *countingRepo) NodeCount(context.Context) (int64, error) {
	r.nodeCalls.Add(1)
	r.mu.Lock()
	d, err := r.delay, r.failing
	r.mu.Unlock()
	time.Sleep(d)
	if err != nil {
		return -1, err
	}
	return 42, nil
}

func (r *countingRepo) EdgeCount(context.Context) (int64, error) {
	r.edgeCalls.Add(1)
	r.mu.Lock()
	err := r.failing
	r.mu.Unlock()
	if err != nil {
		return -1, err
	}
	return 99, nil
}

func newTestHealth(repo healthRepo) *HealthService {
	return &HealthService{repo: repo, version: "test"}
}

// authedCtx marks a request as carrying a verified credential.
//
// Health withholds graph statistics from anonymous callers, so a test that
// wants to exercise the counting path has to say who is asking.
func authedCtx() context.Context {
	return auth.WithAuthenticated(context.Background())
}

// TestHealthCachesGraphCounts: /v1/health is unauthenticated, so recomputing
// two full table scans per call let anyone who can reach the port make the
// database do unbounded work. Measured at 9.5k vertices this was 430ms serial
// and a 2.2s p50 under eight clients.
func TestHealthCachesGraphCounts(t *testing.T) {
	repo := &countingRepo{}
	h := newTestHealth(repo)

	for range 50 {
		resp, err := h.Health(authedCtx(), &pb.HealthRequest{})
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if resp.NodeCount != 42 || resp.EdgeCount != 99 {
			t.Fatalf("wrong counts: %d/%d", resp.NodeCount, resp.EdgeCount)
		}
	}

	if got := repo.nodeCalls.Load(); got != 1 {
		t.Errorf("NodeCount ran %d times over 50 calls, want 1: counts must be cached", got)
	}
	// Reachability is the actual health signal and must never be cached.
	if got := repo.pingCalls.Load(); got != 50 {
		t.Errorf("Ping ran %d times over 50 calls, want 50: liveness must not be cached", got)
	}
}

// TestHealthCollapsesConcurrentRecomputes: without collapsing, every client
// arriving on an expired cache starts its own pair of scans, which is the same
// stampede the cache exists to prevent and is worst under load.
func TestHealthCollapsesConcurrentRecomputes(t *testing.T) {
	repo := &countingRepo{delay: 50 * time.Millisecond}
	h := newTestHealth(repo)

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Health(authedCtx(), &pb.HealthRequest{}); err != nil {
				t.Errorf("health: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := repo.nodeCalls.Load(); got != 1 {
		t.Errorf("32 concurrent callers caused %d recomputes, want 1", got)
	}
}

// TestHealthRecomputesAfterTTL guards against the cache never refreshing, which
// would make the counts permanently wrong rather than briefly stale.
func TestHealthRecomputesAfterTTL(t *testing.T) {
	repo := &countingRepo{}
	h := newTestHealth(repo)

	if _, err := h.Health(authedCtx(), &pb.HealthRequest{}); err != nil {
		t.Fatalf("health: %v", err)
	}
	// Age the cache past its TTL rather than sleeping for it.
	h.mu.Lock()
	h.statsAt = time.Now().Add(-statsTTL - time.Second)
	h.mu.Unlock()

	if _, err := h.Health(authedCtx(), &pb.HealthRequest{}); err != nil {
		t.Fatalf("health: %v", err)
	}
	if got := repo.nodeCalls.Load(); got != 2 {
		t.Errorf("NodeCount ran %d times, want 2: the cache must expire", got)
	}
}

// TestHealthFailsWhenDatabaseUnreachable: the endpoint must still report a real
// outage. Caching the counts must not make a dead database look healthy.
func TestHealthFailsWhenDatabaseUnreachable(t *testing.T) {
	repo := &countingRepo{}
	h := newTestHealth(repo)

	// Warm the cache while healthy.
	if _, err := h.Health(authedCtx(), &pb.HealthRequest{}); err != nil {
		t.Fatalf("health: %v", err)
	}

	repo.mu.Lock()
	repo.failing = errors.New("connection refused")
	repo.mu.Unlock()

	resp, err := h.Health(authedCtx(), &pb.HealthRequest{})
	if err == nil {
		t.Errorf("health reported ok with the database unreachable "+
			"(status %q, nodes %d, edges %d): a health check that cannot reach "+
			"the database must not answer ok, or a broken pod keeps taking traffic",
			resp.GetStatus(), resp.GetNodeCount(), resp.GetEdgeCount())
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("got code %v, want Internal", got)
	}
	// A swallowed count error must not surface as a plausible-looking zero.
	if resp.GetNodeCount() != 0 || resp.GetEdgeCount() != 0 {
		t.Errorf("failed health returned counts nodes=%d edges=%d, want no payload",
			resp.GetNodeCount(), resp.GetEdgeCount())
	}
}

// TestHealthFailsWhenCountsFailOnFirstCall covers the cold-cache path.
//
// TestHealthFailsWhenDatabaseUnreachable warms the cache first, so it exercises
// a recompute after a successful one. A pod that has never answered a health
// call -- which is every pod at startup -- takes a different path, and the
// error handling there was not asserted at all.
func TestHealthFailsWhenCountsFailOnFirstCall(t *testing.T) {
	repo := &countingRepo{failing: errors.New("connection refused")}
	h := newTestHealth(repo)

	resp, err := h.Health(authedCtx(), &pb.HealthRequest{})
	if err == nil {
		t.Fatalf("health reported ok on a cold cache with the database "+
			"unreachable: status %q, nodes %d", resp.GetStatus(), resp.GetNodeCount())
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("got code %v, want Internal", got)
	}
}

// TestHealthFailsWhenEdgeCountFails: the second of the two counts has its own
// error branch, and a health check that reports ok because only one of them
// succeeded is still reporting on a database it cannot fully read.
func TestHealthFailsWhenEdgeCountFails(t *testing.T) {
	repo := &edgeFailingRepo{}
	h := newTestHealth(repo)

	resp, err := h.Health(authedCtx(), &pb.HealthRequest{})
	if err == nil {
		t.Fatalf("health reported ok while EdgeCount was failing: "+
			"status %q, nodes %d, edges %d",
			resp.GetStatus(), resp.GetNodeCount(), resp.GetEdgeCount())
	}
}

// edgeFailingRepo succeeds at NodeCount and fails only at EdgeCount, isolating
// the second error branch.
type edgeFailingRepo struct{}

func (r *edgeFailingRepo) Ping(context.Context) error { return nil }

func (r *edgeFailingRepo) NodeCount(context.Context) (int64, error) { return 42, nil }

func (r *edgeFailingRepo) EdgeCount(context.Context) (int64, error) {
	return -1, errors.New("edge count failed")
}

// TestHealthFailsWhenNodeCountFails isolates the first of the two count error
// branches.
//
// A repo that fails both counts cannot distinguish them: dropping the
// NodeCount error still fails at EdgeCount, so the test passes either way.
// Failing only NodeCount is what makes that branch observable.
func TestHealthFailsWhenNodeCountFails(t *testing.T) {
	h := newTestHealth(&nodeFailingRepo{})

	resp, err := h.Health(authedCtx(), &pb.HealthRequest{})
	if err == nil {
		t.Fatalf("health reported ok while NodeCount was failing: "+
			"status %q, nodes %d, edges %d -- a count error must not be "+
			"reported as a healthy graph",
			resp.GetStatus(), resp.GetNodeCount(), resp.GetEdgeCount())
	}
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("got code %v, want Internal", got)
	}
}

// nodeFailingRepo fails only at NodeCount; EdgeCount succeeds, so nothing
// downstream masks the first error.
type nodeFailingRepo struct{}

func (r *nodeFailingRepo) Ping(context.Context) error { return nil }

func (r *nodeFailingRepo) NodeCount(context.Context) (int64, error) {
	return -1, errors.New("node count failed")
}

func (r *nodeFailingRepo) EdgeCount(context.Context) (int64, error) { return 99, nil }

// TestHealthWithholdsStatisticsFromAnonymousCallers pins the disclosure
// boundary.
//
// This endpoint answers without a credential because Kubernetes probes cannot
// present one. But it was returning the engine version, node count and edge
// count to anyone who could reach the port: `kora stats` with no API key
// at all printed them. None is a secret alone; together they say what is
// running and how much data is in it, which a liveness probe has no need for.
func TestHealthWithholdsStatisticsFromAnonymousCallers(t *testing.T) {
	repo := &countingRepo{}
	h := newTestHealth(repo)

	anon, err := h.Health(context.Background(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("health must still answer without a credential: %v", err)
	}
	if anon.Status != "ok" {
		t.Errorf("status = %q, want ok: probes depend on this", anon.Status)
	}
	if anon.NodeCount != 0 || anon.EdgeCount != 0 {
		t.Errorf("anonymous caller received graph statistics: nodes=%d edges=%d",
			anon.NodeCount, anon.EdgeCount)
	}
	if anon.Version != "" {
		t.Errorf("anonymous caller received the version %q", anon.Version)
	}
	// The counts must not even be computed for an anonymous caller, or the
	// endpoint remains a lever for making the database work.
	if repo.nodeCalls.Load() != 0 {
		t.Errorf("counted the graph for an anonymous caller (%d calls)", repo.nodeCalls.Load())
	}

	authed, err := h.Health(authedCtx(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if authed.NodeCount == 0 || authed.Version == "" {
		t.Errorf("an authenticated caller lost the statistics: nodes=%d version=%q",
			authed.NodeCount, authed.Version)
	}
}

// blockingRepo blocks in NodeCount until released or its context ends, so a
// second caller collapses into the same singleflight flight.
type blockingRepo struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingRepo) Ping(context.Context) error { return nil }

func (r *blockingRepo) NodeCount(ctx context.Context) (int64, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return 42, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (r *blockingRepo) EdgeCount(context.Context) (int64, error) { return 99, nil }

// TestHealthSurvivesADisconnectedCaller covers a defect in how the shared
// stats recompute borrowed a context.
//
// singleflight collapses concurrent callers into one execution, and that
// execution captured whichever caller's context arrived first. When that
// caller went away -- a disconnected client, a request that hit its deadline --
// the shared computation was cancelled and every other caller waiting on the
// same flight received the cancellation, despite their own contexts being
// perfectly healthy.
//
// The visible failure is a health check reporting Internal because an
// unrelated client hung up, which is exactly when a health check must not lie.
// Found when a full parallel test run failed with "failed to get graph
// statistics: context canceled" in tests that never cancelled anything.
func TestHealthSurvivesADisconnectedCaller(t *testing.T) {
	repo := &blockingRepo{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	h := newTestHealth(repo)

	// Caller A starts the flight, then disconnects.
	ctxA, cancelA := context.WithCancel(authedCtx())
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		_, _ = h.Health(ctxA, &pb.HealthRequest{})
	}()

	select {
	case <-repo.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first caller never reached the repository")
	}

	// Caller B joins the same flight with a healthy context.
	errB := make(chan error, 1)
	go func() {
		_, err := h.Health(authedCtx(), &pb.HealthRequest{})
		errB <- err
	}()

	// Give B time to collapse into the flight, then disconnect A.
	time.Sleep(200 * time.Millisecond)
	cancelA()

	// The work can now finish.
	close(repo.release)

	select {
	case err := <-errB:
		if err != nil {
			t.Errorf("a healthy caller's Health failed because another caller "+
				"disconnected: %v -- one client hanging up must not fail the "+
				"health check for everyone sharing the recompute", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("the healthy caller never returned")
	}
	<-doneA
}

// TestHealthStatsRecomputeIsBounded: once the shared flight stops borrowing a
// caller's context it has no deadline of its own, so a database that accepts
// the connection and then stalls would pin the flight and every caller waiting
// on it. statsTimeout is what bounds that.
func TestHealthStatsRecomputeIsBounded(t *testing.T) {
	if statsTimeout <= 0 {
		t.Fatal("the shared stats recompute has no timeout; a stalled database " +
			"would pin every caller waiting on the flight")
	}
	if statsTimeout > time.Minute {
		t.Errorf("statsTimeout is %s, too long to bound a health check", statsTimeout)
	}
}

// A deployment on the zero-dependency defaults must say so.
//
// The defaults are the right ones -- a fresh install makes no network call --
// but they are a floor on retrieval quality, not a measure of it, and the
// engine answers happily either way. Health is where an operator asks "what is
// this actually running", so the answer has to be there rather than inferred
// from a chart or an environment variable they cannot see.
func TestHealth_ReportsWhichBackendsAreRunning(t *testing.T) {
	tests := []struct {
		name      string
		providers Providers
		wantZero  bool
	}{
		{"offline defaults", Providers{Embedding: "bag-of-words", Extraction: "rule"}, true},
		// Unset is the same state: an operator who set nothing gets the
		// defaults, and the warning has to fire for them too.
		{"unset", Providers{}, true},
		{"the alias for the default embedder", Providers{Embedding: "bow", Extraction: "rule"}, true},
		{"real embedder, default extractor", Providers{Embedding: "ollama", Extraction: "rule"}, false},
		{"default embedder, real extractor", Providers{Embedding: "bag-of-words", Extraction: "llm"}, false},
		{"both real", Providers{Embedding: "google", Extraction: "llm"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.providers.ZeroDependencyDefaults(); got != tt.wantZero {
				t.Errorf("ZeroDependencyDefaults() = %v, want %v for %+v", got, tt.wantZero, tt.providers)
			}
		})
	}
}

// The reported names are the ones configured, not a guess reconstructed from
// what happens to be reachable.
func TestHealth_ProviderNamesSurviveToTheResponse(t *testing.T) {
	svc := &HealthService{
		version:   "test",
		providers: Providers{Embedding: "ollama", Extraction: "llm"},
	}

	if svc.providers.Embedding != "ollama" || svc.providers.Extraction != "llm" {
		t.Fatalf("providers not held as given: %+v", svc.providers)
	}
	if svc.providers.ZeroDependencyDefaults() {
		t.Error("a deployment running Ollama and an LLM extractor is not on zero-dependency defaults")
	}
}

// The ablation mode must be visible to an authenticated caller.
//
// A benchmark number produced against an ablated deployment measures the
// ablated engine, and this field is the only way to tell the two apart after
// the fact. See issue #86.
func TestHealthReportsGraphSignalsDisabled(t *testing.T) {
	h := &HealthService{repo: &countingRepo{}, version: "test",
		providers: Providers{GraphSignalsDisabled: true}}

	resp, err := h.Health(authedCtx(), &pb.HealthRequest{})
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !resp.GraphSignalsDisabled {
		t.Error("graph_signals_disabled not reported: an ablated deployment is " +
			"indistinguishable from a normal one, which is the defect the field exists to prevent")
	}
}
