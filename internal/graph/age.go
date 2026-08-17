// Package graph implements graph-backed persistence for Context0's memory
// storage using Apache AGE, a PostgreSQL extension that adds openCypher graph
// query support. Graph data (nodes and edges) lives inside AGE's internal
// storage, while vector embeddings are stored in a separate pgvector-backed
// table (public.memory_embeddings) to leverage HNSW indexing for fast
// approximate nearest neighbor search.
package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GraphName is the name of the Apache AGE graph instance that stores all
// Context0 nodes (Memory, Session) and their relationships. This constant is
// referenced in every Cypher query executed via AGERepository.
const GraphName = "context0"

// searchPath puts ag_catalog ahead of the default schemas so AGE's custom
// types (agtype, graphid) resolve unqualified.
const searchPath = `SET search_path = ag_catalog, "$user", public`

// NewPool opens a connection pool configured for Apache AGE.
//
// search_path is session state, so it must be set on EVERY pooled connection,
// not once at startup: a pool that opens a second connection under concurrency,
// or recycles the first one after an idle timeout, would otherwise hand out
// connections where `agtype` does not resolve and every Cypher query fails with
// `type "agtype" does not exist`. The AfterConnect hook makes that impossible.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, searchPath)
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

// QueryFilter defines filters for graph traversal queries. Callers combine
// multiple filter fields to narrow down which Memory nodes are returned.
// All non-zero fields are AND-ed together; Keywords are OR-ed within the group.
type QueryFilter struct {
	// ProjectID restricts results to memories belonging to this project.
	ProjectID string
	// Keywords are matched case-insensitively against memory content and tags.
	Keywords []string
	// Types restricts results to specific memory types (e.g. fact, episode).
	Types []model.MemoryType
	// TopK caps the number of results returned. Defaults to 5 if zero.
	TopK int32
}

// AGERepository stores and queries Context0's memory graph using Apache AGE.
// All Cypher queries are executed through AGE's ag_catalog.cypher() SQL
// function, which wraps a Cypher string inside a standard PostgreSQL query.
// Results come back as agtype values (a JSON-like format specific to AGE)
// that are parsed into Go structs by the helper functions in this file.
type AGERepository struct {
	pool         *pgxpool.Pool
	embeddingDim int // dimension of the pgvector column (must match the Embedder)
}

// NewAGERepository creates a new AGE-backed repository. The embeddingDim
// parameter sets the width of the pgvector column in public.memory_embeddings
// and must match the Dimension() of the configured Embedder. Common values:
//   - 384: bag-of-words / all-MiniLM-L6-v2
//   - 768: nomic-embed-text / text-embedding-004
//   - 1536: text-embedding-3-small (OpenAI)
//
// If embeddingDim is zero or negative, it defaults to 384.
func NewAGERepository(pool *pgxpool.Pool, embeddingDim int) *AGERepository {
	if embeddingDim <= 0 {
		embeddingDim = 384
	}
	return &AGERepository{pool: pool, embeddingDim: embeddingDim}
}

// Close releases the underlying pgxpool connection pool.
func (r *AGERepository) Close() {
	r.pool.Close()
}

// InitSchema sets up all required PostgreSQL extensions, tables, indexes, and
// the AGE graph. The order matters:
//  1. pgvector extension and embeddings table (in the public schema)
//  2. HNSW index on the embedding column for cosine similarity search
//  3. AGE extension
//  4. AGE graph creation (idempotent -- ignores "already exists" errors)
//
// search_path is not set here: NewPool applies it to every pooled connection,
// which is the only way it can hold for connections opened later.
//
// This method is safe to call on every startup.
func (r *AGERepository) InitSchema(ctx context.Context) error {
	// SCHEMA public is explicit because NewPool puts ag_catalog first on the
	// search_path, and an unqualified CREATE EXTENSION would follow it there.
	if _, err := r.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector SCHEMA public`); err != nil {
		return fmt.Errorf("create vector extension: %w", err)
	}

	// Embeddings table: bridges AGE memory nodes (by UUID string) to their
	// dense vector representations. The project_id column enables scoped
	// similarity search without joining back into the AGE graph.
	createTable := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS public.memory_embeddings (
			memory_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			embedding vector(%d) NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)
	`, r.embeddingDim)
	if _, err := r.pool.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("create public.memory_embeddings table: %w", err)
	}

	// HNSW index enables sub-linear approximate nearest neighbor queries.
	// vector_cosine_ops is chosen because cosine similarity is the standard
	// metric for text embedding comparison.
	if _, err := r.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_embeddings_vector
		ON public.memory_embeddings USING hnsw (embedding vector_cosine_ops)
	`); err != nil {
		return fmt.Errorf("create vector index: %w", err)
	}

	// --- AGE setup ---
	if _, err := r.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS age`); err != nil {
		return fmt.Errorf("create age extension: %w", err)
	}

	// Create the named graph. AGE does not support IF NOT EXISTS, so we
	// catch and ignore the "already exists" error.
	_, err := r.pool.Exec(ctx, `SELECT * FROM ag_catalog.create_graph('`+GraphName+`')`)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create graph: %w", err)
	}

	return nil
}

// cypher executes a Cypher query via AGE's ag_catalog.cypher() SQL wrapper.
// The Cypher string is embedded in a dollar-quoted block ($$ ... $$) to avoid
// escaping issues with single quotes inside the query. The result column is
// typed as agtype, which is AGE's JSON-like data format. Callers must scan
// each row as a string and parsed via scanAgtype/scanOne into a typed value.
func (r *AGERepository) cypher(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	sql := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$) AS (result ag_catalog.agtype)`, GraphName, query)
	return r.pool.Query(ctx, sql, args...)
}

// cypherExec executes a Cypher query that returns no meaningful rows (e.g.
// CREATE, DELETE, SET). It discards any result and returns only the error.
func (r *AGERepository) cypherExec(ctx context.Context, query string) error {
	sql := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$) AS (result ag_catalog.agtype)`, GraphName, query)
	_, err := r.pool.Exec(ctx, sql)
	return err
}

// --- Memory ---

// CreateMemory creates a :Memory vertex with content, type, project_id, tags,
// and metadata properties. Tags are serialized as a JSON string because AGE
// does not natively support list-typed properties. The initial access_count
// is 0 and decay_score is 1.0 (no decay).
func (r *AGERepository) CreateMemory(ctx context.Context, mem model.Memory) error {
	tagsJSON, _ := json.Marshal(mem.Tags)
	q := fmt.Sprintf(
		`CREATE (m:Memory {id: '%s', content: '%s', type: '%s', project_id: '%s', tags: '%s', created_at: '%s', access_count: 0, decay_score: 1.0}) RETURN m`,
		mem.ID.String(),
		escapeCypher(mem.Content),
		escapeCypher(string(mem.Type)),
		escapeCypher(mem.ProjectID),
		escapeCypher(string(tagsJSON)),
		mem.CreatedAt.Format(time.RFC3339),
	)
	return r.cypherExec(ctx, q)
}

// GetMemory retrieves a single memory node by its UUID. Returns an error
// if the node does not exist.
func (r *AGERepository) GetMemory(ctx context.Context, id uuid.UUID) (model.Memory, error) {
	q := fmt.Sprintf(`MATCH (m:Memory {id: '%s'}) RETURN properties(m)`, id.String())
	rows, err := r.cypher(ctx, q)
	if err != nil {
		return model.Memory{}, fmt.Errorf("get memory: %w", err)
	}

	props, found, err := scanOne[memoryProps](rows)
	if err != nil {
		return model.Memory{}, fmt.Errorf("scan memory: %w", err)
	}
	if !found {
		return model.Memory{}, fmt.Errorf("memory not found: %s", id)
	}

	return props.toModel(), nil
}

// DeleteMemory removes a memory node and all its connected edges using
// Cypher's DETACH DELETE, which automatically deletes relationships first.
func (r *AGERepository) DeleteMemory(ctx context.Context, id uuid.UUID) error {
	q := fmt.Sprintf(`MATCH (m:Memory {id: '%s'}) DETACH DELETE m`, id.String())
	return r.cypherExec(ctx, q)
}

// IncrementAccessCount atomically increases a memory's access_count by 1.
// This counter feeds into the ranking layer to prioritize frequently accessed
// memories during retrieval.
func (r *AGERepository) IncrementAccessCount(ctx context.Context, id uuid.UUID) error {
	q := fmt.Sprintf(
		`MATCH (m:Memory {id: '%s'}) SET m.access_count = m.access_count + 1 RETURN m`,
		id.String(),
	)
	return r.cypherExec(ctx, q)
}

// UpdateDecayScore sets a memory's decay_score property to the given value.
// Called by the consolidation pipeline's decay phase after recomputing scores.
func (r *AGERepository) UpdateDecayScore(ctx context.Context, id uuid.UUID, score float64) error {
	q := fmt.Sprintf(
		`MATCH (m:Memory {id: '%s'}) SET m.decay_score = %f RETURN m`,
		id.String(),
		score,
	)
	return r.cypherExec(ctx, q)
}

// --- Edge ---

// CreateEdge idempotently asserts a directed, labeled relationship between
// two nodes. The Cypher MATCH clause finds both endpoints by id
// (label-agnostic), then MERGE matches or creates the edge keyed on
// (from, relationship, to) -- AGE's MERGE on a relationship pattern is
// idempotent, so re-asserting the same triple is a no-op rather than piling
// up duplicate edges. coalesce() in the SET clause makes property writes
// first-writer-wins: on the first assert the caller's id/weight/created_at
// are stored, and on every subsequent re-assert of the same triple the
// existing values are kept. The RETURN clause reports the effective edge --
// the one now actually in the graph -- so callers observe what was really
// stored (their own values on first write, the pre-existing ones on a
// re-assert) rather than blindly echoing their input back.
func (r *AGERepository) CreateEdge(ctx context.Context, edge model.Edge) (model.Edge, error) {
	relLabel := string(edge.Relationship)
	q := fmt.Sprintf(
		`MATCH (a {id: '%s'}), (b {id: '%s'}) `+
			`MERGE (a)-[e:%s]->(b) `+
			`SET e.id = coalesce(e.id, '%s'), e.weight = coalesce(e.weight, %f), e.created_at = coalesce(e.created_at, '%s') `+
			`RETURN {edge_id: e.id, from_id: '%s', to_id: '%s', relationship: '%s', weight: e.weight, created_at: e.created_at}`,
		edge.FromID.String(),
		edge.ToID.String(),
		relLabel,
		edge.ID.String(),
		edge.Weight,
		edge.CreatedAt.Format(time.RFC3339),
		edge.FromID.String(),
		edge.ToID.String(),
		relLabel,
	)

	rows, err := r.cypher(ctx, q)
	if err != nil {
		return model.Edge{}, fmt.Errorf("create edge: %w", err)
	}

	er, found, err := scanOne[edgeRow](rows)
	if err != nil {
		return model.Edge{}, fmt.Errorf("scan edge: %w", err)
	}
	if !found {
		return model.Edge{}, fmt.Errorf("create edge: no endpoints matched for %s -> %s", edge.FromID, edge.ToID)
	}

	edgeID, _ := uuid.Parse(er.EdgeID)
	createdAt, _ := time.Parse(time.RFC3339, er.CreatedAt)

	return model.Edge{
		ID:           edgeID,
		FromID:       edge.FromID,
		ToID:         edge.ToID,
		Relationship: edge.Relationship,
		Weight:       er.Weight,
		CreatedAt:    createdAt,
	}, nil
}

// --- Session ---

// CreateSession creates a :Session vertex and a :belongs_to edge linking it
// to the parent :Project vertex. This two-step operation ensures every session
// is connected to its project in the graph for traversal queries.
func (r *AGERepository) CreateSession(ctx context.Context, sess model.Session) error {
	q := fmt.Sprintf(
		`CREATE (s:Session {id: '%s', project_id: '%s', agent_id: '%s', started_at: '%s'}) RETURN s`,
		sess.ID.String(),
		escapeCypher(sess.ProjectID),
		escapeCypher(sess.AgentID),
		sess.StartedAt.Format(time.RFC3339),
	)
	if err := r.cypherExec(ctx, q); err != nil {
		return err
	}

	// Create belongs_to edge to project.
	edge := model.Edge{
		ID:           uuid.New(),
		FromID:       sess.ID,
		ToID:         uuid.Nil, // Project nodes use string IDs, handled separately.
		Relationship: model.RelBelongsTo,
		Weight:       1.0,
		CreatedAt:    sess.StartedAt,
	}
	belongsQ := fmt.Sprintf(
		`MATCH (s:Session {id: '%s'}), (p:Project {id: '%s'}) CREATE (s)-[e:belongs_to {id: '%s', weight: 1.0, created_at: '%s'}]->(p) RETURN e`,
		sess.ID.String(),
		escapeCypher(sess.ProjectID),
		edge.ID.String(),
		sess.StartedAt.Format(time.RFC3339),
	)
	return r.cypherExec(ctx, belongsQ)
}

// EndSession sets the ended_at timestamp on a session node to the current
// UTC time and returns the updated session. The Cypher SET clause performs
// an in-place property update on the matched vertex.
func (r *AGERepository) EndSession(ctx context.Context, id uuid.UUID) (model.Session, error) {
	now := time.Now().UTC()
	q := fmt.Sprintf(
		`MATCH (s:Session {id: '%s'}) SET s.ended_at = '%s' RETURN properties(s)`,
		id.String(),
		now.Format(time.RFC3339),
	)
	rows, err := r.cypher(ctx, q)
	if err != nil {
		return model.Session{}, fmt.Errorf("end session: %w", err)
	}

	props, found, err := scanOne[sessionProps](rows)
	if err != nil {
		return model.Session{}, fmt.Errorf("scan session: %w", err)
	}
	if !found {
		return model.Session{}, fmt.Errorf("session not found: %s", id)
	}

	return props.toModel(), nil
}

// LinkMemoryToSession creates a :contains edge from a Session to a Memory,
// recording that the memory was produced or accessed during that session.
func (r *AGERepository) LinkMemoryToSession(ctx context.Context, sessionID, memoryID uuid.UUID) error {
	edgeID := uuid.New()
	q := fmt.Sprintf(
		`MATCH (s:Session {id: '%s'}), (m:Memory {id: '%s'}) CREATE (s)-[e:contains {id: '%s', weight: 1.0, created_at: '%s'}]->(m) RETURN e`,
		sessionID.String(),
		memoryID.String(),
		edgeID.String(),
		time.Now().UTC().Format(time.RFC3339),
	)
	return r.cypherExec(ctx, q)
}

// --- Query ---

// QueryMemories builds a dynamic Cypher MATCH query from the given filter.
// Filter conditions are AND-ed: project_id, memory types, and keywords.
// Keywords use Cypher's toLower() + CONTAINS for case-insensitive substring
// matching against both content and tags (OR-ed within the keyword group).
// Results are ordered by created_at DESC and capped at filter.TopK (default 5).
// Each result gets a placeholder Score of 1.0; real scoring is deferred to
// the ranking layer.
func (r *AGERepository) QueryMemories(ctx context.Context, filter QueryFilter) ([]model.MemoryWithContext, error) {
	var conditions []string
	if filter.ProjectID != "" {
		conditions = append(conditions, fmt.Sprintf("m.project_id = '%s'", escapeCypher(filter.ProjectID)))
	}

	if len(filter.Types) > 0 {
		var typeStrs []string
		for _, t := range filter.Types {
			typeStrs = append(typeStrs, fmt.Sprintf("'%s'", escapeCypher(string(t))))
		}
		conditions = append(conditions, fmt.Sprintf("m.type IN [%s]", strings.Join(typeStrs, ", ")))
	}

	// For MVP: keyword search via CONTAINS on content and tags.
	if len(filter.Keywords) > 0 {
		var keywordConds []string
		for _, kw := range filter.Keywords {
			kw = escapeCypher(strings.ToLower(kw))
			keywordConds = append(keywordConds, fmt.Sprintf("(toLower(m.content) CONTAINS '%s' OR toLower(m.tags) CONTAINS '%s')", kw, kw))
		}
		conditions = append(conditions, "("+strings.Join(keywordConds, " OR ")+")")
	}

	topK := filter.TopK
	if topK <= 0 {
		topK = 5
	}

	var q string
	if len(conditions) > 0 {
		q = fmt.Sprintf(
			`MATCH (m:Memory) WHERE %s RETURN properties(m) ORDER BY m.created_at DESC LIMIT %d`,
			strings.Join(conditions, " AND "),
			topK,
		)
	} else {
		q = fmt.Sprintf(
			`MATCH (m:Memory) RETURN properties(m) ORDER BY m.created_at DESC LIMIT %d`,
			topK,
		)
	}

	rows, err := r.cypher(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}

	props, err := scanAgtype[memoryProps](rows)
	var results []model.MemoryWithContext
	for _, p := range props {
		results = append(results, model.MemoryWithContext{
			Memory: p.toModel(),
			Score:  1.0, // Scoring will be enhanced in ranking layer.
		})
	}

	return results, err
}

// GetSubgraph returns all unique Memory nodes directly connected to a center
// node, along with the edges connecting them. AGE has limited support for
// variable-length path patterns, so this performs a 1-hop neighbor lookup.
func (r *AGERepository) GetSubgraph(ctx context.Context, centerID uuid.UUID) ([]model.Memory, []model.Edge, error) {
	// Get direct neighbors. AGE has limited variable-length path support,
	// so this is a 1-hop lookup.
	q := fmt.Sprintf(
		`MATCH (center {id: '%s'})-[e]-(neighbor) RETURN properties(neighbor)`,
		centerID.String(),
	)

	rows, err := r.cypher(ctx, q)
	if err != nil {
		return nil, nil, fmt.Errorf("get subgraph: %w", err)
	}

	props, err := scanAgtype[memoryProps](rows)
	if err != nil {
		return nil, nil, fmt.Errorf("get subgraph: %w", err)
	}

	seen := make(map[string]bool)
	var memories []model.Memory
	for _, p := range props {
		mem := p.toModel()
		if !seen[mem.ID.String()] {
			seen[mem.ID.String()] = true
			memories = append(memories, mem)
		}
	}

	edges, err := r.getEdgesAround(ctx, centerID)
	if err != nil {
		return nil, nil, fmt.Errorf("get subgraph edges: %w", err)
	}

	return memories, edges, nil
}

// edgeRow is the wire shape of a single edge returned by the map literal in
// getEdgesAround and the context-edge queries below. AGE's cypher() helper
// hardcodes a single `agtype` result column, so multi-value Cypher RETURN
// clauses are collapsed into one map literal here and unmarshalled as JSON.
type edgeRow struct {
	EdgeID       string  `json:"edge_id"`
	FromID       string  `json:"from_id"`
	ToID         string  `json:"to_id"`
	Relationship string  `json:"relationship"`
	Weight       float64 `json:"weight"`
	CreatedAt    string  `json:"created_at"`
}

// contextEdgeRow is the wire shape of a single context edge returned by the
// map literal in GetContextEdges.
type contextEdgeRow struct {
	MemoryID      string  `json:"memory_id"`
	Relationship  string  `json:"relationship"`
	Weight        float64 `json:"weight"`
	TargetID      string  `json:"target_id"`
	TargetContent string  `json:"target_content"`
}

// scanAgtype scans every row as an agtype JSON string and unmarshals it into
// T. A row that fails to scan or unmarshal is skipped rather than treated as
// fatal, so one malformed node doesn't fail an entire query; rows.Err() is
// still returned once the loop ends.
func scanAgtype[T any](rows pgx.Rows) ([]T, error) {
	defer rows.Close()

	var results []T
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			continue
		}
		results = append(results, v)
	}

	return results, rows.Err()
}

// scanOne scans a single row as an agtype JSON string and unmarshals it into
// T. The returned bool reports whether a row existed at all; callers decide
// whether a missing row is an error or a valid zero value.
func scanOne[T any](rows pgx.Rows) (T, bool, error) {
	defer rows.Close()

	var zero T
	if !rows.Next() {
		return zero, false, rows.Err()
	}

	var raw string
	if err := rows.Scan(&raw); err != nil {
		return zero, false, err
	}
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return zero, false, err
	}

	return v, true, nil
}

// getEdgesAround returns every edge with at least one endpoint at centerID,
// deduplicated by edge id. startNode()/endNode() recover the true directed
// endpoints of each relationship even though the MATCH pattern itself is
// undirected.
func (r *AGERepository) getEdgesAround(ctx context.Context, centerID uuid.UUID) ([]model.Edge, error) {
	q := fmt.Sprintf(
		`MATCH (center {id: '%s'})-[e]-() RETURN {edge_id: e.id, from_id: startNode(e).id, to_id: endNode(e).id, relationship: label(e), weight: e.weight, created_at: e.created_at}`,
		centerID.String(),
	)

	rows, err := r.cypher(ctx, q)
	if err != nil {
		return nil, err
	}

	ers, err := scanAgtype[edgeRow](rows)

	seen := make(map[string]bool)
	var edges []model.Edge
	for _, er := range ers {
		if seen[er.EdgeID] {
			continue
		}
		seen[er.EdgeID] = true

		edgeID, _ := uuid.Parse(er.EdgeID)
		fromID, _ := uuid.Parse(er.FromID)
		toID, _ := uuid.Parse(er.ToID)
		createdAt, _ := time.Parse(time.RFC3339, er.CreatedAt)

		edges = append(edges, model.Edge{
			ID:           edgeID,
			FromID:       fromID,
			ToID:         toID,
			Relationship: model.RelationshipType(er.Relationship),
			Weight:       er.Weight,
			CreatedAt:    createdAt,
		})
	}

	return edges, err
}

// GetContextEdges returns, for each of the given memory ids, the edges
// connecting it to its neighbors -- used to explain why a query result was
// returned. A single Cypher query handles all ids at once (bounded by
// WHERE m.id IN [...]) so callers issue one round trip regardless of how
// many memories are being explained.
func (r *AGERepository) GetContextEdges(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]model.ContextEdge, error) {
	result := make(map[uuid.UUID][]model.ContextEdge)
	if len(ids) == 0 {
		return result, nil
	}

	idStrs := make([]string, len(ids))
	for i, id := range ids {
		idStrs[i] = fmt.Sprintf("'%s'", id.String())
	}

	q := fmt.Sprintf(
		`MATCH (m:Memory)-[e]-(n:Memory) WHERE m.id IN [%s] RETURN {memory_id: m.id, relationship: label(e), weight: e.weight, target_id: n.id, target_content: n.content}`,
		strings.Join(idStrs, ", "),
	)

	rows, err := r.cypher(ctx, q)
	if err != nil {
		return nil, err
	}

	crs, err := scanAgtype[contextEdgeRow](rows)
	for _, cr := range crs {
		memID, perr := uuid.Parse(cr.MemoryID)
		if perr != nil {
			continue
		}
		targetID, perr := uuid.Parse(cr.TargetID)
		if perr != nil {
			continue
		}
		result[memID] = append(result[memID], model.ContextEdge{
			Relationship:  model.RelationshipType(cr.Relationship),
			TargetID:      targetID,
			TargetContent: cr.TargetContent,
			Weight:        cr.Weight,
		})
	}

	return result, err
}

// --- Embeddings (pgvector) ---

// StoreEmbedding upserts a vector embedding for a memory node into the
// public.memory_embeddings table. The embedding is converted to pgvector's
// text format ([0.1,0.2,...]) and cast to the vector type. ON CONFLICT
// performs an upsert so re-embedding a memory replaces both the vector and
// the project_id, repairing any stale scoping from a prior write.
func (r *AGERepository) StoreEmbedding(ctx context.Context, memoryID uuid.UUID, projectID string, embedding []float32) error {
	// Convert []float32 to pgvector string format: [0.1,0.2,0.3,...]
	vecStr := float32SliceToVectorString(embedding)
	_, err := r.pool.Exec(ctx,
		`INSERT INTO public.memory_embeddings (memory_id, project_id, embedding)
		 VALUES ($1, $2, $3::vector)
		 ON CONFLICT (memory_id) DO UPDATE SET embedding = $3::vector, project_id = $2`,
		memoryID.String(), projectID, vecStr)
	if err != nil {
		return fmt.Errorf("store embedding: %w", err)
	}
	return nil
}

// SearchByVector performs approximate nearest neighbor search against stored
// embeddings using pgvector's cosine distance operator (<=>). The similarity
// score is computed as 1 - cosine_distance, yielding values in [0, 1] where
// 1 means identical. Results are ordered by ascending distance (highest
// similarity first) and limited to topK. For each matching embedding, the
// full Memory is fetched from the AGE graph via GetMemory.
func (r *AGERepository) SearchByVector(ctx context.Context, embedding []float32, projectID string, topK int) ([]model.MemoryWithContext, error) {
	if topK <= 0 {
		topK = 10
	}

	vecStr := float32SliceToVectorString(embedding)

	// Use cosine distance for similarity search.
	var query string
	var rows pgx.Rows
	var err error

	if projectID != "" {
		query = `SELECT memory_id, 1 - (embedding <=> $1::vector) AS similarity
				 FROM public.memory_embeddings
				 WHERE project_id = $2
				 ORDER BY embedding <=> $1::vector
				 LIMIT $3`
		rows, err = r.pool.Query(ctx, query, vecStr, projectID, topK)
	} else {
		query = `SELECT memory_id, 1 - (embedding <=> $1::vector) AS similarity
				 FROM public.memory_embeddings
				 ORDER BY embedding <=> $1::vector
				 LIMIT $2`
		rows, err = r.pool.Query(ctx, query, vecStr, topK)
	}
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var results []model.MemoryWithContext
	for rows.Next() {
		var memID string
		var similarity float64
		if err := rows.Scan(&memID, &similarity); err != nil {
			continue
		}

		id, err := uuid.Parse(memID)
		if err != nil {
			continue
		}

		mem, err := r.GetMemory(ctx, id)
		if err != nil {
			continue
		}

		results = append(results, model.MemoryWithContext{
			Memory: mem,
			Score:  similarity,
		})
	}

	return results, rows.Err()
}

// float32SliceToVectorString converts a Go float32 slice to pgvector's text
// input format: "[0.1,0.2,...]". This string can be cast to the vector type
// in SQL via $1::vector. Each component is formatted with strconv's shortest
// round-tripping representation for a 32-bit float, rather than truncating to
// six decimal places.
func float32SliceToVectorString(v []float32) string {
	buf := make([]byte, 0, len(v)*12+2)
	buf = append(buf, '[')
	for i, f := range v {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, float64(f), 'f', -1, 32)
	}
	buf = append(buf, ']')
	return string(buf)
}

// --- Stats ---

// count runs a Cypher count(...) query and unmarshals the resulting agtype
// number. label is used only to name the entity in error messages (e.g.
// "node", "edge"). Absence of a row is not an error -- an empty graph counts
// as zero.
func (r *AGERepository) count(ctx context.Context, query, label string) (int64, error) {
	rows, err := r.cypher(ctx, query)
	if err != nil {
		return 0, err
	}

	// agtype returns numbers as strings like "42".
	count, found, err := scanOne[int64](rows)
	if err != nil {
		return 0, fmt.Errorf("parse %s count: %w", label, err)
	}
	if !found {
		return 0, nil
	}
	return count, nil
}

// NodeCount returns the total number of vertices in the AGE graph.
func (r *AGERepository) NodeCount(ctx context.Context) (int64, error) {
	return r.count(ctx, `MATCH (n) RETURN count(n)`, "node")
}

// EdgeCount returns the total number of directed edges in the AGE graph.
func (r *AGERepository) EdgeCount(ctx context.Context) (int64, error) {
	return r.count(ctx, `MATCH ()-[e]->() RETURN count(e)`, "edge")
}

// --- Helpers ---

// escapeCypher escapes single quotes in strings to prevent Cypher injection.
// This is a minimal defense; parameterized queries should be preferred when
// AGE adds support for them.
func escapeCypher(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// memoryProps is the wire shape of a :Memory vertex's properties as returned
// by Cypher's properties() function. AGE encodes every property as JSON, so
// numeric and list-typed fields still need conversion after unmarshalling:
// tags arrives as a JSON-encoded string holding a []string, and timestamps
// arrive as RFC3339 strings.
type memoryProps struct {
	ID          string  `json:"id"`
	Content     string  `json:"content"`
	Type        string  `json:"type"`
	ProjectID   string  `json:"project_id"`
	Tags        string  `json:"tags"`
	CreatedAt   string  `json:"created_at"`
	AccessCount int64   `json:"access_count"`
	DecayScore  float64 `json:"decay_score"`
}

// toModel converts already-unmarshalled memory properties into a
// model.Memory. Tags are stored as a JSON-encoded string and decoded back
// into a []string slice; decay_score defaults to 1.0 when unset.
func (p memoryProps) toModel() model.Memory {
	id, _ := uuid.Parse(p.ID)
	createdAt, _ := time.Parse(time.RFC3339, p.CreatedAt)

	var tags []string
	if p.Tags != "" {
		_ = json.Unmarshal([]byte(p.Tags), &tags)
	}

	decayScore := p.DecayScore
	if decayScore == 0 {
		decayScore = 1.0
	}

	return model.Memory{
		ID:          id,
		Content:     p.Content,
		Type:        model.MemoryType(p.Type),
		ProjectID:   p.ProjectID,
		Tags:        tags,
		CreatedAt:   createdAt,
		AccessCount: p.AccessCount,
		DecayScore:  decayScore,
	}
}

// sessionProps is the wire shape of a :Session vertex's properties as
// returned by Cypher's properties() function.
type sessionProps struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

// toModel converts already-unmarshalled session properties into a
// model.Session. The ended_at field is optional and only populated if the
// session has ended.
func (p sessionProps) toModel() model.Session {
	id, _ := uuid.Parse(p.ID)
	startedAt, _ := time.Parse(time.RFC3339, p.StartedAt)

	sess := model.Session{
		ID:        id,
		ProjectID: p.ProjectID,
		AgentID:   p.AgentID,
		StartedAt: startedAt,
	}

	if p.EndedAt != "" {
		endedAt, _ := time.Parse(time.RFC3339, p.EndedAt)
		sess.EndedAt = &endedAt
	}

	return sess
}
