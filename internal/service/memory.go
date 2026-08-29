// Package service implements the gRPC service layer for Kora, the memory graph
// system. It orchestrates the core memory lifecycle: Store, Query, Extract, and
// GetProfile. Each operation coordinates between the graph repository (for
// persistent storage and traversal), the embedding layer (for vector search), and
// the extraction/ranking subsystems.
//
// The primary flow is:
//
//  1. Store -- persist a memory node, generate its embedding, link it to a session,
//     detect contradictions with existing memories, and auto-link by shared tags.
//  2. Query -- hybrid retrieval combining graph-based keyword/tag matching with
//     vector similarity search, merged and ranked by a weighted scoring function.
//  3. Extract -- parse a raw conversation into structured memories using either
//     an LLM provider or rule-based heuristics, then store each extracted memory.
//  4. GetProfile -- build a user/project profile by splitting memories into a
//     static layer (semantic facts, procedures) and a dynamic layer (recent episodes).
package service

import (
	"log/slog"
	"math"

	"context"
	"fmt"
	"github.com/NarayanaSabari/Kora/internal/logging"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/extraction"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/ingest"
	"github.com/NarayanaSabari/Kora/internal/metrics"
	"github.com/NarayanaSabari/Kora/internal/retrieval"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MemoryService implements the Kora gRPC service.
//
// It is the wire layer and little else now: protobuf in, protobuf out, and the
// arguments that belong to the wire format -- notably session ids, where Store
// rejects a malformed one and Extract warns and carries on. The behaviour sits
// in the two engines either side of the store, internal/ingest and
// internal/retrieval.
type MemoryService struct {
	pb.UnimplementedKoraServer
	repo     *graph.AGERepository
	embedder embedding.Embedder

	// The two halves of the engine's behaviour, either side of the store.
	// Constructed here rather than injected because the service is what
	// decides the engine's dependencies; the interfaces they take exist so
	// each half can be tested without a gRPC request around it.
	retrieval *retrieval.Engine
	ingest    *ingest.Engine
}

// NewMemoryService creates a new MemoryService with the given graph repository
// and embedder. The embedder may be nil, in which case vector search is disabled
// and the service operates in graph-only mode.
//
// Extraction defaults to the zero-dependency rule-based scanner. Use
// NewMemoryServiceWithExtractor to supply another.
func NewMemoryService(repo *graph.AGERepository, embedder embedding.Embedder) *MemoryService {
	return NewMemoryServiceWithExtractor(repo, embedder, extraction.RuleExtractor{})
}

// NewMemoryServiceWithExtractor is NewMemoryService with an explicit
// extraction backend. A nil extractor falls back to the rule-based scanner --
// in ingest.New, which is the only place that decision is now made. The
// service used to make it too, and duplicate defence in two layers is how the
// two drift apart.
func NewMemoryServiceWithExtractor(repo *graph.AGERepository, embedder embedding.Embedder, extractor extraction.Extractor) *MemoryService {
	return &MemoryService{
		repo:      repo,
		embedder:  embedder,
		retrieval: retrieval.New(repo, embedder),
		ingest:    ingest.New(repo, embedder, extractor),
	}
}

// Store persists a new memory node into the graph. The full pipeline is:
//  1. Validate input (content and project_id are required).
//  2. Create the memory node in the graph repository.
//  3. Generate and store an embedding vector for future similarity search.
//  4. Link the memory to a session if session_id is provided.
//  5. Detect contradictions with existing semantic memories and create supersedes edges.
//  6. Auto-link to other memories that share overlapping tags via relates_to edges.
func (s *MemoryService) Store(ctx context.Context, req *pb.StoreRequest) (*pb.StoreResponse, error) {
	timer := prometheus.NewTimer(metrics.StoreDuration)
	defer timer.ObserveDuration()

	if req.Content == "" {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}
	if req.ProjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}

	memType, err := protoToMemoryType(req.Type)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid type: %v", err)
	}

	mem := model.Memory{
		ID:          uuid.New(),
		Content:     req.Content,
		Type:        memType,
		ProjectID:   req.ProjectId,
		Tags:        req.Tags,
		CreatedAt:   time.Now().UTC(),
		AccessCount: 0,
		DecayScore:  1.0,
	}

	// Session ids are a wire concern: a malformed one is an argument error, and
	// argument errors belong to the layer that owns the wire format.
	var sessionID uuid.UUID
	if req.SessionId != "" {
		parsed, err := uuid.Parse(req.SessionId)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
		}
		sessionID = parsed
	}

	if err := s.ingest.Store(ctx, mem, sessionID); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to store memory: %v", err)
	}

	metrics.MemoriesTotal.WithLabelValues(string(memType)).Inc()

	return &pb.StoreResponse{
		Memory: memoryToProto(mem),
	}, nil
}

// Query performs hybrid retrieval over the memory graph. It combines:
//   - Graph-based retrieval using keyword/tag matching and type filters.
//   - Vector-based retrieval using cosine similarity on embeddings.
//
// Results from both methods are merged (duplicates are boosted by 50%), ranked
// using the weighted scoring function in the ranking package, and truncated to
// the requested top-K count. Access counts are incremented for all returned memories
// so the decay/consolidation pipeline can factor in usage frequency.
func (s *MemoryService) Query(ctx context.Context, req *pb.QueryRequest) (*pb.QueryResponse, error) {
	timer := prometheus.NewTimer(metrics.QueryDuration)
	defer timer.ObserveDuration()

	// project_id is optional: empty searches every project in the deployment.
	// That is a scoping decision, not an authorisation one -- an API key is not
	// bound to a project. See docs/adr/0002-one-deployment-is-one-trust-domain.md.

	var types []model.MemoryType
	for _, t := range req.Types {
		mt, err := protoToMemoryType(t)
		if err != nil {
			continue
		}
		types = append(types, mt)
	}

	// Retrieval lives in internal/retrieval: three retrievers, a fallback for
	// queries with nothing to search for, and the merge that puts them on one
	// scale. What remains here is what serving a request needs -- protocol
	// translation, context edges, access counts, metrics.
	results, err := s.retrieval.Retrieve(ctx, req.Query, req.ProjectId, types, req.TopK)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "retrieval failed: %v", err)
	}

	// Populate context edges for the (already truncated) top-K results in a
	// single round trip, and increment access counts for returned results.
	ids := make([]uuid.UUID, len(results))
	for i, r := range results {
		ids[i] = r.Memory.ID
	}
	// Context is supplementary, not required: a failure here degrades
	// gracefully since a nil map reads as empty for every id.
	contextEdges, cerr := s.repo.GetContextEdges(ctx, ids)
	if cerr != nil {
		logging.FromContext(ctx).Warn("loading context edges failed; results returned without context",
			slog.Int("result_count", len(ids)),
			slog.Any("error", cerr))
	}
	for i, r := range results {
		results[i].Context = contextEdges[r.Memory.ID]
	}

	// One statement for every result rather than a round trip each. Access
	// counts feed ranking and consolidation, so a failure here skews future
	// ordering slightly but must not fail the read the caller asked for.
	if err := s.repo.IncrementAccessCounts(ctx, ids); err != nil {
		logging.FromContext(ctx).Warn("incrementing access counts failed; ranking and consolidation will see stale frequencies",
			slog.Int("result_count", len(ids)),
			slog.Any("error", err))
	}

	metrics.QueryResultsCount.Observe(float64(len(results)))

	var pbResults []*pb.MemoryWithContext
	for _, r := range results {
		pbResults = append(pbResults, memoryWithContextToProto(r))
	}

	return &pb.QueryResponse{Results: pbResults}, nil
}

// Connect creates a directed edge between two existing memory nodes. Both nodes
// must already exist in the graph. If no weight is specified, the edge defaults
// to a weight of 1.0. The edge relationship type must be a valid RelationshipType.
func (s *MemoryService) Connect(ctx context.Context, req *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	if req.FromId == "" || req.ToId == "" {
		return nil, status.Error(codes.InvalidArgument, "from_id and to_id are required")
	}

	fromID, err := uuid.Parse(req.FromId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid from_id: %v", err)
	}
	toID, err := uuid.Parse(req.ToId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid to_id: %v", err)
	}

	// Verify both memories exist.
	if _, err := s.repo.GetMemory(ctx, fromID); err != nil {
		return nil, status.Errorf(codes.NotFound, "from memory not found: %v", err)
	}
	if _, err := s.repo.GetMemory(ctx, toID); err != nil {
		return nil, status.Errorf(codes.NotFound, "to memory not found: %v", err)
	}

	relType, err := protoToRelType(req.Relationship)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid relationship: %v", err)
	}

	weight := req.Weight
	if weight <= 0 {
		weight = 1.0
	}

	edge := model.Edge{
		ID:           uuid.New(),
		FromID:       fromID,
		ToID:         toID,
		Relationship: relType,
		Weight:       weight,
		CreatedAt:    time.Now().UTC(),
	}

	effective, err := s.repo.CreateEdge(ctx, edge)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create edge: %v", err)
	}

	metrics.EdgesTotal.WithLabelValues(string(relType)).Inc()

	return &pb.ConnectResponse{
		Edge: edgeToProto(effective),
	}, nil
}

// Delete removes a memory node and its associated edges from the graph.
func (s *MemoryService) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	id, err := uuid.Parse(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id: %v", err)
	}

	if err := s.repo.DeleteMemory(ctx, id); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete memory: %v", err)
	}

	return &pb.DeleteResponse{}, nil
}

// GetGraph returns a subgraph centered on a given memory node. This is used
// for graph visualization and context exploration.
func (s *MemoryService) GetGraph(ctx context.Context, req *pb.GetGraphRequest) (*pb.GetGraphResponse, error) {
	if req.CenterId == "" {
		return nil, status.Error(codes.InvalidArgument, "center_id is required")
	}

	centerID, err := uuid.Parse(req.CenterId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid center_id: %v", err)
	}

	nodes, edges, err := s.repo.GetSubgraph(ctx, centerID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get subgraph: %v", err)
	}

	var pbNodes []*pb.Memory
	for _, n := range nodes {
		pbNodes = append(pbNodes, memoryToProto(n))
	}

	var pbEdges []*pb.Edge
	for _, e := range edges {
		pbEdges = append(pbEdges, edgeToProto(e))
	}

	return &pb.GetGraphResponse{
		Nodes: pbNodes,
		Edges: pbEdges,
	}, nil
}

// Extract processes a raw conversation string and automatically extracts structured
// memories from it. The extraction strategy is whichever Extractor the service
// was built with: the rule-based scanner by default, or an LLM-backed one when
// configured. Each extracted memory is persisted, embedded, optionally linked
// to a session, and auto-linked to related memories.
//
// Memories that restate something already stored are consolidated rather than
// added, in three stages of increasing cost:
//
//  1. Within the conversation, before anything is written or embedded
//     (extraction.FoldRedundant).
//  2. Against the project, by exact content hash, in one query for the whole
//     batch (FindByContentHash).
//  3. Against the nearest stored memories, by lexical subsumption over the
//     neighbours the write already fetches (consolidateAgainst).
//
// This is a cost fix, not an accuracy fix. Measured on retrieved results, only
// 1% of retrieval slots were exact duplicates and 2% near duplicates, so
// ranking already suppressed them; what redundancy cost was 10x the rows, 10x
// the edges, and 10x the embedding spend at ingest.
func (s *MemoryService) Extract(ctx context.Context, req *pb.ExtractRequest) (*pb.ExtractResponse, error) {
	if req.Conversation == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation is required")
	}
	if req.ProjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}

	// A malformed session_id warns rather than failing: discarding a whole
	// conversation over one bad field is the wrong trade, and silence was the
	// old bug -- a typo unlinked every extracted memory while the response
	// still reported success.
	var sessionID uuid.UUID
	if req.SessionId != "" {
		parsed, err := uuid.Parse(req.SessionId)
		if err != nil {
			logging.FromContext(ctx).Warn("session_id is not a valid UUID; extracted memories will not be linked to a session",
				slog.String("session_id", req.SessionId),
				slog.Any("error", err))
		} else {
			sessionID = parsed
		}
	}

	stored, relCount, err := s.ingest.Extract(ctx, req.Conversation, req.ProjectId, sessionID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "extraction failed: %v", err)
	}

	pbMemories := make([]*pb.Memory, 0, len(stored))
	for i := range stored {
		pbMemories = append(pbMemories, memoryToProto(stored[i]))
	}

	// Narrowed with an explicit bound rather than min(), which gosec cannot see
	// through: the proto field is int32 and nothing caps how many edges one
	// conversation can produce. Clamped rather than wrapped, so an
	// implausible count reports the largest number it can instead of a
	// negative one.
	var relationships int32
	switch {
	case relCount < 0:
		relationships = 0
	case relCount > math.MaxInt32:
		relationships = math.MaxInt32
	default:
		relationships = int32(relCount)
	}

	return &pb.ExtractResponse{
		Memories:             pbMemories,
		RelationshipsCreated: relationships,
	}, nil
}

// GetProfile builds a user/project profile by splitting all project memories into
// two layers:
//   - Static profile: semantic facts and procedural knowledge that are stable over time.
//   - Dynamic profile: episodic memories inside the recency window, defaulting
//     to the last 7 days, representing recent activity.
//
// Each fact carries the memory's current decay score as a confidence indicator.
// Profile sizing. Both were hardcoded, which made "recent" a claim this engine
// imposed on every caller: a support bot and a coding assistant do not agree
// on how long ago something stops being current.
//
// The clamps follow the contract top_k settled on -- a request above the
// maximum is served at the maximum rather than refused -- because a profile is
// a convenience view and failing one over an ambitious number helps nobody.
// The maxima exist because both bound real work: the memory budget is a graph
// query's LIMIT, and a 365-day window with a large budget is the widest profile
// anyone has asked for.
const (
	defaultProfileMemories = 200
	maxProfileMemories     = 1000

	defaultProfileRecencyDays = 7
	maxProfileRecencyDays     = 365
)

// profileMemoryBudget resolves how many memories a profile is built from.
func profileMemoryBudget(requested int32) int32 {
	switch {
	case requested <= 0:
		return defaultProfileMemories
	case requested > maxProfileMemories:
		return maxProfileMemories
	default:
		return requested
	}
}

// profileRecencyDays resolves the window that separates current from settled.
func profileRecencyDays(requested int32) int {
	switch {
	case requested <= 0:
		return defaultProfileRecencyDays
	case requested > maxProfileRecencyDays:
		return maxProfileRecencyDays
	default:
		return int(requested)
	}
}

func (s *MemoryService) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.GetProfileResponse, error) {
	if req.ProjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}

	filter := graph.QueryFilter{
		ProjectID: req.ProjectId,
		TopK:      profileMemoryBudget(req.MaxMemories),
	}
	results, err := s.repo.QueryMemories(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query failed: %v", err)
	}

	now := time.Now().UTC()
	dynamicCutoff := now.AddDate(0, 0, -profileRecencyDays(req.RecencyDays))

	var staticFacts []*pb.ProfileFact
	var dynamicFacts []*pb.ProfileFact

	for _, r := range results {
		t := r.Memory.Type
		fact := &pb.ProfileFact{
			Content:    r.Memory.Content,
			Type:       memoryTypeToProto(t),
			Confidence: r.Memory.DecayScore,
			Tags:       r.Memory.Tags,
		}

		switch t {
		case model.MemoryTypeSemantic, model.MemoryTypeProcedural:
			// Static profile: facts and procedures (stable knowledge).
			staticFacts = append(staticFacts, fact)
		case model.MemoryTypeEpisodic:
			// Dynamic profile: only recent episodes.
			if r.Memory.CreatedAt.After(dynamicCutoff) {
				dynamicFacts = append(dynamicFacts, fact)
			}
		}
	}

	return &pb.GetProfileResponse{
		StaticProfile:  staticFacts,
		DynamicProfile: dynamicFacts,
		TotalMemories:  int64(len(results)),
	}, nil
}

// --- Converters ---
// The functions below translate between internal domain models and their protobuf
// wire representations. They are used by every gRPC handler in this package.

// memoryToProto converts an internal Memory model to its protobuf representation.
func memoryToProto(m model.Memory) *pb.Memory {
	return &pb.Memory{
		Id:          m.ID.String(),
		Content:     m.Content,
		Type:        memoryTypeToProto(m.Type),
		ProjectId:   m.ProjectID,
		Tags:        m.Tags,
		CreatedAt:   timestamppb.New(m.CreatedAt),
		AccessCount: m.AccessCount,
		DecayScore:  m.DecayScore,
	}
}

// memoryWithContextToProto converts a MemoryWithContext to its protobuf
// representation, including its context edges.
func memoryWithContextToProto(m model.MemoryWithContext) *pb.MemoryWithContext {
	var pbContext []*pb.ContextEdge
	for _, e := range m.Context {
		pbContext = append(pbContext, &pb.ContextEdge{
			Relationship:  relTypeToProto(e.Relationship),
			TargetId:      e.TargetID.String(),
			TargetContent: e.TargetContent,
			Weight:        e.Weight,
		})
	}
	return &pb.MemoryWithContext{
		Memory:  memoryToProto(m.Memory),
		Context: pbContext,
		Score:   m.Score,
	}
}

// edgeToProto converts an internal Edge model to its protobuf representation.
func edgeToProto(e model.Edge) *pb.Edge {
	return &pb.Edge{
		Id:           e.ID.String(),
		FromId:       e.FromID.String(),
		ToId:         e.ToID.String(),
		Relationship: relTypeToProto(e.Relationship),
		Weight:       e.Weight,
		CreatedAt:    timestamppb.New(e.CreatedAt),
	}
}

// memoryTypeToProto maps an internal MemoryType to its protobuf enum value.
func memoryTypeToProto(t model.MemoryType) pb.MemoryType {
	switch t {
	case model.MemoryTypeEpisodic:
		return pb.MemoryType_MEMORY_TYPE_EPISODIC
	case model.MemoryTypeSemantic:
		return pb.MemoryType_MEMORY_TYPE_SEMANTIC
	case model.MemoryTypeProcedural:
		return pb.MemoryType_MEMORY_TYPE_PROCEDURAL
	default:
		return pb.MemoryType_MEMORY_TYPE_UNSPECIFIED
	}
}

// protoToMemoryType maps a protobuf MemoryType enum to the internal model type.
// Returns an error for unrecognized values.
func protoToMemoryType(t pb.MemoryType) (model.MemoryType, error) {
	switch t {
	case pb.MemoryType_MEMORY_TYPE_EPISODIC:
		return model.MemoryTypeEpisodic, nil
	case pb.MemoryType_MEMORY_TYPE_SEMANTIC:
		return model.MemoryTypeSemantic, nil
	case pb.MemoryType_MEMORY_TYPE_PROCEDURAL:
		return model.MemoryTypeProcedural, nil
	default:
		return "", fmt.Errorf("unknown memory type: %v", t)
	}
}

// relTypeToProto maps an internal RelationshipType to its protobuf enum value.
func relTypeToProto(r model.RelationshipType) pb.RelationshipType {
	switch r {
	case model.RelRelatesTo:
		return pb.RelationshipType_RELATIONSHIP_TYPE_RELATES_TO
	case model.RelSupersedes:
		return pb.RelationshipType_RELATIONSHIP_TYPE_SUPERSEDES
	case model.RelCausedBy:
		return pb.RelationshipType_RELATIONSHIP_TYPE_CAUSED_BY
	default:
		return pb.RelationshipType_RELATIONSHIP_TYPE_UNSPECIFIED
	}
}

// protoToRelType maps a protobuf RelationshipType enum to the internal model type.
// Returns an error for unrecognized values.
func protoToRelType(r pb.RelationshipType) (model.RelationshipType, error) {
	switch r {
	case pb.RelationshipType_RELATIONSHIP_TYPE_RELATES_TO:
		return model.RelRelatesTo, nil
	case pb.RelationshipType_RELATIONSHIP_TYPE_SUPERSEDES:
		return model.RelSupersedes, nil
	case pb.RelationshipType_RELATIONSHIP_TYPE_CAUSED_BY:
		return model.RelCausedBy, nil
	default:
		return "", fmt.Errorf("unknown relationship type: %v", r)
	}
}
