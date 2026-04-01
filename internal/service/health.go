package service

import (
	"context"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/graph"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HealthService implements the HealthService gRPC service.
type HealthService struct {
	pb.UnimplementedHealthServiceServer
	repo    graph.Repository
	version string
}

// NewHealthService creates a new HealthService.
func NewHealthService(repo graph.Repository, version string) *HealthService {
	return &HealthService{repo: repo, version: version}
}

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
