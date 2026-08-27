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
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/extraction"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/internal/metrics"
	"github.com/NarayanaSabari/Kora/internal/ranking"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MemoryService implements the Kora gRPC service. It holds a graph repository
// for persistent memory storage and traversal, and an optional embedder for
// generating vector representations used in hybrid search.
type MemoryService struct {
	pb.UnimplementedKoraServer
	repo     *graph.AGERepository
	embedder embedding.Embedder
}

// NewMemoryService creates a new MemoryService with the given graph repository
// and embedder. The embedder may be nil, in which case vector search is disabled
// and the service operates in graph-only mode.
func NewMemoryService(repo *graph.AGERepository, embedder embedding.Embedder) *MemoryService {
	return &MemoryService{repo: repo, embedder: embedder}
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

	if err := s.repo.CreateMemory(ctx, mem); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create memory: %v", err)
	}

	// From here the memory is committed, so finishing its record belongs to
	// this write rather than to the caller.
	//
	// Running the remaining steps on the caller's context meant a client that
	// hung up after CreateMemory left the memory stored with no embedding row:
	// permanently absent from vector search, while Store still returned
	// success, so nothing would ever retry it. Reproduced by cancelling at a
	// range of delays -- every memory that landed had zero embedding rows,
	// against exactly one for an uncancelled write.
	//
	// The deadline is the write's own. Dropping cancellation without one would
	// let a stalled provider or database hold the request open indefinitely.
	ctx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), storeFinishTimeout)
	defer cancelFinish()

	// Generate and store embedding for vector search.
	//
	// A failure here is not fatal to the write -- the memory is already stored
	// and remains findable by keyword -- but it silently removes that memory
	// from vector search forever, so it must be visible rather than dropped.
	//
	// Kept in scope below, where auto-linking reuses it to find semantically
	// related memories rather than embedding the same content a second time.
	var vec []float32
	if s.embedder != nil {
		embedded, err := s.embedder.Embed(mem.Content)
		switch {
		case err != nil:
			logging.FromContext(ctx).Error("embedding failed; memory will not be vector-searchable",
				slog.String("memory_id", mem.ID.String()),
				slog.String("project_id", mem.ProjectID),
				slog.Any("error", err))
		default:
			vec = embedded
			if err := s.repo.StoreEmbedding(ctx, mem.ID, mem.ProjectID, vec); err != nil {
				logging.FromContext(ctx).Error("storing embedding failed; memory will not be vector-searchable",
					slog.String("memory_id", mem.ID.String()),
					slog.String("project_id", mem.ProjectID),
					slog.Any("error", err))
			}
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
	superseded := s.detectAndSupersede(ctx, mem)

	// Auto-link: connect this memory to others sharing a tag or close to it in
	// meaning.
	//
	// No longer gated on len(req.Tags). That gate made sense when tag overlap
	// was the only source of relatedness, but it now skips the semantic pass
	// too, so an untagged memory -- which is most of them -- would never link.
	s.autoLinkByTags(ctx, mem, superseded, vec)

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
	filter := ParseQuery(req.Query, req.ProjectId, types, req.TopK)

	// --- Hybrid retrieval: graph + vector ---
	graphResults, err := s.repo.QueryMemories(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "graph query failed: %v", err)
	}

	// The graph retriever matches keywords with a boolean CONTAINS, so grade
	// each hit lexically here to recover a comparable relevance signal.
	for i := range graphResults {
		graphResults[i].Relevance = ranking.LexicalRelevance(
			graphResults[i].Memory.Content,
			graphResults[i].Memory.Tags,
			filter.Keywords,
		)
	}

	var vectorResults []model.MemoryWithContext
	if s.embedder != nil && req.Query != "" {
		if queryVec, err := s.embedder.Embed(req.Query); err == nil {
			var verr error
			// The vector retriever gets the same candidate pool size as the
			// graph one, for the same reason: this LIMIT runs before ranking,
			// so it decides what ranking is allowed to consider.
			//
			// It was topK*2, which is far too tight to act as the safety net
			// for the keyword retriever. The two retrievers exist to cover each
			// other -- a paraphrased query that shares no keyword with the
			// answer is exactly what vector search is for -- and a pool of 20
			// cannot do that in a project holding thousands of memories.
			vectorResults, verr = s.repo.SearchByVector(ctx, queryVec, req.ProjectId, vectorCandidatePool(filter.TopK))
			if verr != nil {
				// The query still returns keyword results, so the caller sees a
				// quietly worse answer rather than an error. Record it.
				logging.FromContext(ctx).Warn("vector search failed; falling back to keyword results only",
					slog.String("project_id", req.ProjectId),
					slog.Any("error", verr))
			}
		}
	}

	// Merge: deduplicate by ID, boosting memories both retrievers agree on.
	// Keywords are passed so the merge can tell a candidate that lexically
	// matched from one the vector retriever surfaced on similarity alone; the
	// two scores are otherwise not comparable. See ranking.RelevanceTier.
	results := mergeResults(graphResults, vectorResults, filter.Keywords)

	// Rank results using scoring function. This consumes the Relevance set
	// above, so retrieval quality drives the final order.
	results = ranking.RankResults(results, int(filter.TopK))

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

	extracted := extraction.Extract(req.Conversation)
	memories := extraction.ToMemories(extracted, req.ProjectId)
	var pbMemories []*pb.Memory
	relCount := int32(0)

	// Detached from the caller for the same reason as Store: each memory here
	// is committed and then embedded, so a client that hangs up mid-loop would
	// leave the memories already written permanently absent from vector search
	// while the response still counts them as extracted. Bounded, so a stalled
	// dependency cannot hold the request open.
	ctx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), storeFinishTimeout)
	defer cancelFinish()

	// Embeddings are computed up front and in parallel. A cloud embedder is a
	// network round trip per memory (~0.5s against Google's API), and a single
	// conversation extracts dozens: done serially inside the loop below, a
	// 50-turn transcript took 30s and a full benchmark ingest ran into hours.
	// The Embedder interface requires implementations to be safe for
	// concurrent use, so the only cost here is bounded fan-out.
	vectors := s.embedExtracted(ctx, memories)

	// Parsed once rather than per memory: a malformed id is a property of the
	// request, and logging it inside the loop would emit one warning for every
	// extracted memory in the conversation.
	//
	// Store rejects a bad session_id outright. Extract only warns, because
	// failing here would discard an entire conversation over one bad field,
	// but it must not stay silent: dropping the error meant a typo unlinked
	// every extracted memory while the response still reported success.
	var sessionID uuid.UUID
	haveSession := false
	if req.SessionId != "" {
		parsed, err := uuid.Parse(req.SessionId)
		if err != nil {
			logging.FromContext(ctx).Warn("session_id is not a valid UUID; extracted memories will not be linked to a session",
				slog.String("session_id", req.SessionId),
				slog.Any("error", err))
		} else {
			sessionID, haveSession = parsed, true
		}
	}

	for _, mem := range memories {
		if err := s.repo.CreateMemory(ctx, mem); err != nil {
			// Extraction is best-effort per memory: one bad memory must not
			// discard the rest of the conversation. But the caller is told how
			// many were extracted, not how many were dropped, so a silent skip
			// here reads as "the extractor found nothing".
			logging.FromContext(ctx).Error("extracted memory could not be stored; it is missing from the response count",
				slog.String("project_id", req.ProjectId),
				slog.Any("error", err))
			continue
		}

		// Generate and store embedding.
		if s.embedder != nil {
			if vec := vectors[mem.ID]; vec == nil {
				// The failure was already logged by embedExtracted, which has
				// the underlying error; repeating it here would double-count.
			} else if err := s.repo.StoreEmbedding(ctx, mem.ID, mem.ProjectID, vec); err != nil {
				logging.FromContext(ctx).Error("storing embedding failed for extracted memory; it will not be vector-searchable",
					slog.String("memory_id", mem.ID.String()),
					slog.Any("error", err))
			}
		}

		// Link to session if provided.
		if haveSession {
			if err := s.repo.LinkMemoryToSession(ctx, sessionID, mem.ID); err != nil {
				logging.FromContext(ctx).Warn("linking extracted memory to session failed",
					slog.String("memory_id", mem.ID.String()),
					slog.String("session_id", req.SessionId),
					slog.Any("error", err))
			}
		}

		// Auto-link by tags and semantic similarity.
		relCount += int32(s.autoLinkByTags(ctx, mem, nil, vectors[mem.ID]))

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
func (s *MemoryService) detectAndSupersede(ctx context.Context, mem model.Memory) map[uuid.UUID]bool {
	if mem.Type != model.MemoryTypeSemantic {
		return nil
	}

	filter := graph.QueryFilter{
		ProjectID: mem.ProjectID,
		Types:     []model.MemoryType{model.MemoryTypeSemantic},
		TopK:      contradictionCandidates,
	}

	results, err := s.repo.QueryMemories(ctx, filter)
	if err != nil {
		logging.FromContext(ctx).Warn("contradiction detection skipped: candidate lookup failed",
			slog.String("memory_id", mem.ID.String()),
			slog.Any("error", err))
		return nil
	}

	var existingMems []model.Memory
	for _, r := range results {
		existingMems = append(existingMems, r.Memory)
	}

	contradictions := extraction.DetectContradictions(mem, existingMems)

	// Collected and written in one statement: a project with many similar
	// memories can produce dozens of contradictions, and a round trip each
	// would land entirely on the caller's Store latency.
	//
	// Only the highest-confidence contradictions are recorded. Without a cap,
	// a project of near-identical semantic memories links each new write to
	// most of its candidates, so edges grow with writes x candidates -- 70k
	// supersedes edges over ~6k memories in a soak run -- and every later
	// traversal over those nodes pays for it. Superseding the few most likely
	// matches carries essentially all the signal.
	var edges []model.Edge
	for _, c := range contradictions {
		if c.Confidence < 0.5 {
			continue // skip low-confidence contradictions
		}

		edges = append(edges, model.Edge{
			ID:           uuid.New(),
			FromID:       c.NewMemory.ID,
			ToID:         c.OldMemory.ID,
			Relationship: model.RelSupersedes,
			Weight:       c.Confidence,
			CreatedAt:    time.Now().UTC(),
		})
	}

	sort.SliceStable(edges, func(i, j int) bool { return edges[i].Weight > edges[j].Weight })
	if len(edges) > maxSupersedesPerStore {
		edges = edges[:maxSupersedesPerStore]
	}

	if err := s.repo.CreateEdges(ctx, edges); err != nil {
		logging.FromContext(ctx).Warn("writing supersedes edges failed; superseded facts stay live",
			slog.String("memory_id", mem.ID.String()),
			slog.Int("edge_count", len(edges)),
			slog.Any("error", err))
		return nil
	}
	metrics.EdgesTotal.WithLabelValues(string(model.RelSupersedes)).Add(float64(len(edges)))

	// Reported so tag auto-linking can skip pairs that already have a more
	// specific relationship, without re-reading them from the graph.
	linked := make(map[uuid.UUID]bool, len(edges))
	for _, e := range edges {
		linked[e.ToID] = true
	}
	return linked
}

// storeFinishTimeout bounds the work that follows a committed memory:
// embedding, session linking, contradiction detection and tag auto-linking.
//
// These run detached from the caller's context, because the memory is already
// stored and leaving its record half-finished is worse than a slow request.
// The bound is what stops a stalled provider or database holding that work --
// and the goroutine serving it -- open forever.
const storeFinishTimeout = 30 * time.Second

// maxSupersedesPerStore caps how many supersedes edges a single write may
// create. Contradiction detection is a heuristic over text overlap, so a
// project of similar memories can flag most candidates at once; recording all
// of them inflates the graph without adding information.
const maxSupersedesPerStore = 5

// maxRelatesPerStore caps how many relates_to edges tag auto-linking may create
// per write.
const maxRelatesPerStore = 5

// contradictionCandidates bounds how many existing semantic memories a new one
// is checked against for contradictions. Same reasoning as autoLinkCandidates:
// every detection becomes a supersedes edge.
const contradictionCandidates = 50

// autoLinkCandidates bounds how many recent memories a new one is compared
// against for tag overlap.
//
// This is deliberately small. Every match becomes an edge, so the graph grows
// with the product of writes and candidates, and each edge makes later
// traversals over the same node more expensive. Measured under 8-way
// concurrency at the time the cap was introduced, a tagged write cost ~469ms
// against a 100-candidate pool versus ~6ms for an untagged one.
//
// That 469ms figure is what the cap prevents, not what the code does now: with
// the cap in place, and after the query fixes in a661212, a tagged write into a
// 94k-vertex graph measures ~38ms serially. Both numbers are kept because the
// second alone would make this constant look unnecessary.
const autoLinkCandidates = 10

// relatesTagWeight is the edge weight recorded when two memories share a tag.
// A tag is a deliberate label rather than an inferred similarity, so it is
// treated as solid but unquantified evidence: strong enough to beat a marginal
// embedding match, below a high-confidence one.
const relatesTagWeight = 0.5

// relatedStdDevs is how far above the mean a candidate's similarity must sit,
// in standard deviations of the candidate set, to count as related.
//
// Neither an absolute nor a proportional threshold works here, because
// similarity scales are a property of the embedder rather than of the data.
// Measured on the same four sentences: bag-of-words scores a related pair at
// 0.55 and unrelated ones at 0.09, while gemini-embedding-2 scores the same
// related pair at 0.88 and the unrelated ones at 0.64-0.66, because a dense
// model places everything one person says in roughly the same region. An
// absolute cut tuned for one embedder is a dense graph or an empty one for the
// other; a proportional cut fails too, since the unrelated-to-related ratio is
// 0.16 for bag-of-words and 0.74 for Gemini. The engine supports four
// providers and operators can point it at any OpenAI-compatible endpoint, so
// no fixed constant can be right.
//
// Dispersion is what both distributions agree on: whatever the scale, a
// genuinely related memory stands out from that project's own spread of
// similarities. One standard deviation above the mean separates the two cases
// cleanly (bag-of-words cuts at 0.55, Gemini at 0.81) and needs no tuning when
// the embedding provider changes.
const relatedStdDevs = 1.0

// minRelatedCandidates is the number of candidates below which the dispersion
// test cannot be applied: a mean and standard deviation over one or two
// samples describe nothing.
//
// Below it, similarity is compared against the whole candidate set's mean
// instead, which is weaker evidence but still refuses to link a memory that is
// no closer than average. Skipping the link entirely would make the graph
// depend on write order -- the first memories written into a project would
// stay unlinked forever, and within a single Extract call the earliest
// utterances are exactly the ones later ones should attach to.
const minRelatedCandidates = 3

// embedExtractedConcurrency bounds the fan-out in embedExtracted. Cloud
// embedding APIs rate-limit per key, so a 200-memory conversation must not
// open 200 simultaneous requests: the burst gets throttled and ends up slower
// than the serial version it replaced.
const embedExtractedConcurrency = 8

// embedExtracted computes embeddings for every extracted memory in parallel and
// returns them keyed by memory ID. Memories whose embedding failed are absent
// from the map rather than present with a nil value, so the caller can tell
// "failed" from "no embedder configured".
//
// Failures are logged here and not returned: extraction is best-effort per
// memory, and one embedding failure must not discard a whole conversation. The
// memory is still stored, it is simply not vector-searchable.
func (s *MemoryService) embedExtracted(ctx context.Context, memories []model.Memory) map[uuid.UUID][]float32 {
	if s.embedder == nil || len(memories) == 0 {
		return nil
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		vectors = make(map[uuid.UUID][]float32, len(memories))
		sem     = make(chan struct{}, embedExtractedConcurrency)
	)

	for _, mem := range memories {
		wg.Add(1)
		go func(mem model.Memory) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			vec, err := s.embedder.Embed(mem.Content)
			if err != nil {
				logging.FromContext(ctx).Error("embedding failed for extracted memory; it will not be vector-searchable",
					slog.String("memory_id", mem.ID.String()),
					slog.Any("error", err))
				return
			}

			mu.Lock()
			vectors[mem.ID] = vec
			mu.Unlock()
		}(mem)
	}

	wg.Wait()
	return vectors
}

// autoLinkByTags finds existing memories in the same project that share at least
// one tag with the given memory, or whose meaning is close to it, and creates
// relates_to edges to connect them. This builds implicit topic clusters in the
// graph. Memories already connected to mem by some other edge (e.g. a
// supersedes edge inferred by detectAndSupersede) are skipped: a generic
// relates_to edge adds nothing once a more specific relationship has already
// been recorded between the same pair.
//
// Relatedness has two sources, and the edge weight records which one fired:
//
//   - Shared tags, weight 0.5. A tag is a deliberate label, so an overlap is
//     the strongest cheap evidence that two memories belong together.
//   - Embedding similarity above relatedThreshold, weight = the similarity.
//     This is what makes the graph work outside the extractor's vocabulary.
//
// Tag overlap alone used to be the only source, and extractTopics only emits
// tags for roughly forty hardcoded technical terms. Every conversation outside
// that vocabulary therefore produced untagged memories that could never share
// a tag, and so never linked: a LoCoMo benchmark corpus of 6,760 memories held
// exactly one edge, with 97% of memories untagged. A graph-first engine whose
// graph only forms for conversations about Kubernetes is not graph-first, so
// relatedness now also comes from the embeddings the engine already computes.
//
// Returns the number of edges actually written, so callers can report a real
// relationship count rather than assuming the call did something.
func (s *MemoryService) autoLinkByTags(ctx context.Context, mem model.Memory, connected map[uuid.UUID]bool, vector []float32) int {
	// Which memories this one is already connected to, so a generic relates_to
	// edge is not added on top of a more specific relationship.
	//
	// Only the supersedes edges just written by detectAndSupersede can exist at
	// this point -- the memory was created moments ago -- so they are passed in
	// rather than re-read. The previous round trip to GetContextEdges was a
	// significant share of a tagged write's latency, and it queried a node whose
	// edges this process had itself just created.
	//
	// candidates is keyed by ID so the tag and vector passes cannot both add an
	// edge for the same pair.
	type candidate struct {
		id     uuid.UUID
		weight float64
	}
	seen := make(map[uuid.UUID]float64)

	skip := func(id uuid.UUID) bool {
		return id == mem.ID || connected[id]
	}

	// Pass 1: shared tags among recent memories.
	filter := graph.QueryFilter{
		ProjectID: mem.ProjectID,
		TopK:      autoLinkCandidates,
	}
	if existing, err := s.repo.QueryMemories(ctx, filter); err == nil {
		for _, e := range existing {
			if skip(e.Memory.ID) {
				continue
			}
			if hasOverlappingTags(mem.Tags, e.Memory.Tags) {
				seen[e.Memory.ID] = relatesTagWeight
			}
		}
	}

	// Pass 2: nearest neighbours in embedding space.
	//
	// Only memories that stand out from this project's own distribution of
	// similarities are linked. Vector search always returns its k nearest
	// neighbours however far away they are, so linking without a cut connects
	// every write to k arbitrary memories: a complete graph in slow motion,
	// carrying no information and making every later traversal costlier.
	//
	// The cut is mean + relatedStdDevs standard deviations over the candidate
	// set, not a constant, because similarity scales belong to the embedder
	// rather than to the data. See relatedStdDevs.
	if vector != nil {
		neighbours, err := s.repo.SearchByVector(ctx, vector, mem.ProjectID, autoLinkCandidates)
		if err != nil {
			logging.FromContext(ctx).Warn("semantic auto-linking skipped: vector search failed",
				slog.String("memory_id", mem.ID.String()),
				slog.Any("error", err))
		} else {
			scores := make([]float64, 0, len(neighbours))
			for _, n := range neighbours {
				if !skip(n.Memory.ID) {
					scores = append(scores, n.Score)
				}
			}

			if len(scores) > 0 {
				var sum float64
				for _, s := range scores {
					sum += s
				}
				mean := sum / float64(len(scores))

				// With too few samples the deviation is meaningless, so the
				// mean alone is the bar. See minRelatedCandidates.
				floor := mean
				if len(scores) >= minRelatedCandidates {
					var variance float64
					for _, s := range scores {
						d := s - mean
						variance += d * d
					}
					floor = mean + relatedStdDevs*math.Sqrt(variance/float64(len(scores)))
				}

				for _, n := range neighbours {
					if skip(n.Memory.ID) || n.Score < floor {
						continue
					}
					// A tag overlap already scored this pair higher; leave it.
					if w, ok := seen[n.Memory.ID]; ok && w >= n.Score {
						continue
					}
					seen[n.Memory.ID] = n.Score
				}
			}
		}
	}

	candidates := make([]candidate, 0, len(seen))
	for id, w := range seen {
		candidates = append(candidates, candidate{id: id, weight: w})
	}

	// Strongest first, so the cap keeps the best evidence rather than whichever
	// pair the map happened to yield first. The ID is the tie-break: map order
	// is randomised in Go, and without it the same write could produce
	// different edges on each run.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].weight != candidates[j].weight {
			return candidates[i].weight > candidates[j].weight
		}
		return candidates[i].id.String() < candidates[j].id.String()
	})

	// Capped for the same reason as detectAndSupersede: unbounded linking
	// makes the graph grow with writes x candidates, and every later traversal
	// over those nodes pays for it.
	if len(candidates) > maxRelatesPerStore {
		candidates = candidates[:maxRelatesPerStore]
	}

	now := time.Now().UTC()
	edges := make([]model.Edge, 0, len(candidates))
	for _, c := range candidates {
		edges = append(edges, model.Edge{
			ID:           uuid.New(),
			FromID:       mem.ID,
			ToID:         c.id,
			Relationship: model.RelRelatesTo,
			Weight:       c.weight,
			CreatedAt:    now,
		})
	}

	// One statement rather than a round trip per match, for the same reason as
	// detectAndSupersede: this runs inline on every Store.
	if err := s.repo.CreateEdges(ctx, edges); err != nil {
		logging.FromContext(ctx).Warn("writing relates_to edges failed; the memory stays unlinked",
			slog.String("memory_id", mem.ID.String()),
			slog.Int("edge_count", len(edges)),
			slog.Any("error", err))
		return 0
	}
	metrics.EdgesTotal.WithLabelValues(string(model.RelRelatesTo)).Add(float64(len(edges)))
	return len(edges)
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

// Vector candidate pool sizing, mirroring the graph retriever's over-fetch in
// internal/graph. Both LIMITs run before ranking, so both decide what the
// ranking layer is allowed to see, and a pool that is generous on one side but
// tight on the other leaves the hybrid with only one working retriever.
//
// The cap is lower than the graph side's 500 because each vector candidate
// carries an embedding to hydrate, so the memory cost per row is far higher.
// 200 still comfortably exceeds any plausible topK.
const (
	vectorCandidatePoolFactor = 10
	maxVectorCandidatePool    = 200
)

// vectorCandidatePool returns how many nearest neighbours to fetch for a query
// asking for topK results.
func vectorCandidatePool(topK int32) int {
	pool := int(topK) * vectorCandidatePoolFactor
	if pool > maxVectorCandidatePool {
		pool = maxVectorCandidatePool
	}
	if pool < 1 {
		pool = 1
	}
	return pool
}

// ParseQuery converts a raw query string and request parameters into a
// graph.QueryFilter. It extracts keywords (filtering stop words) and clamps
// topK to a safe bound.
func ParseQuery(query string, projectID string, types []model.MemoryType, topK int32) graph.QueryFilter {
	keywords := extractKeywords(query)

	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}

	return graph.QueryFilter{
		ProjectID: projectID,
		Keywords:  keywords,
		Types:     types,
		TopK:      topK,
		// Query ranks its results, so it must see more than TopK candidates:
		// the Cypher LIMIT runs before ranking, and created_at ties make the
		// database's choice among equals arbitrary.
		OverFetch: true,
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
		// Strip surrounding punctuation before anything else. Keywords are
		// matched with CONTAINS against stored content, so a token carrying
		// its sentence punctuation can never match: "group?" is not a
		// substring of "...support group yesterday". Questions are the normal
		// way a memory engine is queried, and the "?" attaches to the last
		// word, which is often the most specific one in the query.
		//
		// Trimmed only at the edges, so identifiers keep the punctuation that
		// is part of them: "api-key", "node.js" and "user's" survive intact.
		// A token that was nothing but punctuation trims to empty and is then
		// dropped by the length check below.
		w = strings.Trim(w, `.,;:!?"'()[]{}<>-`)
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

// mergeResults combines graph-retrieved and vector-retrieved results into a
// single deduplicated slice, carrying each candidate's retrieval relevance
// forward for the ranking layer.
//
// Graph hits arrive with a lexical Relevance already assigned by the caller;
// vector hits carry cosine similarity in Score, which is normalized into
// Relevance here. When a memory is found by both retrievers the two signals are
// combined via ranking.CombineRelevance, so cross-strategy agreement raises the
// memory above what either retriever claimed alone.
//
// The returned slice is sorted by memory ID. Merging happens through a map, and
// Go randomizes map iteration, so without this the candidate order (and
// therefore the resolution of any score tie downstream) would vary between
// identical queries.
func mergeResults(graph, vector []model.MemoryWithContext, keywords []string) []model.MemoryWithContext {
	seen := make(map[uuid.UUID]*model.MemoryWithContext, len(graph)+len(vector))
	hasKeywords := len(keywords) > 0

	// Track the cosine signal separately from the lexical one. Overwriting a
	// lexical score with a cosine score, or vice versa, is what let an
	// unmatched memory outrank a verbatim match.
	cosine := make(map[uuid.UUID]float64, len(vector))
	for _, r := range vector {
		cosine[r.Memory.ID] = r.Score
	}

	// Add all graph results, whose lexical Relevance was set by the caller.
	for i := range graph {
		r := graph[i]
		seen[r.Memory.ID] = &r
	}

	// Add vector-only results. A memory the graph retriever did not return did
	// not match any keyword, so it carries no lexical evidence.
	for i := range vector {
		r := vector[i]
		if _, ok := seen[r.Memory.ID]; ok {
			continue
		}
		r.Relevance = 0
		seen[r.Memory.ID] = &r
	}

	// Resolve every candidate on one scale.
	for id, r := range seen {
		r.Relevance = ranking.RelevanceTier(r.Relevance, cosine[id], hasKeywords)
	}

	results := make([]model.MemoryWithContext, 0, len(seen))
	for _, r := range seen {
		results = append(results, *r)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Memory.ID.String() < results[j].Memory.ID.String()
	})
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
