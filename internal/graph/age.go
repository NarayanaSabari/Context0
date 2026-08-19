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
	"errors"
	"fmt"
	"log/slog"
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

// timestampLayout is RFC3339 with milliseconds. Timestamps are stored as
// strings in agtype and compared lexically, so the layout must be fixed-width
// and zero-padded for ordering to match chronology -- which this satisfies.
//
// Second precision was not enough: under load a single second accumulated 153
// memories, and `ORDER BY created_at DESC LIMIT k` over such a tie group
// returns an arbitrary subset.
//
// Rows written before this change carry second precision. time.RFC3339 parses
// both, so reads are unaffected. Within one second an older row sorts after a
// newer millisecond-precision one ("...19Z" > "...19.123Z" lexically), which is
// a harmless ordering quirk confined to memories written across the upgrade.
const timestampLayout = "2006-01-02T15:04:05.000Z07:00"

// defaultEmbeddingDim is the width used when creating memory_embeddings for a
// database that has none yet and the caller did not specify one. It matches
// the bag-of-words embedder and common small transformer models.
const defaultEmbeddingDim = 384

// Candidate pool sizing for QueryMemories.
//
// The Cypher LIMIT runs before ranking, so fetching exactly topK rows lets the
// database decide which memories the ranking layer is even allowed to see.
// Over-fetching by this factor gives ranking a real pool to choose from, capped
// so a large topK cannot pull an unbounded result set into memory.
const (
	candidatePoolFactor = 10
	maxCandidatePool    = 500
)

// Connection pool sizing. These are deliberately fixed rather than derived from
// the CPU count: see the comment in NewPool. Override per deployment with the
// standard pgx DSN parameters pool_max_conns and pool_min_conns.
const (
	defaultMaxConns = 10
	defaultMinConns = 2
)

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

	// Size the pool explicitly unless the DSN already says otherwise.
	//
	// pgxpool's default MaxConns is the machine's core count, which is the
	// wrong number on Kubernetes: it reflects the node, not the container's CPU
	// limit. On a 64-core node every replica would open up to 64 connections
	// and a handful of pods would exhaust Postgres's default max_connections of
	// 100. A fixed, modest default keeps replica count and database capacity
	// related in a way an operator can reason about.
	if !strings.Contains(databaseURL, "pool_max_conns") {
		cfg.MaxConns = defaultMaxConns
	}
	if !strings.Contains(databaseURL, "pool_min_conns") {
		// Keep a couple of connections warm so a request arriving after an idle
		// period does not pay TCP plus TLS plus the AfterConnect search_path
		// round trip.
		cfg.MinConns = defaultMinConns
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

	// OverFetch asks for a candidate pool larger than TopK.
	//
	// The Cypher LIMIT runs before any ranking, so a caller that ranks its
	// results must not let the database pick which TopK rows it is allowed to
	// consider -- created_at ties make that choice arbitrary. Ranking callers
	// set this and truncate to TopK themselves.
	//
	// Off by default so TopK keeps its plain meaning of "at most this many",
	// which is what every non-ranking caller (profiles, consolidation, tag
	// auto-linking) depends on.
	OverFetch bool
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
// Pass zero to mean "whatever the database already uses". Callers that never
// produce embeddings -- the consolidation job, for instance -- do that, so they
// can run against any deployment without knowing which model the server is
// configured with. When the table does not exist yet, a zero dimension creates
// it at defaultEmbeddingDim.
func NewAGERepository(pool *pgxpool.Pool, embeddingDim int) *AGERepository {
	if embeddingDim < 0 {
		embeddingDim = 0
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
	//
	// A zero embeddingDim means the caller has no opinion, so adopt whatever
	// the database already uses and fall back to the default only when the
	// table has yet to be created.
	if r.embeddingDim == 0 {
		r.embeddingDim = defaultEmbeddingDim
		if existing, err := r.existingEmbeddingDim(ctx); err != nil {
			return err
		} else if existing > 0 {
			r.embeddingDim = existing
		}
	}

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

	// CREATE TABLE IF NOT EXISTS keeps whatever width an earlier run chose, so
	// a changed embedding provider would leave the column too narrow and every
	// insert would fail at write time -- where StoreEmbedding's error is
	// discarded, silently leaving memories unsearchable by vector. Detect the
	// mismatch here instead, while it can still be reported.
	if err := r.verifyEmbeddingDim(ctx); err != nil {
		return err
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

	// An index on project_id, so a scoped vector search can filter before it
	// searches rather than after.
	//
	// Without it, the planner drives the query from the HNSW index and applies
	// `WHERE project_id = $1` to whatever that returns. On a table with many
	// projects the scan budget is spent on other projects' vectors and live
	// matches are never reached: reproduced deterministically at 40,000
	// embeddings across 500 projects, where a two-row project returned one row.
	// Raising hnsw.ef_search to 1000 and max_scan_tuples tenfold did not fix
	// it; a sequential scan returned both rows, which is what proved the loss
	// was the index and not the data.
	//
	// With this index the planner filters to the project first and sorts that
	// small set exactly, so a scoped search is both correct and cheap. The HNSW
	// index still serves unscoped searches, where there is nothing to filter.
	if _, err := r.pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_embeddings_project
		ON public.memory_embeddings (project_id)
	`); err != nil {
		return fmt.Errorf("create embedding project index: %w", err)
	}

	// The index only helps if the planner knows the table is big enough to
	// bother filtering first. With no statistics it estimates one row, picks
	// the HNSW scan, and the recall loss returns -- observed as a scoped search
	// finding 0 of 2 rows that a sequential scan found immediately.
	if _, err := r.pool.Exec(ctx, `ANALYZE public.memory_embeddings`); err != nil {
		slog.Warn("could not analyze memory_embeddings; scoped vector search may "+
			"under-return until autoanalyze runs", slog.Any("error", err))
	}

	// Vacuum this table far more eagerly than the 20% default.
	//
	// An HNSW index keeps entries for deleted rows until VACUUM removes them,
	// and a scan gives up after examining hnsw.max_scan_tuples. On a table with
	// many dead entries the budget is spent reaching them, and live matches are
	// never returned -- a query that plainly matched two rows returned one,
	// intermittently, and a VACUUM fixed it immediately.
	//
	// Deletes here come from consolidation pruning and from DeleteMemory, so
	// churn is normal rather than exceptional. 2% plus 100 rows keeps the dead
	// fraction small enough that recall does not depend on when autovacuum last
	// happened to run.
	if _, err := r.pool.Exec(ctx, `
		ALTER TABLE public.memory_embeddings SET (
			autovacuum_vacuum_scale_factor = 0.02,
			autovacuum_vacuum_threshold = 100,
			autovacuum_analyze_scale_factor = 0.02
		)
	`); err != nil {
		// Not fatal: recall degrades between vacuums rather than the engine
		// failing to start, and a managed database may refuse the ALTER.
		slog.Warn("could not tune autovacuum for memory_embeddings; "+
			"vector recall may degrade between vacuums", slog.Any("error", err))
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

	// Index the vertex properties we filter on. AGE only creates indexes for
	// its internal graphid columns, so without these every lookup by id or
	// project_id is a sequential scan of the entire label.
	if err := r.createPropertyIndexes(ctx); err != nil {
		return err
	}

	return nil
}

// memoryPropertyIndexes are the vertex properties worth indexing, in the form
// AGE's own regression suite uses (regress/sql/index.sql). AGE keeps every
// property inside one agtype column, so a plain column index is impossible:
// the index has to be on the extraction expression, and it only matches a query
// whose WHERE clause produces that identical expression.
//
// This is why the repository consistently writes `WHERE m.id = $id` rather than
// the map form `MATCH (m:Memory {id: $id})`. The two compile to different quals
// -- the map form becomes a `properties @> ...` containment check, which these
// btree indexes cannot serve.
var memoryPropertyIndexes = []struct{ name, property string }{
	// Looked up per vector-search hit, and on every Connect and Delete.
	{"memory_id_idx", "id"},
	// Filters nearly every query; also the tenant boundary.
	{"memory_project_id_idx", "project_id"},
}

// createPropertyIndexes builds the expression indexes and refreshes planner
// statistics. It is idempotent and safe on every startup.
//
// Measured at 50k vertices, these turn a lookup by id from a 5.5ms sequential
// scan into a 0.19ms index scan, and the project filter from 17.2ms to 3.6ms.
// Re-checked at 94k vertices: the id lookup still plans as an index scan
// (0.48ms). Worth re-checking rather than trusting, because the UNWIND form
// documented on uuidLiteralList did plan an index scan at 50k and stopped
// doing so by 64k -- a plan is a property of the data, not of the query.
func (r *AGERepository) createPropertyIndexes(ctx context.Context) error {
	// AGE creates a label's table lazily, on first write. Without this the
	// indexes below cannot be built on a fresh database, and the deployment
	// would run unindexed until something happened to restart it -- exactly
	// when the corpus is growing and the seq scans hurt most. create_vlabel is
	// idempotent apart from erroring when the label already exists.
	if _, err := r.pool.Exec(ctx,
		`SELECT ag_catalog.create_vlabel($1, 'Memory')`, GraphName,
	); err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create Memory label: %w", err)
	}

	for _, idx := range memoryPropertyIndexes {
		// The property name is a compile-time constant from the slice above,
		// never caller input, so interpolating it introduces no injection risk.
		stmt := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s."Memory" `+
				`(ag_catalog.agtype_access_operator(properties, '"%s"'::ag_catalog.agtype))`,
			idx.name, GraphName, idx.property,
		)
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}

	// Expression indexes have no statistics until ANALYZE runs, and without
	// them the planner may still choose a sequential scan.
	if _, err := r.pool.Exec(ctx, fmt.Sprintf(`ANALYZE %s."Memory"`, GraphName)); err != nil {
		return fmt.Errorf("analyze Memory label: %w", err)
	}

	return nil
}

// verifyEmbeddingDim checks that the existing memory_embeddings column matches
// the configured embedding dimension, returning an actionable error when an
// earlier run created the table at a different width.
func (r *AGERepository) verifyEmbeddingDim(ctx context.Context) error {
	actual, err := r.existingEmbeddingDim(ctx)
	if err != nil {
		return err
	}

	if actual > 0 && actual != r.embeddingDim {
		return fmt.Errorf(
			"embedding dimension mismatch: public.memory_embeddings stores %d-dimensional "+
				"vectors but the configured embedder produces %d. The embedding provider or "+
				"model changed since the table was created. Re-embed the corpus against the new "+
				"model, or set CONTEXT0_EMBEDDING_DIM=%d to keep the previous one",
			actual, r.embeddingDim, actual,
		)
	}

	return nil
}

// existingEmbeddingDim reports the declared width of the memory_embeddings
// vector column, or 0 when the table does not exist yet.
func (r *AGERepository) existingEmbeddingDim(ctx context.Context) (int, error) {
	// atttypmod carries the declared dimension of a pgvector column. to_regclass
	// returns NULL rather than erroring when the table is absent, which is the
	// normal state on a first run.
	const q = `SELECT atttypmod FROM pg_attribute
	           WHERE attrelid = to_regclass('public.memory_embeddings')
	             AND attname = 'embedding'`

	var dim int
	err := r.pool.QueryRow(ctx, q).Scan(&dim)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read embedding column dimension: %w", err)
	}
	if dim < 0 {
		// An unparameterized vector column reports -1.
		return 0, nil
	}
	return dim, nil
}

// params holds the values bound to a Cypher query's $named placeholders.
//
// AGE takes query parameters as a single agtype object rather than as
// positional arguments: the whole map is passed as one SQL argument, and each
// key becomes a $name usable inside the Cypher text. Values are carried as
// ordinary Go types and marshalled to JSON at execution time, so quotes,
// backslashes, and newlines in memory content never reach the parser as
// syntax.
type params map[string]any

// cypherSQL builds the SQL wrapper around a Cypher query.
//
// GraphName is a compile-time constant, so interpolating it carries no
// injection risk. Everything caller-supplied travels through the parameter
// object instead of the query text. The Cypher body sits in a dollar-quoted
// block because AGE requires a literal there.
func cypherSQL(query string, parameterized bool) string {
	if parameterized {
		return fmt.Sprintf(
			`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$, $1) AS (result ag_catalog.agtype)`,
			GraphName, query,
		)
	}
	return fmt.Sprintf(
		`SELECT * FROM ag_catalog.cypher('%s', $$ %s $$) AS (result ag_catalog.agtype)`,
		GraphName, query,
	)
}

// encodeParams marshals a parameter map into the single JSON argument AGE
// expects. A nil or empty map means the query is a constant with no bound
// values, signalled by the false return.
func encodeParams(p params) (string, bool, error) {
	if len(p) == 0 {
		return "", false, nil
	}
	encoded, err := json.Marshal(map[string]any(p))
	if err != nil {
		return "", false, fmt.Errorf("encode cypher parameters: %w", err)
	}
	return string(encoded), true, nil
}

// cypher executes a Cypher query via AGE's ag_catalog.cypher() SQL wrapper and
// returns its rows. Caller-supplied values must be passed in p and referenced
// as $name inside query -- never formatted into the query string. The result
// column is typed as agtype, AGE's JSON-like format; callers scan each row as
// a string and parse it via scanAgtype/scanOne into a typed value.
func (r *AGERepository) cypher(ctx context.Context, query string, p params) (pgx.Rows, error) {
	encoded, ok, err := encodeParams(p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return r.pool.Query(ctx, cypherSQL(query, false))
	}
	return r.pool.Query(ctx, cypherSQL(query, true), encoded)
}

// cypherExec executes a Cypher query that returns no meaningful rows (e.g.
// CREATE, DELETE, SET). It discards any result and returns only the error.
// The same parameter rules as cypher apply.
func (r *AGERepository) cypherExec(ctx context.Context, query string, p params) error {
	encoded, ok, err := encodeParams(p)
	if err != nil {
		return err
	}
	if !ok {
		_, err = r.pool.Exec(ctx, cypherSQL(query, false))
		return err
	}
	_, err = r.pool.Exec(ctx, cypherSQL(query, true), encoded)
	return err
}

// --- Memory ---

// CreateMemory creates a :Memory vertex with content, type, project_id, tags,
// and metadata properties. Tags are serialized as a JSON string because AGE
// does not natively support list-typed properties. The initial access_count
// is 0 and decay_score is 1.0 (no decay).
func (r *AGERepository) CreateMemory(ctx context.Context, mem model.Memory) error {
	tagsJSON, _ := json.Marshal(mem.Tags)
	const q = `CREATE (m:Memory {id: $id, content: $content, type: $type, project_id: $project_id, ` +
		`tags: $tags, created_at: $created_at, access_count: 0, decay_score: 1.0}) RETURN m`
	return r.cypherExec(ctx, q, params{
		"id":         mem.ID.String(),
		"content":    mem.Content,
		"type":       string(mem.Type),
		"project_id": mem.ProjectID,
		"tags":       string(tagsJSON),
		// Millisecond precision, not second: at second granularity a busy
		// project puts hundreds of memories on the same timestamp, which makes
		// any created_at ordering arbitrary within the group.
		"created_at": mem.CreatedAt.Format(timestampLayout),
	})
}

// GetMemory retrieves a single memory node by its UUID. Returns an error
// if the node does not exist.
func (r *AGERepository) GetMemory(ctx context.Context, id uuid.UUID) (model.Memory, error) {
	const q = `MATCH (m:Memory) WHERE m.id = $id RETURN properties(m)`
	rows, err := r.cypher(ctx, q, params{"id": id.String()})
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
//
// The embedding lives in a separate pgvector table that AGE knows nothing
// about, so it has to be removed explicitly. Without this every delete leaves
// an orphan row that still participates in similarity search: the HNSW index
// grows without bound, and a deleted memory keeps consuming candidate slots,
// pushing live results out of a filtered search.
//
// The embedding is deleted first. If the graph delete then fails, the memory
// simply loses vector searchability and can be re-embedded; the reverse order
// would leave an orphan pointing at a node that no longer exists.
func (r *AGERepository) DeleteMemory(ctx context.Context, id uuid.UUID) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM public.memory_embeddings WHERE memory_id = $1`, id.String(),
	); err != nil {
		return fmt.Errorf("delete embedding: %w", err)
	}

	const q = `MATCH (m:Memory) WHERE m.id = $id DETACH DELETE m`
	return r.cypherExec(ctx, q, params{"id": id.String()})
}

// IncrementAccessCount atomically increases a memory's access_count by 1.
// This counter feeds into the ranking layer to prioritize frequently accessed
// memories during retrieval.
func (r *AGERepository) IncrementAccessCount(ctx context.Context, id uuid.UUID) error {
	const q = `MATCH (m:Memory) WHERE m.id = $id SET m.access_count = m.access_count + 1 RETURN m`
	return r.cypherExec(ctx, q, params{"id": id.String()})
}

// IncrementAccessCounts bumps access_count for several memories in one
// statement.
//
// Query calls this for every result it returns, so the serial alternative put a
// full round trip per result on the read path -- ~1.4ms each at 50k vertices,
// paid before the response could be written. Batching makes the cost
// independent of top_k.
//
// A literal id list, not a parameter: see uuidLiteralList. The parameterized
// forms depend on fresh planner statistics to reach the index.
func (r *AGERepository) IncrementAccessCounts(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}

	list, err := uuidLiteralList(ids)
	if err != nil {
		return err
	}

	// Literal list, not UNWIND or a parameter: see uuidLiteralList.
	q := `MATCH (m:Memory) WHERE m.id IN ` + list +
		` SET m.access_count = m.access_count + 1`
	return r.cypherExec(ctx, q, nil)
}

// UpdateDecayScore sets a memory's decay_score property to the given value.
// Called by the consolidation pipeline's decay phase after recomputing scores.
func (r *AGERepository) UpdateDecayScore(ctx context.Context, id uuid.UUID, score float64) error {
	const q = `MATCH (m:Memory) WHERE m.id = $id SET m.decay_score = $score RETURN m`
	return r.cypherExec(ctx, q, params{"id": id.String(), "score": score})
}

// --- Edge ---

// CreateEdge idempotently asserts a directed, labeled relationship between
// two memories. The Cypher MATCH clause finds both endpoints by id, then
// MERGE matches or creates the edge keyed on
// (from, relationship, to) -- AGE's MERGE on a relationship pattern is
// idempotent, so re-asserting the same triple is a no-op rather than piling
// up duplicate edges. coalesce() in the SET clause makes property writes
// first-writer-wins: on the first assert the caller's id/weight/created_at
// are stored, and on every subsequent re-assert of the same triple the
// existing values are kept. The RETURN clause reports the effective edge --
// the one now actually in the graph -- so callers observe what was really
// stored (their own values on first write, the pre-existing ones on a
// re-assert) rather than blindly echoing their input back.
//
// Both endpoints are labeled :Memory and matched with WHERE rather than an
// inline map. Unlabeled, AGE scans every vertex table and cannot use the
// property index, which matters here more than anywhere else: a single Store
// of a tagged semantic memory fans out into roughly a dozen of these calls via
// detectAndSupersede and autoLinkByTags. Measured 0.41ms unlabeled versus
// 0.067ms labeled, per edge.
//
// Every caller connects one memory to another -- Connect, contradiction
// detection, tag auto-linking, and the consolidation merge phase. Session
// relationships are written by CreateSession and LinkMemoryToSession, which
// have their own queries, so the label costs nothing in reach.
//
// The relationship label is the one value that cannot be a query parameter,
// since openCypher has no parameter slot for labels. It is validated against
// the closed set of known relationship types before being interpolated, so
// only a fixed vocabulary of identifiers can ever reach the query text.
func (r *AGERepository) CreateEdge(ctx context.Context, edge model.Edge) (model.Edge, error) {
	if !edge.Relationship.Valid() {
		return model.Edge{}, fmt.Errorf("create edge: unknown relationship type %q", edge.Relationship)
	}
	relLabel := string(edge.Relationship)

	q := fmt.Sprintf(
		`MATCH (a:Memory), (b:Memory) WHERE a.id = $from_id AND b.id = $to_id `+
			`MERGE (a)-[e:%s]->(b) `+
			`SET e.id = coalesce(e.id, $edge_id), e.weight = coalesce(e.weight, $weight), `+
			`e.created_at = coalesce(e.created_at, $created_at) `+
			`RETURN {edge_id: e.id, from_id: $from_id, to_id: $to_id, relationship: $relationship, `+
			`weight: e.weight, created_at: e.created_at}`,
		relLabel,
	)

	rows, err := r.cypher(ctx, q, params{
		"from_id":      edge.FromID.String(),
		"to_id":        edge.ToID.String(),
		"edge_id":      edge.ID.String(),
		"weight":       edge.Weight,
		"created_at":   edge.CreatedAt.Format(time.RFC3339),
		"relationship": relLabel,
	})
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

// CreateEdges asserts many edges of the same relationship type in one
// statement, with the same first-writer-wins semantics as CreateEdge.
//
// Storing a tagged semantic memory fans out into an edge per contradiction
// detected and an edge per tag match, so the serial version put up to ~50
// round trips on a single Store. Measured on a project of identical-content
// memories, write latency climbed to 82ms and kept rising with project size.
//
// Edges whose endpoints do not both exist are silently skipped, matching the
// per-edge behaviour where MATCH simply finds nothing. Callers that need to
// know whether a specific edge was created should use CreateEdge.
func (r *AGERepository) CreateEdges(ctx context.Context, edges []model.Edge) error {
	if len(edges) == 0 {
		return nil
	}

	// One statement per relationship type: the label cannot be parameterized,
	// so edges are grouped rather than interpolated per row.
	byRel := make(map[model.RelationshipType][]model.Edge)
	for _, e := range edges {
		if !e.Relationship.Valid() {
			return fmt.Errorf("create edges: unknown relationship type %q", e.Relationship)
		}
		byRel[e.Relationship] = append(byRel[e.Relationship], e)
	}

	for rel, group := range byRel {
		// One statement per edge, with both endpoint ids inlined as literals.
		//
		// The batched UNWIND form this replaces could not use the id index for
		// `WHERE a.id = row.from_id`: AGE planned a sequential scan over the
		// whole Memory label for *each* endpoint. Measured at 64,070 vertices
		// that was 94.2ms for a single-row batch and it grew with the graph,
		// while the same MERGE with literal endpoints is 0.286ms because both
		// sides plan as index scans.
		//
		// So the round trips lose to the scans: a batch of five edges is five
		// statements at ~0.3ms rather than one statement at ~94ms. See
		// uuidLiteralList for why inlining these particular values is safe.
		for _, e := range group {
			from, err := uuidLiteralList([]uuid.UUID{e.FromID})
			if err != nil {
				return err
			}
			to, err := uuidLiteralList([]uuid.UUID{e.ToID})
			if err != nil {
				return err
			}

			q := fmt.Sprintf(
				`MATCH (a:Memory), (b:Memory) WHERE a.id IN %s AND b.id IN %s `+
					`MERGE (a)-[e:%s]->(b) `+
					`SET e.id = coalesce(e.id, $edge_id), `+
					`e.weight = coalesce(e.weight, $weight), `+
					`e.created_at = coalesce(e.created_at, $created_at)`,
				from, to, string(rel),
			)
			if err := r.cypherExec(ctx, q, params{
				"edge_id":    e.ID.String(),
				"weight":     e.Weight,
				"created_at": e.CreatedAt.Format(time.RFC3339),
			}); err != nil {
				return fmt.Errorf("create %s edge: %w", rel, err)
			}
		}
	}

	return nil
}

// --- Session ---

// CreateSession creates a :Session vertex and a :belongs_to edge linking it
// to the parent :Project vertex. This two-step operation ensures every session
// is connected to its project in the graph for traversal queries.
func (r *AGERepository) CreateSession(ctx context.Context, sess model.Session) error {
	const q = `CREATE (s:Session {id: $id, project_id: $project_id, agent_id: $agent_id, started_at: $started_at}) RETURN s`
	if err := r.cypherExec(ctx, q, params{
		"id":         sess.ID.String(),
		"project_id": sess.ProjectID,
		"agent_id":   sess.AgentID,
		"started_at": sess.StartedAt.Format(time.RFC3339),
	}); err != nil {
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
	const belongsQ = `MATCH (s:Session {id: $session_id}), (p:Project {id: $project_id}) ` +
		`CREATE (s)-[e:belongs_to {id: $edge_id, weight: 1.0, created_at: $created_at}]->(p) RETURN e`
	return r.cypherExec(ctx, belongsQ, params{
		"session_id": sess.ID.String(),
		"project_id": sess.ProjectID,
		"edge_id":    edge.ID.String(),
		"created_at": sess.StartedAt.Format(time.RFC3339),
	})
}

// EndSession sets the ended_at timestamp on a session node to the current
// UTC time and returns the updated session. The Cypher SET clause performs
// an in-place property update on the matched vertex.
func (r *AGERepository) EndSession(ctx context.Context, id uuid.UUID) (model.Session, error) {
	now := time.Now().UTC()
	const q = `MATCH (s:Session {id: $id}) SET s.ended_at = $ended_at RETURN properties(s)`
	rows, err := r.cypher(ctx, q, params{
		"id":       id.String(),
		"ended_at": now.Format(time.RFC3339),
	})
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
	const q = `MATCH (s:Session {id: $session_id}), (m:Memory {id: $memory_id}) ` +
		`CREATE (s)-[e:contains {id: $edge_id, weight: 1.0, created_at: $created_at}]->(m) RETURN e`
	return r.cypherExec(ctx, q, params{
		"session_id": sessionID.String(),
		"memory_id":  memoryID.String(),
		"edge_id":    uuid.New().String(),
		"created_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// --- Query ---

// QueryMemories builds a dynamic Cypher MATCH query from the given filter.
// Filter conditions are AND-ed: project_id, memory types, and keywords.
// Keywords use Cypher's toLower() + CONTAINS for case-insensitive substring
// matching against both content and tags (OR-ed within the keyword group).
// Results are ordered by created_at DESC and capped at filter.TopK (default 5).
// Each result gets a placeholder Score of 1.0; real scoring is deferred to
// the ranking layer.
// QueryMemories builds a dynamic Cypher MATCH query from the given filter.
// Filter conditions are AND-ed: project_id, memory types, and keywords.
// Keywords use Cypher's toLower() + CONTAINS for case-insensitive substring
// matching against both content and tags (OR-ed within the keyword group).
// Results are ordered by created_at DESC and capped at filter.TopK (default 5).
//
// Only the shape of the query varies with the filter; every filter value is
// bound as a parameter. The generated Cypher text therefore depends solely on
// how many keywords and types were supplied, never on their contents.
//
// Each result gets a placeholder Score of 1.0; real scoring is deferred to
// the ranking layer.
func (r *AGERepository) QueryMemories(ctx context.Context, filter QueryFilter) ([]model.MemoryWithContext, error) {
	var conditions []string
	p := params{}

	if filter.ProjectID != "" {
		conditions = append(conditions, `m.project_id = $project_id`)
		p["project_id"] = filter.ProjectID
	}

	if len(filter.Types) > 0 {
		types := make([]string, len(filter.Types))
		for i, t := range filter.Types {
			types[i] = string(t)
		}
		conditions = append(conditions, `m.type IN $types`)
		p["types"] = types
	}

	// Keyword search via CONTAINS on content and tags. Each keyword gets its
	// own numbered parameter; only the placeholder names are built into the
	// query text.
	if len(filter.Keywords) > 0 {
		var keywordConds []string
		for i, kw := range filter.Keywords {
			name := fmt.Sprintf("kw%d", i)
			keywordConds = append(keywordConds, fmt.Sprintf(
				`(toLower(m.content) CONTAINS $%s OR toLower(m.tags) CONTAINS $%s)`, name, name,
			))
			p[name] = strings.ToLower(kw)
		}
		conditions = append(conditions, "("+strings.Join(keywordConds, " OR ")+")")
	}

	topK := filter.TopK
	if topK <= 0 {
		topK = 5
	}

	// Fetch a candidate pool larger than topK, because this LIMIT is applied
	// before the ranking layer has seen anything.
	//
	// created_at has second precision, so a busy project produces large groups
	// of ties -- measured 153 memories sharing one timestamp under load. With
	// `ORDER BY created_at DESC LIMIT 5` over such a group, Postgres is free to
	// return any five, and the memory that actually matched the query could be
	// discarded before ranking ran. That showed up as a write not being
	// readable by its own keyword moments after being stored.
	//
	// Over-fetching lets ranking choose from a real pool. The cap keeps the
	// cost bounded: the filter has already narrowed to one project, and the
	// ranking layer truncates to topK immediately afterwards.
	candidateLimit := int32(topK)
	if filter.OverFetch {
		candidateLimit *= candidatePoolFactor
		if candidateLimit > maxCandidatePool {
			candidateLimit = maxCandidatePool
		}
	}
	p["top_k"] = candidateLimit

	q := `MATCH (m:Memory) RETURN properties(m) ORDER BY m.created_at DESC LIMIT $top_k`
	if len(conditions) > 0 {
		q = fmt.Sprintf(
			`MATCH (m:Memory) WHERE %s RETURN properties(m) ORDER BY m.created_at DESC LIMIT $top_k`,
			strings.Join(conditions, " AND "),
		)
	}

	rows, err := r.cypher(ctx, q, p)
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
	//
	// Two directed matches rather than one undirected `-[e]-`. AGE cannot drive
	// an undirected pattern from the edge indexes and falls back to scanning the
	// whole Memory label: 209ms at 50k vertices, versus 0.09ms for the directed
	// pair. Their union is exactly the undirected neighbour set.
	//
	// The neighbour carries the :Memory label because only that label holds the
	// property index, and Memory is the only kind of neighbour decoded here.
	// The center stays unlabeled on purpose: callers pass Session ids as well as
	// Memory ids, so constraining it to :Memory would silently return nothing
	// for a session subgraph.
	const outgoing = `MATCH (center)-[e]->(neighbor:Memory) WHERE center.id = $center_id RETURN properties(neighbor)`
	const incoming = `MATCH (center)<-[e]-(neighbor:Memory) WHERE center.id = $center_id RETURN properties(neighbor)`

	var props []memoryProps
	for _, q := range [...]string{outgoing, incoming} {
		rows, err := r.cypher(ctx, q, params{"center_id": centerID.String()})
		if err != nil {
			return nil, nil, fmt.Errorf("get subgraph: %w", err)
		}
		batch, err := scanAgtype[memoryProps](rows)
		if err != nil {
			return nil, nil, fmt.Errorf("get subgraph: %w", err)
		}
		props = append(props, batch...)
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
// endpoints of each relationship, so the reported direction is the stored one
// regardless of which side the traversal entered from.
//
// Like GetSubgraph, this runs as two directed matches instead of one undirected
// pattern: AGE cannot use the edge indexes for an undirected match and degrades
// into a full label scan. Measured at 50k vertices, 71.5ms undirected versus
// 0.05ms directed; re-checked at 94k vertices, 734ms versus 14ms. The gap grows
// with the graph, because the undirected form scans all of it. The dedup by
// edge id below also absorbs self-loops, which would otherwise be returned by
// both halves.
func (r *AGERepository) getEdgesAround(ctx context.Context, centerID uuid.UUID) ([]model.Edge, error) {
	const outgoing = `MATCH (center)-[e]->(other:Memory) WHERE center.id = $center_id ` +
		`RETURN {edge_id: e.id, from_id: startNode(e).id, to_id: endNode(e).id, ` +
		`relationship: label(e), weight: e.weight, created_at: e.created_at}`
	const incoming = `MATCH (center)<-[e]-(other:Memory) WHERE center.id = $center_id ` +
		`RETURN {edge_id: e.id, from_id: startNode(e).id, to_id: endNode(e).id, ` +
		`relationship: label(e), weight: e.weight, created_at: e.created_at}`

	var ers []edgeRow
	for _, q := range [...]string{outgoing, incoming} {
		rows, err := r.cypher(ctx, q, params{"center_id": centerID.String()})
		if err != nil {
			return nil, err
		}
		batch, err := scanAgtype[edgeRow](rows)
		if err != nil {
			return nil, err
		}
		ers = append(ers, batch...)
	}

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

	return edges, nil
}

// uuidLiteralList renders UUIDs as a Cypher list literal: ['a','b',...].
//
// Building query text from values is exactly what this repository stopped doing
// when it replaced escapeCypher with real parameters, so this needs to be
// justified rather than assumed:
//
//   - The input is []uuid.UUID, not a string. A uuid.UUID is 16 bytes and
//     String() renders it as 36 characters from [0-9a-f-]. There is no input,
//     malicious or otherwise, that produces a quote, a backslash, or a brace.
//     The type system is doing the escaping, which is stronger than escaping.
//   - The check below is belt and braces: if a future change makes this take
//     strings, the assumption fails loudly rather than silently reopening the
//     injection hole.
//
// It exists because the parameterized forms depend on planner statistics to
// reach the index, and the literal form does not.
//
// The original measurement, at 64,070 vertices with ten ids:
//
//	UNWIND + WHERE m.id = wanted   24.5ms   (Seq Scan over all 64,070)
//	WHERE m.id IN $ids             76.2ms   (Seq Scan)
//	WHERE m.id IN ['literal',...]   0.4ms   (Bitmap Index Scan)
//
// Re-measured later at 101,446 vertices with fresh statistics, the
// parameterized UNWIND *does* use the index (0.157ms), as does the literal form
// (0.219ms). So the original reading was not the whole story: what was actually
// being observed was stale statistics on the id expression index, not an
// absolute inability of AGE to use it.
//
// The literal form is kept because the difference reappears exactly when it
// hurts. With the statistics removed -- which is the state after a bulk import,
// a restore, or any window where autoanalyze has not caught up on a
// fast-growing table -- the two diverge sharply:
//
//	UNWIND over $ids              158.7ms   (Seq Scan over all 101,446)
//	WHERE m.id IN ['literal',...]   0.23ms  (Bitmap Index Scan)
//
// A literal list gives the planner a constant it can match against the
// expression index without consulting statistics at all, so this path degrades
// gracefully instead of falling off a cliff at the worst moment. That is worth
// the narrow, type-checked exception to parameterisation documented above.
func uuidLiteralList(ids []uuid.UUID) (string, error) {
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		s := id.String()
		if !isPlainUUID(s) {
			return "", fmt.Errorf("refusing to inline non-uuid id %q", s)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('\'')
		b.WriteString(s)
		b.WriteByte('\'')
	}
	b.WriteByte(']')
	return b.String(), nil
}

// isPlainUUID reports whether s consists only of hex digits and hyphens at the
// canonical length. Deliberately stricter than uuid.Parse: this is the guard
// that makes inlining safe, so it rejects anything it does not fully recognise.
func isPlainUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// GetContextEdges returns, for each of the given memory ids, the edges
// connecting it to its neighbors -- used to explain why a query result was
// returned. All ids are handled in one round trip per direction, so the cost
// does not grow with the number of memories being explained.
//
// Two details here are both counter-intuitive and both measured, so do not
// "simplify" either one without re-running EXPLAIN:
//
//  1. A literal `IN [...]` list, not a parameter and not UNWIND. The
//     parameterized forms reach the index only while planner statistics are
//     fresh, and collapse to a full label scan when they are not -- which is
//     the state after a bulk import or a restore. See uuidLiteralList for the
//     measurements and for why inlining these particular values is safe.
//  2. Two directed matches, not one undirected `-[e]-`. AGE cannot drive an
//     undirected pattern from the edge indexes and degrades to a full label
//     scan. The union of both directions is exactly the undirected set.
func (r *AGERepository) GetContextEdges(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]model.ContextEdge, error) {
	result := make(map[uuid.UUID][]model.ContextEdge)
	if len(ids) == 0 {
		return result, nil
	}

	list, err := uuidLiteralList(ids)
	if err != nil {
		return nil, err
	}

	// Outgoing then incoming. Both shapes return the neighbour as n, so the
	// decoding below is identical for each.
	outgoing := `MATCH (m:Memory)-[e]->(n:Memory) WHERE m.id IN ` + list +
		` RETURN {memory_id: m.id, relationship: label(e), weight: e.weight, ` +
		`target_id: n.id, target_content: n.content}`
	incoming := `MATCH (m:Memory)<-[e]-(n:Memory) WHERE m.id IN ` + list +
		` RETURN {memory_id: m.id, relationship: label(e), weight: e.weight, ` +
		`target_id: n.id, target_content: n.content}`

	for _, q := range [...]string{outgoing, incoming} {
		rows, err := r.cypher(ctx, q, nil)
		if err != nil {
			return nil, err
		}

		crs, err := scanAgtype[contextEdgeRow](rows)
		if err != nil {
			return nil, err
		}

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
	}

	return result, nil
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

	hits, err := r.nearestNeighbours(ctx, vecStr, projectID, topK)
	if err != nil {
		return nil, err
	}

	// Hydration runs after the similarity search has fully released its
	// connection. Doing it inline would hold two pool connections per call --
	// one for the search, one for the graph fetch -- and deadlock the pool as
	// soon as concurrency reached MaxConns.
	return r.hydrate(ctx, hits)
}

// nearestNeighbours runs the pgvector similarity search and returns the raw
// hits, holding at most one pool connection and releasing it before returning.
func (r *AGERepository) nearestNeighbours(ctx context.Context, vecStr, projectID string, topK int) ([]vectorHit, error) {
	if projectID == "" {
		const q = `SELECT memory_id, 1 - (embedding <=> $1::vector) AS similarity
				   FROM public.memory_embeddings
				   ORDER BY embedding <=> $1::vector
				   LIMIT $2`
		rows, err := r.pool.Query(ctx, q, vecStr, topK)
		if err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		return scanVectorHits(rows)
	}

	// A project filter is applied AFTER the HNSW index has already chosen its
	// candidates, so a scoped query can silently return far too few rows --
	// measured 0 of 250 matching memories on a 5k corpus with 20 projects.
	// hnsw.iterative_scan makes pgvector keep scanning until the limit is
	// satisfied, and strict_order preserves exact distance ordering so ranking
	// still receives correctly ordered similarities.
	//
	// SET LOCAL needs a transaction, which is also what confines the setting to
	// this query rather than leaking to the next user of the pooled connection.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("vector search: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = strict_order`); err != nil {
		// Older pgvector builds do not know the GUC. Recall degrades rather
		// than the query failing, so carry on.
		_ = err
	}

	// Raise the scan budget above the default 20,000 tuples.
	//
	// iterative_scan alone is not enough after churn. HNSW keeps entries for
	// deleted rows until VACUUM removes them, and the scan gives up once it has
	// examined max_scan_tuples -- so on a table that has seen many deletes, the
	// budget can be spent entirely on dead entries and live matches are never
	// reached. Observed as a query returning 1 of 2 rows that plainly matched,
	// intermittently (6 failures in 20 runs), and fixed immediately by VACUUM.
	//
	// A larger budget bounds how bad that gets between vacuums. It is a ceiling
	// on work, not a target: a query that finds its matches early still stops
	// early.
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.max_scan_tuples = 200000`); err != nil {
		_ = err
	}

	// A scoped search filters to the project first, then orders exactly within
	// it. The CTE is materialised precisely to stop the planner folding it back
	// into an HNSW scan with the filter applied afterwards.
	//
	// The inner LIMIT bounds what that materialisation costs. Without it, a
	// project holding 5,000 embeddings pulled 5,000 x 384 floats into memory on
	// every query and OOM-killed Postgres four times under a six-worker soak.
	// The cap is well above any plausible top_k, so ordering within it is the
	// same ordering an unbounded scan would produce for the rows that can
	// actually be returned.
	//
	// That post-filtering is a real recall failure, not a theoretical one:
	// reproduced at 40,000 embeddings across 500 projects, where a project
	// holding two matching rows returned one -- and, with stale statistics,
	// zero -- while a sequential scan returned both. Raising hnsw.ef_search to
	// 1000 and the scan budget tenfold did not help, because the budget is
	// spent on other projects' vectors before reaching this project's.
	//
	// Exact ordering within one project is also better than approximate: the
	// filtered set is small, so there is nothing to approximate away.
	const scopedQ = `WITH scoped AS MATERIALIZED (
					   SELECT memory_id, embedding
					   FROM public.memory_embeddings
					   WHERE project_id = $2
					   LIMIT ` + scopedVectorCandidates + `
					 )
					 SELECT memory_id, 1 - (embedding <=> $1::vector) AS similarity
					 FROM scoped
					 ORDER BY embedding <=> $1::vector
					 LIMIT $3`

	// Unscoped, there is nothing to filter, so the HNSW index is exactly right.
	const unscopedQ = `SELECT memory_id, 1 - (embedding <=> $1::vector) AS similarity
			   FROM public.memory_embeddings
			   WHERE $2 = ''
			   ORDER BY embedding <=> $1::vector
			   LIMIT $3`

	q := scopedQ
	if projectID == "" {
		q = unscopedQ
	}
	rows, err := tx.Query(ctx, q, vecStr, projectID, topK)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return scanVectorHits(rows)
}

// scopedVectorCandidates bounds how many of a project's embeddings a scoped
// search will materialise.
//
// Chosen against measured memory cost, not intuition: each embedding is 384
// float32s, so 20,000 of them is roughly 30MB per query -- affordable against
// the 1Gi Postgres limit even with several queries in flight, where the
// unbounded version OOM-killed the database.
//
// A project larger than this degrades to searching its 20,000 most recently
// inserted embeddings rather than all of them. That is a real limitation and it
// is stated rather than hidden; the alternative measured worse in both
// directions, either losing rows to HNSW post-filtering or losing the database.
const scopedVectorCandidates = "20000"

// vectorHit is one row from the pgvector similarity search, before the memory
// itself has been fetched from the graph.
type vectorHit struct {
	id         uuid.UUID
	similarity float64
}

// scanVectorHits reads similarity rows, discarding any with an unparseable id
// rather than failing the whole search for one bad row.
func scanVectorHits(rows pgx.Rows) ([]vectorHit, error) {
	defer rows.Close()

	var hits []vectorHit
	for rows.Next() {
		var memID string
		var similarity float64
		if err := rows.Scan(&memID, &similarity); err != nil {
			slog.Warn("vector search: dropping a row that failed to scan", slog.Any("error", err))
			continue
		}
		id, err := uuid.Parse(memID)
		if err != nil {
			slog.Warn("vector search: dropping a row with an unparseable id",
				slog.String("memory_id", memID), slog.Any("error", err))
			continue
		}
		hits = append(hits, vectorHit{id: id, similarity: similarity})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return hits, nil
}

// hydrate turns similarity hits into full memories with one graph query rather
// than one per hit.
//
// This used to call GetMemory in a loop, so a top_k of 20 issued up to 40
// separate round trips before the response could be written. Ordering by
// descending similarity is preserved: the batch fetch is indexed by id and the
// results are re-emitted in the order pgvector returned them.
func (r *AGERepository) hydrate(ctx context.Context, hits []vectorHit) ([]model.MemoryWithContext, error) {
	if len(hits) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(hits))
	for i, h := range hits {
		ids[i] = h.id
	}
	list, err := uuidLiteralList(ids)
	if err != nil {
		return nil, err
	}

	// Literal list, not UNWIND or a parameter: see uuidLiteralList.
	q := `MATCH (m:Memory) WHERE m.id IN ` + list + ` RETURN properties(m)`
	rows, err := r.cypher(ctx, q, nil)
	if err != nil {
		return nil, fmt.Errorf("hydrate vector hits: %w", err)
	}

	props, err := scanAgtype[memoryProps](rows)
	if err != nil {
		return nil, fmt.Errorf("hydrate vector hits: %w", err)
	}

	byID := make(map[uuid.UUID]model.Memory, len(props))
	for _, p := range props {
		mem := p.toModel()
		byID[mem.ID] = mem
	}

	results := make([]model.MemoryWithContext, 0, len(hits))
	for _, h := range hits {
		mem, ok := byID[h.id]
		if !ok {
			// An embedding whose memory vertex is not in the graph. Usually a
			// memory deleted while its embedding lingered, which is why this is
			// a skip rather than an error -- but it is also how a live memory
			// silently disappears from vector results if hydration fails, so it
			// is recorded rather than passed over in silence.
			slog.Warn("vector search: embedding has no matching memory; dropping it",
				slog.String("memory_id", h.id.String()))
			continue
		}
		results = append(results, model.MemoryWithContext{
			Memory: mem,
			Score:  h.similarity,
		})
	}

	return results, nil
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
	rows, err := r.cypher(ctx, query, nil)
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

// Ping verifies the backing database is reachable.
//
// This is the reachability half of a health check, kept separate from the count
// queries because it is cheap and must never be served from a cache, while the
// counts are expensive full scans and can be.
func (r *AGERepository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
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
