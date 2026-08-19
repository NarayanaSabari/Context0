// health.go implements the HealthService gRPC handler. It answers the question
// "is the backing store reachable", and reports graph statistics alongside it.
//
// The statistics are cached. Counting is the expensive part: `MATCH (n) RETURN
// count(n)` and `MATCH ()-[e]->() RETURN count(e)` are full scans of the vertex
// and edge tables, and this endpoint is deliberately unauthenticated so that
// probes and monitoring can reach it. Recomputing on every call made a public
// endpoint a lever for making the database do unbounded work: measured at 9.5k
// vertices and 107k edges, /v1/health cost 430ms served serially and a 2.2s p50
// under eight concurrent clients, while every other endpoint stayed in single
// digit milliseconds.
//
// Counts are a dashboard number, not a control signal -- nothing takes a
// decision on whether the graph holds 9,540 or 9,551 vertices -- so a few
// seconds of staleness costs nothing. Reachability, the part that must not be
// stale, is still checked on every call.

package service

import (
	"context"

	"github.com/context0/context0/internal/auth"
	"sync"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/graph"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// statsTTL is how long cached graph counts are served before being recomputed.
//
// Five seconds keeps a scrape at any typical interval (10s, 15s, 30s) from
// paying for a scan while remaining fresh enough that the number tracks reality
// on any human timescale.
const statsTTL = 5 * time.Second

// healthRepo is the subset of the repository this service uses. Narrow by
// design: it keeps the caching behaviour testable without a live database,
// which is the only way to assert how many times the counts are actually
// recomputed. Mirrors the Pinger interface used by the HTTP probes.
type healthRepo interface {
	Ping(ctx context.Context) error
	NodeCount(ctx context.Context) (int64, error)
	EdgeCount(ctx context.Context) (int64, error)
}

// HealthService implements the HealthService gRPC service.
type HealthService struct {
	pb.UnimplementedHealthServiceServer
	repo    healthRepo
	version string

	// group collapses concurrent recomputes into one. Without it, N clients
	// arriving on an expired cache each launch their own pair of full scans,
	// which is the same stampede the cache exists to prevent -- and it is worst
	// exactly when load is highest.
	group singleflight.Group

	mu       sync.RWMutex
	stats    graphStats
	statsAt  time.Time
	hasStats bool
}

// graphStats is the cached payload.
type graphStats struct {
	nodes int64
	edges int64
}

// NewHealthService creates a new HealthService with the given graph repository
// and server version string. The version is returned verbatim in responses.
func NewHealthService(repo *graph.AGERepository, version string) *HealthService {
	return &HealthService{repo: repo, version: version}
}

// Health reports service status and graph statistics.
//
// Reachability is verified on every call via a cheap probe. The counts may be
// up to statsTTL old; see the file comment for why that trade is deliberate.
func (s *HealthService) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	// Liveness of the backing store is the actual health signal, so it is never
	// served from cache. Ping is a round trip to Postgres, not a scan.
	if err := s.repo.Ping(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "database unreachable: %v", err)
	}

	// An unauthenticated caller gets liveness and nothing else.
	//
	// This endpoint answers without a credential because Kubernetes probes
	// cannot present one, but it was volunteering the version, node count and
	// edge count to anyone who could reach the port -- `context0 stats` with no
	// key at all returned them. Those are not secrets individually; together
	// they are a free reconnaissance signal (what is running, and how much data
	// is in it) that a probe has no need for.
	if !auth.IsAuthenticated(ctx) {
		return &pb.HealthResponse{Status: "ok"}, nil
	}

	stats, err := s.graphStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get graph statistics: %v", err)
	}

	return &pb.HealthResponse{
		Status:    "ok",
		Version:   s.version,
		NodeCount: stats.nodes,
		EdgeCount: stats.edges,
	}, nil
}

// graphStats returns cached counts, recomputing them at most once per statsTTL
// across all concurrent callers.
func (s *HealthService) graphStats(ctx context.Context) (graphStats, error) {
	s.mu.RLock()
	cached, at, ok := s.stats, s.statsAt, s.hasStats
	s.mu.RUnlock()

	if ok && time.Since(at) < statsTTL {
		return cached, nil
	}

	v, err, _ := s.group.Do("stats", func() (any, error) {
		// Re-check under the flight: a caller that queued behind an in-progress
		// recompute would otherwise immediately trigger a second one.
		s.mu.RLock()
		cached, at, ok := s.stats, s.statsAt, s.hasStats
		s.mu.RUnlock()
		if ok && time.Since(at) < statsTTL {
			return cached, nil
		}

		nodes, err := s.repo.NodeCount(ctx)
		if err != nil {
			return graphStats{}, err
		}
		edges, err := s.repo.EdgeCount(ctx)
		if err != nil {
			return graphStats{}, err
		}

		fresh := graphStats{nodes: nodes, edges: edges}
		s.mu.Lock()
		s.stats, s.statsAt, s.hasStats = fresh, time.Now(), true
		s.mu.Unlock()
		return fresh, nil
	})
	if err != nil {
		// Serving a stale count beats failing the endpoint: the reachability
		// check above already passed, so the service is healthy and only the
		// decorative number is unavailable.
		if ok {
			return cached, nil
		}
		return graphStats{}, err
	}
	return v.(graphStats), nil
}
