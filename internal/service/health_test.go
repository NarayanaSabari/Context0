package service

import (
	"context"
	"errors"
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

func (r *countingRepo) NodeCount(context.Context) (int64, error) {
	r.nodeCalls.Add(1)
	r.mu.Lock()
	d, err := r.delay, r.failing
	r.mu.Unlock()
	time.Sleep(d)
	if err != nil {
		return 0, err
	}
	return 42, nil
}

func (r *countingRepo) EdgeCount(context.Context) (int64, error) {
	r.edgeCalls.Add(1)
	r.mu.Lock()
	err := r.failing
	r.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return 99, nil
}

func newTestHealth(repo healthRepo) *HealthService {
	return &HealthService{repo: repo, version: "test"}
}

// TestHealthCachesGraphCounts: /v1/health is unauthenticated, so recomputing
// two full table scans per call let anyone who can reach the port make the
// database do unbounded work. Measured at 9.5k vertices this was 430ms serial
// and a 2.2s p50 under eight clients.
func TestHealthCachesGraphCounts(t *testing.T) {
	repo := &countingRepo{}
	h := newTestHealth(repo)

	for range 50 {
		resp, err := h.Health(context.Background(), &pb.HealthRequest{})
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
			if _, err := h.Health(context.Background(), &pb.HealthRequest{}); err != nil {
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

	if _, err := h.Health(context.Background(), &pb.HealthRequest{}); err != nil {
		t.Fatalf("health: %v", err)
	}
	// Age the cache past its TTL rather than sleeping for it.
	h.mu.Lock()
	h.statsAt = time.Now().Add(-statsTTL - time.Second)
	h.mu.Unlock()

	if _, err := h.Health(context.Background(), &pb.HealthRequest{}); err != nil {
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
	if _, err := h.Health(context.Background(), &pb.HealthRequest{}); err != nil {
		t.Fatalf("health: %v", err)
	}

	repo.mu.Lock()
	repo.failing = errors.New("connection refused")
	repo.mu.Unlock()

	if _, err := h.Health(context.Background(), &pb.HealthRequest{}); err == nil {
		t.Error("health reported ok with the database unreachable")
	}
}
