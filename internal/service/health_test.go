package service

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"errors"
	"github.com/context0/context0/internal/auth"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
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
// count to anyone who could reach the port: `context0 stats` with no API key
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
