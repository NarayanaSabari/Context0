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

// AGERepository implements Repository using Apache AGE on PostgreSQL.
type AGERepository struct {
	pool *pgxpool.Pool
}

// NewAGERepository creates a new AGE-backed repository.
func NewAGERepository(pool *pgxpool.Pool) *AGERepository {
	return &AGERepository{pool: pool}
}

// Close releases the connection pool.
func (r *AGERepository) Close() {
	r.pool.Close()
}

// InitSchema creates the AGE graph and loads the extension.
func (r *AGERepository) InitSchema(ctx context.Context) error {
	// Ensure AGE extension is loaded.
	if _, err := r.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS age`); err != nil {
		return fmt.Errorf("create age extension: %w", err)
	}

	// Add ag_catalog to search path for this session.
	if _, err := r.pool.Exec(ctx, `SET search_path = ag_catalog, "$user", public`); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}

	// Create graph (ignore error if it already exists).
	_, err := r.pool.Exec(ctx, `SELECT * FROM ag_catalog.create_graph('`+GraphName+`')`)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create graph: %w", err)
	}

	return nil
}

// cypher executes a Cypher query via AGE's ag_catalog.cypher function.
// Returns the raw rows for the caller to scan.
func (r *AGERepository) cypher(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	// AGE requires SET search_path per connection, and wraps Cypher in a SQL call.
	sql := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$) AS (result agtype)`, GraphName, query)
	return r.pool.Query(ctx, sql, args...)
}

// cypherExec executes a Cypher query that doesn't return rows.
func (r *AGERepository) cypherExec(ctx context.Context, query string) error {
	sql := fmt.Sprintf(`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$) AS (result agtype)`, GraphName, query)
	_, err := r.pool.Exec(ctx, sql)
	return err
}

// --- Project ---

func (r *AGERepository) CreateProject(ctx context.Context, project model.Project) error {
	q := fmt.Sprintf(
		`CREATE (p:Project {id: '%s', name: '%s', created_at: '%s'}) RETURN p`,
		escapeCypher(project.ID),
		escapeCypher(project.Name),
		project.CreatedAt.Format(time.RFC3339),
	)
	return r.cypherExec(ctx, q)
}

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

func (r *AGERepository) DeleteMemory(ctx context.Context, id uuid.UUID) error {
	q := fmt.Sprintf(`MATCH (m:Memory {id: '%s'}) DETACH DELETE m`, id.String())
	return r.cypherExec(ctx, q)
}

func (r *AGERepository) IncrementAccessCount(ctx context.Context, id uuid.UUID) error {
	q := fmt.Sprintf(
		`MATCH (m:Memory {id: '%s'}) SET m.access_count = m.access_count + 1 RETURN m`,
		id.String(),
	)
	return r.cypherExec(ctx, q)
}

// --- Edge ---

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

func (r *AGERepository) GetEdgesFrom(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error) {
	q := fmt.Sprintf(
		`MATCH (a {id: '%s'})-[e]->(b) RETURN properties(e), label(e), a.id, b.id`,
		nodeID.String(),
	)
	return r.queryEdges(ctx, q)
}

func (r *AGERepository) GetEdgesTo(ctx context.Context, nodeID uuid.UUID) ([]model.Edge, error) {
	q := fmt.Sprintf(
		`MATCH (a)-[e]->(b {id: '%s'}) RETURN properties(e), label(e), a.id, b.id`,
		nodeID.String(),
	)
	return r.queryEdges(ctx, q)
}

func (r *AGERepository) DeleteEdgesForNode(ctx context.Context, nodeID uuid.UUID) error {
	q := fmt.Sprintf(`MATCH (n {id: '%s'})-[e]-() DELETE e`, nodeID.String())
	return r.cypherExec(ctx, q)
}

func (r *AGERepository) queryEdges(ctx context.Context, q string) ([]model.Edge, error) {
	// This is a simplified implementation — AGE returns agtype that needs parsing.
	// Full implementation will parse multi-column returns from Cypher.
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

func (r *AGERepository) QueryMemories(ctx context.Context, filter QueryFilter) ([]model.MemoryWithContext, error) {
	// Build a Cypher MATCH query with filters.
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

// --- Stats ---

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

// escapeCypher escapes single quotes in strings for Cypher injection safety.
func escapeCypher(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// toEdgeLabel converts a RelationshipType to a Cypher edge label.
func toEdgeLabel(rel model.RelationshipType) string {
	return string(rel) // e.g. "relates_to", "supersedes", "caused_by"
}

// parseMemory parses an agtype JSON properties result into a Memory.
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

// parseProject parses an agtype JSON properties result into a Project.
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

// parseSession parses an agtype JSON properties result into a Session.
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
