package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AGERepository implements the Repository interface using Apache AGE, a
// PostgreSQL extension that adds openCypher graph query support. Graph data
// (nodes and edges) lives inside AGE's internal storage, while vector
// embeddings are stored in a separate pgvector-backed table
// (public.memory_embeddings) to leverage HNSW indexing for fast approximate
// nearest neighbor search.
//
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
//  3. AGE extension and search_path configuration
//  4. AGE graph creation (idempotent -- ignores "already exists" errors)
//
// This method is safe to call on every startup.
func (r *AGERepository) InitSchema(ctx context.Context) error {
	// --- pgvector setup (must happen BEFORE setting search_path to ag_catalog) ---
	if _, err := r.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
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

	// AGE requires ag_catalog on the search_path so that its custom types
	// (agtype, graphid) and functions (cypher, create_graph) are resolvable.
	if _, err := r.pool.Exec(ctx, `SET search_path = ag_catalog, "$user", public`); err != nil {
		return fmt.Errorf("set search_path: %w", err)
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
// each row as a string and parse it with the appropriate parseXxx helper.
func (r *AGERepository) cypher(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	sql := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$) AS (result agtype)`, GraphName, query)
	return r.pool.Query(ctx, sql, args...)
}

// cypherExec executes a Cypher query that returns no meaningful rows (e.g.
// CREATE, DELETE, SET). It discards any result and returns only the error.
func (r *AGERepository) cypherExec(ctx context.Context, query string) error {
	sql := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$) AS (result agtype)`, GraphName, query)
	_, err := r.pool.Exec(ctx, sql)
	return err
}

// --- Project ---

// CreateProject creates a :Project vertex in the AGE graph with id, name,
// and created_at properties. The Cypher CREATE clause is used because
// projects are created once and identified by their string ID.
func (r *AGERepository) CreateProject(ctx context.Context, project model.Project) error {
	q := fmt.Sprintf(
		`CREATE (p:Project {id: '%s', name: '%s', created_at: '%s'}) RETURN p`,
		escapeCypher(project.ID),
		escapeCypher(project.Name),
		project.CreatedAt.Format(time.RFC3339),
	)
	return r.cypherExec(ctx, q)
}

// GetProject retrieves a project by its string ID. The Cypher MATCH clause
// filters on the id property, and properties(p) returns all vertex properties
// as an agtype map that is parsed by parseProject.
func (r *AGERepository) GetProject(ctx context.Context, id string) (model.Project, error) {
	q := fmt.Sprintf(`MATCH (p:Project {id: '%s'}) RETURN properties(p)`, escapeCypher(id))
	rows, err := r.cypher(ctx, q)
	if err != nil {
		return model.Project{}, fmt.Errorf("get project: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return model.Project{}, fmt.Errorf("project not found: %s", id)
	}

	var raw string
	if err := rows.Scan(&raw); err != nil {
		return model.Project{}, fmt.Errorf("scan project: %w", err)
	}

	return parseProject(raw)
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
	defer rows.Close()

	if !rows.Next() {
		return model.Memory{}, fmt.Errorf("memory not found: %s", id)
	}

	var raw string
	if err := rows.Scan(&raw); err != nil {
		return model.Memory{}, fmt.Errorf("scan memory: %w", err)
	}

	return parseMemory(raw)
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

// --- Edge ---

// CreateEdge creates a directed, labeled relationship between two nodes.
// The Cypher MATCH clause finds both endpoints by id (label-agnostic), then
// CREATE adds the edge with the relationship type as its label (e.g.
// relates_to, supersedes, caused_by) and weight/created_at as properties.
func (r *AGERepository) CreateEdge(ctx context.Context, edge model.Edge) error {
	relLabel := toEdgeLabel(edge.Relationship)
	q := fmt.Sprintf(
		`MATCH (a {id: '%s'}), (b {id: '%s'}) CREATE (a)-[e:%s {id: '%s', weight: %f, created_at: '%s'}]->(b) RETURN e`,
		edge.FromID.String(),
		edge.ToID.String(),
		relLabel,
		edge.ID.String(),
		edge.Weight,
		edge.CreatedAt.Format(time.RFC3339),
	)
	return r.cypherExec(ctx, q)
}

// GetEdgesFrom returns all outgoing edges from the node with the given ID.
// The Cypher pattern (a)-[e]->(b) matches only outbound relationships.
func (r *AGERepository) GetEdgesFrom(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error) {
	q := fmt.Sprintf(
		`MATCH (a {id: '%s'})-[e]->(b) RETURN properties(e), label(e), a.id, b.id`,
		nodeID.String(),
	)
	return r.queryEdges(ctx, q)
}

// GetEdgesTo returns all incoming edges to the node with the given ID.
// The Cypher pattern (a)-[e]->(b {id: ...}) matches only inbound relationships.
func (r *AGERepository) GetEdgesTo(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error) {
	q := fmt.Sprintf(
		`MATCH (a)-[e]->(b {id: '%s'}) RETURN properties(e), label(e), a.id, b.id`,
		nodeID.String(),
	)
	return r.queryEdges(ctx, q)
}

// DeleteEdgesForNode removes all edges (both directions) connected to the
// given node without deleting the node itself. The undirected Cypher pattern
// (n)-[e]-() matches edges in both directions.
func (r *AGERepository) DeleteEdgesForNode(ctx context.Context, nodeID uuid.UUID) error {
	q := fmt.Sprintf(`MATCH (n {id: '%s'})-[e]-() DELETE e`, nodeID.String())
	return r.cypherExec(ctx, q)
}

// queryEdges is a shared helper for GetEdgesFrom and GetEdgesTo. It executes
// the given Cypher query and attempts to parse each row into a model.Edge.
// NOTE: This is a simplified implementation. AGE returns multi-column agtype
// results that require more sophisticated parsing for full edge hydration.
func (r *AGERepository) queryEdges(ctx context.Context, q string) ([]model.Edge, error) {
	rows, err := r.cypher(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	var edges []model.Edge
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		// TODO: parse multi-column agtype response into Edge
		_ = raw
	}
	return edges, rows.Err()
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
	defer rows.Close()

	if !rows.Next() {
		return model.Session{}, fmt.Errorf("session not found: %s", id)
	}

	var raw string
	if err := rows.Scan(&raw); err != nil {
		return model.Session{}, fmt.Errorf("scan session: %w", err)
	}

	return parseSession(raw)
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
	defer rows.Close()

	var results []model.MemoryWithContext
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		mem, err := parseMemory(raw)
		if err != nil {
			continue
		}
		results = append(results, model.MemoryWithContext{
			Memory: mem,
			Score:  1.0, // Scoring will be enhanced in ranking layer.
		})
	}

	return results, rows.Err()
}

// GetSubgraph returns all unique Memory nodes reachable from a center node
// within the given hop depth. Depth is clamped to [1, 5] to prevent
// unbounded traversals. Currently performs a 1-hop neighbor lookup because
// AGE has limited support for variable-length path patterns; deeper traversals
// will be added iteratively.
func (r *AGERepository) GetSubgraph(ctx context.Context, centerID uuid.UUID, depth int32) ([]model.Memory, []model.Edge, error) {
	if depth <= 0 {
		depth = 2
	}
	if depth > 5 {
		depth = 5
	}

	// Get direct neighbors. AGE has limited variable-length path support,
	// so for MVP we do 1-hop and iterate if depth > 1 is needed later.
	_ = depth
	q := fmt.Sprintf(
		`MATCH (center {id: '%s'})-[e]-(neighbor) RETURN properties(neighbor)`,
		centerID.String(),
	)

	rows, err := r.cypher(ctx, q)
	if err != nil {
		return nil, nil, fmt.Errorf("get subgraph: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var memories []model.Memory
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		mem, err := parseMemory(raw)
		if err != nil {
			continue
		}
		if !seen[mem.ID.String()] {
			seen[mem.ID.String()] = true
			memories = append(memories, mem)
		}
	}

	// TODO: also return edges between the discovered nodes.
	return memories, nil, rows.Err()
}

// --- Embeddings (pgvector) ---

// StoreEmbedding upserts a vector embedding for a memory node into the
// public.memory_embeddings table. The embedding is converted to pgvector's
// text format ([0.1,0.2,...]) and cast to the vector type. ON CONFLICT
// performs an upsert so re-embedding a memory replaces the old vector.
func (r *AGERepository) StoreEmbedding(ctx context.Context, memoryID uuid.UUID, embedding []float32) error {
	// Convert []float32 to pgvector string format: [0.1,0.2,0.3,...]
	vecStr := float32SliceToVectorString(embedding)
	_, err := r.pool.Exec(ctx,
		`INSERT INTO public.memory_embeddings (memory_id, project_id, embedding)
		 VALUES ($1, (SELECT 'default'), $2::vector)
		 ON CONFLICT (memory_id) DO UPDATE SET embedding = $2::vector`,
		memoryID.String(), vecStr)
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
// input format: "[0.100000,0.200000,...]". This string can be cast to the
// vector type in SQL via $1::vector.
func float32SliceToVectorString(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf("%f", f))
	}
	b.WriteByte(']')
	return b.String()
}

// --- Stats ---

// NodeCount returns the total number of vertices in the AGE graph by running
// MATCH (n) RETURN count(n). The agtype result is a JSON number string.
func (r *AGERepository) NodeCount(ctx context.Context) (int64, error) {
	q := `MATCH (n) RETURN count(n)`
	rows, err := r.cypher(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, nil
	}

	var raw string
	if err := rows.Scan(&raw); err != nil {
		return 0, err
	}
	// agtype returns numbers as strings like "42"
	var count int64
	if err := json.Unmarshal([]byte(raw), &count); err != nil {
		return 0, fmt.Errorf("parse node count: %w", err)
	}
	return count, nil
}

// EdgeCount returns the total number of directed edges in the AGE graph.
func (r *AGERepository) EdgeCount(ctx context.Context) (int64, error) {
	q := `MATCH ()-[e]->() RETURN count(e)`
	rows, err := r.cypher(ctx, q)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, nil
	}

	var raw string
	if err := rows.Scan(&raw); err != nil {
		return 0, err
	}
	var count int64
	if err := json.Unmarshal([]byte(raw), &count); err != nil {
		return 0, fmt.Errorf("parse edge count: %w", err)
	}
	return count, nil
}

// --- Helpers ---

// escapeCypher escapes single quotes in strings to prevent Cypher injection.
// This is a minimal defense; parameterized queries should be preferred when
// AGE adds support for them.
func escapeCypher(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// toEdgeLabel converts a model.RelationshipType to a Cypher-compatible edge
// label string (e.g. "relates_to", "supersedes", "caused_by").
func toEdgeLabel(rel model.RelationshipType) string {
	return string(rel) // e.g. "relates_to", "supersedes", "caused_by"
}

// parseMemory deserializes an agtype JSON properties map (returned by
// Cypher's properties() function) into a model.Memory. Tags are stored as
// a JSON-encoded string and decoded back into a []string slice.
func parseMemory(raw string) (model.Memory, error) {
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return model.Memory{}, fmt.Errorf("parse memory json: %w", err)
	}

	id, _ := uuid.Parse(getString(props, "id"))
	createdAt, _ := time.Parse(time.RFC3339, getString(props, "created_at"))

	var tags []string
	if tagsStr := getString(props, "tags"); tagsStr != "" {
		_ = json.Unmarshal([]byte(tagsStr), &tags)
	}

	accessCount := int64(getFloat(props, "access_count"))
	decayScore := getFloat(props, "decay_score")
	if decayScore == 0 {
		decayScore = 1.0
	}

	return model.Memory{
		ID:          id,
		Content:     getString(props, "content"),
		Type:        model.MemoryType(getString(props, "type")),
		ProjectID:   getString(props, "project_id"),
		Tags:        tags,
		CreatedAt:   createdAt,
		AccessCount: accessCount,
		DecayScore:  decayScore,
	}, nil
}

// parseProject deserializes an agtype JSON properties map into a model.Project.
func parseProject(raw string) (model.Project, error) {
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return model.Project{}, fmt.Errorf("parse project json: %w", err)
	}

	createdAt, _ := time.Parse(time.RFC3339, getString(props, "created_at"))

	return model.Project{
		ID:        getString(props, "id"),
		Name:      getString(props, "name"),
		CreatedAt: createdAt,
	}, nil
}

// parseSession deserializes an agtype JSON properties map into a model.Session.
// The ended_at field is optional and only populated if the session has ended.
func parseSession(raw string) (model.Session, error) {
	var props map[string]any
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return model.Session{}, fmt.Errorf("parse session json: %w", err)
	}

	id, _ := uuid.Parse(getString(props, "id"))
	startedAt, _ := time.Parse(time.RFC3339, getString(props, "started_at"))

	sess := model.Session{
		ID:        id,
		ProjectID: getString(props, "project_id"),
		AgentID:   getString(props, "agent_id"),
		StartedAt: startedAt,
	}

	if endedStr := getString(props, "ended_at"); endedStr != "" {
		endedAt, _ := time.Parse(time.RFC3339, endedStr)
		sess.EndedAt = &endedAt
	}

	return sess, nil
}

// getString extracts a string value from an agtype properties map.
// Non-string values are converted via fmt.Sprintf. Missing keys return "".
func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

// getFloat extracts a float64 value from an agtype properties map.
// Returns 0 if the key is missing or the value is not a float64.
func getFloat(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return f
}
