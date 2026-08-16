// health.go implements the HealthService gRPC handler, providing a lightweight
// liveness/readiness probe that also reports graph statistics (node and edge
// counts) and the current server version.

package service

import (
	"context"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/graph"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HealthService implements the HealthService gRPC service. It exposes a single
// Health RPC that verifies the graph repository is reachable and returns summary
// statistics about the stored data.
type HealthService struct {
	pb.UnimplementedHealthServiceServer
	repo    *graph.AGERepository
	version string
}

// NewHealthService creates a new HealthService with the given graph repository
// and server version string. The version is returned verbatim in health responses.
func NewHealthService(repo *graph.AGERepository, version string) *HealthService {
	return &HealthService{repo: repo, version: version}
}

// Health checks the system status by querying the graph repository for node and
// edge counts. If either query fails, a gRPC Internal error is returned, signaling
// that the backing store is unhealthy.
func (s *HealthService) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	nodeCount, err := s.repo.NodeCount(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get node count: %v", err)
	}

	edgeCount, err := s.repo.EdgeCount(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get edge count: %v", err)
	}

	return &pb.HealthResponse{
		Status:    "ok",
		Version:   s.version,
		NodeCount: nodeCount,
		EdgeCount: edgeCount,
	}, nil
}
