// Package service implements the gRPC service layer for Context0, the memory graph
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
	"context"
	"fmt"
	"strings"
	"time"

	pb "github.com/context0/context0/api/gen/context0/v1"
	"github.com/context0/context0/internal/embedding"
	"github.com/context0/context0/internal/extraction"
	"github.com/context0/context0/internal/graph"
	"github.com/context0/context0/internal/metrics"
	"github.com/context0/context0/internal/ranking"
	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MemoryService implements the Context0 gRPC service. It holds a graph repository
// for persistent memory storage and traversal, and an optional embedder for
// generating vector representations used in hybrid search.
type MemoryService struct {
	pb.UnimplementedContext0Server
	repo     graph.Repository
	embedder embedding.Embedder
}

// NewMemoryService creates a new MemoryService with the given graph repository
// and embedder. The embedder may be nil, in which case vector search is disabled
// and the service operates in graph-only mode.
func NewMemoryService(repo graph.Repository, embedder embedding.Embedder) *MemoryService {
	return &MemoryService{repo: repo, embedder: embedder}
}

// Store persists a new memory node into the graph. The full pipeline is:
//  1. Validate input (content and project_id are required).
//  2. Create the memory node in the graph repository.
//  3. Generate and store an embedding vector for future similarity search.
//  4. Link the memory to a session if session_id is provided.
//  5. Detect contradictions with existing semantic memories and create supersedes edges.
//  6. Auto-link to other memories that share overlapping tags via relates_to edges.
func (s *MemoryService) Store(ctx context.Context, req *StoreRequest) (*StoreResponse, error) {
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

	if err := s.repo.CreateMemory(ctx, mem); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create memory: %v", err)
	}

	// Generate and store embedding for vector search.
	if s.embedder != nil {
		if vec, err := s.embedder.Embed(mem.Content); err == nil {
			_ = s.repo.StoreEmbedding(ctx, mem.ID, vec)
		}
	}

	// Link to session if provided.
	if req.SessionId != "" {
		sessID, err := uuid.Parse(req.SessionId)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
		}
		if err := s.repo.LinkMemoryToSession(ctx, sessID, mem.ID); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to link memory to session: %v", err)
		}
	}

	// Detect contradictions with existing memories and create supersedes edges.
	s.detectAndSupersede(ctx, mem)

	// Auto-link by tags: find existing memories with overlapping tags and create relates_to edges.
	if len(req.Tags) > 0 {
		s.autoLinkByTags(ctx, mem)
	}

	metrics.MemoriesTotal.WithLabelValues(string(memType)).Inc()

	return &StoreResponse{
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
func (s *MemoryService) Query(ctx context.Context, req *QueryRequest) (*QueryResponse, error) {
	timer := prometheus.NewTimer(metrics.QueryDuration)
	defer timer.ObserveDuration()

	// project_id is optional — if empty, query across all projects.

	var types []model.MemoryType
	for _, t := range req.Types {
		mt, err := protoToMemoryType(t)
		if err != nil {
			continue
		}
		types = append(types, mt)
	}

	// Parse query into structured form with time filtering.
	parsed := ParseQuery(req.Query, req.ProjectId, types, req.MaxDepth, req.TopK)
	filter := parsed.ToGraphFilter()

	// --- Hybrid retrieval: graph + vector ---
	graphResults, err := s.repo.QueryMemories(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "graph query failed: %v", err)
	}

	var vectorResults []model.MemoryWithContext
	if s.embedder != nil && req.Query != "" {
		if queryVec, err := s.embedder.Embed(req.Query); err == nil {
			vectorResults, _ = s.repo.SearchByVector(ctx, queryVec, req.ProjectId, int(parsed.TopK)*2)
		}
	}

	// Merge: deduplicate by ID, boost memories found by both methods.
	results := mergeResults(graphResults, vectorResults)

	// Rank results using scoring function.
	results = ranking.RankResults(results, ranking.DefaultWeights(), int(parsed.TopK))

	// Increment access counts for returned results.
	for _, r := range results {
		_ = s.repo.IncrementAccessCount(ctx, r.Memory.ID)
	}

	metrics.QueryResultsCount.Observe(float64(len(results)))

	var pbResults []*pb.MemoryWithContext
	for _, r := range results {
		pbResults = append(pbResults, memoryWithContextToProto(r))
	}

	return &QueryResponse{Results: pbResults}, nil
}

// Connect creates a directed edge between two existing memory nodes. Both nodes
// must already exist in the graph. If no weight is specified, the edge defaults
// to a weight of 1.0. The edge relationship type must be a valid RelationshipType.
func (s *MemoryService) Connect(ctx context.Context, req *ConnectRequest) (*ConnectResponse, error) {
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

	if err := s.repo.CreateEdge(ctx, edge); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create edge: %v", err)
	}

	metrics.EdgesTotal.WithLabelValues(string(relType)).Inc()

	return &ConnectResponse{
		Edge: edgeToProto(edge),
	}, nil
}

// Delete removes a memory node and its associated edges from the graph.
func (s *MemoryService) Delete(ctx context.Context, req *DeleteRequest) (*DeleteResponse, error) {
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

	return &DeleteResponse{}, nil
}

// GetGraph returns a subgraph centered on a given memory node, expanding outward
// to the specified depth. This is used for graph visualization and context exploration.
func (s *MemoryService) GetGraph(ctx context.Context, req *GetGraphRequest) (*GetGraphResponse, error) {
	if req.CenterId == "" {
		return nil, status.Error(codes.InvalidArgument, "center_id is required")
	}

	centerID, err := uuid.Parse(req.CenterId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid center_id: %v", err)
	}

	nodes, edges, err := s.repo.GetSubgraph(ctx, centerID, req.Depth)
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

	return &GetGraphResponse{
		Nodes: pbNodes,
		Edges: pbEdges,
	}, nil
}

// Extract processes a raw conversation string and automatically extracts structured
// memories from it. The extraction is delegated to the extraction package, which
// supports both LLM-based and rule-based strategies. Each extracted memory is
// persisted, embedded, optionally linked to a session, and auto-linked by tags.
func (s *MemoryService) Extract(ctx context.Context, req *pb.ExtractRequest) (*pb.ExtractResponse, error) {
	if req.Conversation == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation is required")
	}
	if req.ProjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}

	extractor := extraction.NewExtractor(nil) // TODO: use configured LLM provider
	extracted, err := extractor.Extract(ctx, req.Conversation, req.ProjectId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "extraction failed: %v", err)
	}

	memories := extraction.ToMemories(extracted, req.ProjectId)
	var pbMemories []*pb.Memory
	relCount := int32(0)

	for _, mem := range memories {
		if err := s.repo.CreateMemory(ctx, mem); err != nil {
			continue
		}

		// Generate and store embedding.
		if s.embedder != nil {
			if vec, err := s.embedder.Embed(mem.Content); err == nil {
				_ = s.repo.StoreEmbedding(ctx, mem.ID, vec)
			}
		}

		// Link to session if provided.
		if req.SessionId != "" {
			sessID, err := uuid.Parse(req.SessionId)
			if err == nil {
				_ = s.repo.LinkMemoryToSession(ctx, sessID, mem.ID)
			}
		}

		// Auto-link by tags.
		s.autoLinkByTags(ctx, mem)
		relCount++ // approximate — autoLinkByTags may create multiple edges

		metrics.MemoriesTotal.WithLabelValues(string(mem.Type)).Inc()
		pbMemories = append(pbMemories, memoryToProto(mem))
	}

	return &pb.ExtractResponse{
		Memories:             pbMemories,
		RelationshipsCreated: relCount,
	}, nil
}

// GetProfile builds a user/project profile by splitting all project memories into
// two layers:
//   - Static profile: semantic facts and procedural knowledge that are stable over time.
//   - Dynamic profile: episodic memories from the last 7 days representing recent activity.
//
// Each fact carries the memory's current decay score as a confidence indicator.
func (s *MemoryService) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.GetProfileResponse, error) {
	if req.ProjectId == "" {
		return nil, status.Error(codes.InvalidArgument, "project_id is required")
	}

	// Fetch all memories for this project.
	filter := graph.QueryFilter{
		ProjectID: req.ProjectId,
		TopK:      200,
	}
	results, err := s.repo.QueryMemories(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query failed: %v", err)
	}

	now := time.Now().UTC()
	dynamicCutoff := now.AddDate(0, 0, -7) // last 7 days

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

// detectAndSupersede checks whether a newly stored memory contradicts any existing
// semantic memories in the same project. For each contradiction detected with
// confidence >= 0.5, a supersedes edge is created from the new memory to the old one,
// signaling that the new memory replaces the old fact. Only semantic memories are
// checked because episodic and procedural memories do not contradict each other.
func (s *MemoryService) detectAndSupersede(ctx context.Context, mem model.Memory) {
	if mem.Type != model.MemoryTypeSemantic {
		return
	}

	filter := graph.QueryFilter{
		ProjectID: mem.ProjectID,
		Types:     []model.MemoryType{model.MemoryTypeSemantic},
		TopK:      50,
	}

	results, err := s.repo.QueryMemories(ctx, filter)
	if err != nil {
		return
	}

	var existingMems []model.Memory
	for _, r := range results {
		existingMems = append(existingMems, r.Memory)
	}

	contradictions := extraction.DetectContradictions(mem, existingMems)

	for _, c := range contradictions {
		if c.Confidence < 0.5 {
			continue // skip low-confidence contradictions
		}

		edge := model.Edge{
			ID:           uuid.New(),
			FromID:       c.NewMemory.ID,
			ToID:         c.OldMemory.ID,
			Relationship: model.RelSupersedes,
			Weight:       c.Confidence,
			CreatedAt:    time.Now().UTC(),
		}
		_ = s.repo.CreateEdge(ctx, edge)
		metrics.EdgesTotal.WithLabelValues(string(model.RelSupersedes)).Inc()
	}
}

// autoLinkByTags finds existing memories in the same project that share at least
// one tag with the given memory and creates relates_to edges with a default weight
// of 0.5 to connect them. This builds implicit topic clusters in the graph.
func (s *MemoryService) autoLinkByTags(ctx context.Context, mem model.Memory) {
	filter := graph.QueryFilter{
		ProjectID: mem.ProjectID,
		Tags:      mem.Tags,
		TopK:      10,
	}

	existing, err := s.repo.QueryMemories(ctx, filter)
	if err != nil {
		return
	}

	for _, e := range existing {
		if e.Memory.ID == mem.ID {
			continue
		}
		if hasOverlappingTags(mem.Tags, e.Memory.Tags) {
			edge := model.Edge{
				ID:           uuid.New(),
				FromID:       mem.ID,
				ToID:         e.Memory.ID,
				Relationship: model.RelRelatesTo,
				Weight:       0.5,
				CreatedAt:    time.Now().UTC(),
			}
			_ = s.repo.CreateEdge(ctx, edge)
		}
	}
}

// --- Type aliases for generated proto types ---
// These aliases allow the service layer to reference proto request/response types
// without requiring callers to import the generated proto package directly.

type StoreRequest = pb.StoreRequest
type StoreResponse = pb.StoreResponse
type QueryRequest = pb.QueryRequest
type QueryResponse = pb.QueryResponse
type ConnectRequest = pb.ConnectRequest
type ConnectResponse = pb.ConnectResponse
type DeleteRequest = pb.DeleteRequest
type DeleteResponse = pb.DeleteResponse
type GetGraphRequest = pb.GetGraphRequest
type GetGraphResponse = pb.GetGraphResponse

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

// memoryWithContextToProto converts a MemoryWithContext (memory plus its neighboring
// edges and their target content) to the protobuf representation.
func memoryWithContextToProto(m model.MemoryWithContext) *pb.MemoryWithContext {
	var ctxEdges []*pb.ContextEdge
	for _, c := range m.Context {
		ctxEdges = append(ctxEdges, &pb.ContextEdge{
			Relationship:  relTypeToProto(c.Relationship),
			TargetId:      c.TargetID.String(),
			TargetContent: c.TargetContent,
			Weight:        c.Weight,
		})
	}
	return &pb.MemoryWithContext{
		Memory:  memoryToProto(m.Memory),
		Context: ctxEdges,
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

// extractKeywords splits a query string into keywords for tag/content matching.
func extractKeywords(query string) []string {
	if query == "" {
		return nil
	}
	// Simple tokenization: split on spaces, filter short words.
	words := strings.Fields(strings.ToLower(query))
	var keywords []string
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true,
		"was": true, "were": true, "do": true, "does": true, "did": true,
		"what": true, "which": true, "who": true, "how": true, "when": true,
		"where": true, "this": true, "that": true, "it": true, "of": true,
		"in": true, "on": true, "for": true, "to": true, "with": true,
		"my": true, "our": true, "we": true, "i": true, "use": true,
		"project": true,
	}
	for _, w := range words {
		if len(w) < 2 {
			continue
		}
		if stopWords[w] {
			continue
		}
		keywords = append(keywords, w)
	}
	return keywords
}

// mergeResults combines graph-retrieved and vector-retrieved results into a single
// deduplicated slice. When a memory appears in both result sets, its score is
// boosted by 50% of the vector score, rewarding memories that are relevant across
// both retrieval strategies.
func mergeResults(graph, vector []model.MemoryWithContext) []model.MemoryWithContext {
	seen := make(map[uuid.UUID]*model.MemoryWithContext)

	// Add all graph results.
	for i := range graph {
		id := graph[i].Memory.ID
		r := graph[i]
		seen[id] = &r
	}

	// Merge vector results — boost score if already found by graph.
	for i := range vector {
		id := vector[i].Memory.ID
		if existing, ok := seen[id]; ok {
			// Found by both methods — boost by 50%.
			existing.Score += vector[i].Score * 0.5
		} else {
			r := vector[i]
			seen[id] = &r
		}
	}

	results := make([]model.MemoryWithContext, 0, len(seen))
	for _, r := range seen {
		results = append(results, *r)
	}
	return results
}

// hasOverlappingTags returns true if the two tag slices share at least one tag
// (compared case-insensitively).
func hasOverlappingTags(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[strings.ToLower(t)] = true
	}
	for _, t := range b {
		if set[strings.ToLower(t)] {
			return true
		}
	}
	return false
}
