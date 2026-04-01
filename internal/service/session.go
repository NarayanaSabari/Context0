package service

import (
	"context"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/metrics"
	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SessionService implements the SessionService gRPC service.
type SessionService struct {
	pb.UnimplementedSessionServiceServer
	repo graph.Repository
}

// NewSessionService creates a new SessionService.
func NewSessionService(repo graph.Repository) *SessionService {
	return &SessionService{repo: repo}
}

func (s *SessionService) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	if req.ProjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	sess := model.Session{
		ID:        uuid.New(),
		ProjectID: req.ProjectId,
		AgentID:   req.AgentId,
		StartedAt: time.Now().UTC(),
	}

	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	metrics.ActiveSessions.Inc()

	return &pb.StartSessionResponse{
		Session: sessionToProto(sess),
	}, nil
}

func (s *SessionService) EndSession(ctx context.Context, req *pb.EndSessionRequest) (*pb.EndSessionResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid session id: %v", err)
	}

	sess, err := s.repo.EndSession(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to end session: %v", err)
	}

	metrics.ActiveSessions.Dec()

	return &pb.EndSessionResponse{
		Session: sessionToProto(sess),
	}, nil
}

func sessionToProto(s model.Session) *pb.Session {
	pbSess := &pb.Session{
		Id:        s.ID.String(),
		ProjectId: s.ProjectID,
		AgentId:   s.AgentID,
		StartedAt: timestamppb.New(s.StartedAt),
	}
	if s.EndedAt != nil {
		pbSess.EndedAt = timestamppb.New(*s.EndedAt)
	}
	return pbSess
}
