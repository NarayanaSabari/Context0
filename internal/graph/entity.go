package graph

// entity.go persists Entity nodes and the mentions edges connecting memories
// to them.
//
// The reason entities are nodes rather than tags is the second hop. A tag is a
// string on a memory, so finding what else mentions it means scanning for that
// string; a shared node is one edge away in either direction. Multi-hop
// questions were the weakest LoCoMo category at 65% because the graph had
// nothing like this to traverse -- `Caroline` was never connected to her dog
// `Biscuit`, only to other sentences worded like the one mentioning him.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// entityPropertyIndexes are the Entity vertex properties worth indexing.
//
// Same reasoning as memoryPropertyIndexes: AGE keeps every property inside one
// agtype column, so the index has to be on the extraction expression and only
// matches a query producing that identical expression. That is why the queries
// below consistently write `WHERE e.project_id = $x` rather than the map form
// `MATCH (e:Entity {project_id: $x})` -- the map form compiles to a
// `properties @> ...` containment check these btree indexes cannot serve.
var entityPropertyIndexes = []struct{ name, property string }{
	// The upsert path reads by this on every extracted memory.
	{"entity_normalized_name_idx", "normalized_name"},
	// The tenant boundary, and half of every entity lookup.
	{"entity_project_id_idx", "project_id"},
}

// initEntitySchema creates the Entity label and its indexes. Idempotent, and
// called from InitSchema on every startup.
func (r *AGERepository) initEntitySchema(ctx context.Context) error {
	// AGE creates a label's table lazily, on first write, so the indexes below
	// cannot be built without this on a fresh database -- and the deployment
	// would then run unindexed until something happened to restart it, which
	// is exactly while the corpus is growing and the scans hurt most.
	if _, err := r.pool.Exec(ctx,
		`SELECT ag_catalog.create_vlabel($1, 'Entity')`, GraphName,
	); err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("create Entity label: %w", err)
	}

	for _, idx := range entityPropertyIndexes {
		// The property name is a compile-time constant from the slice above,
		// never caller input, so interpolating it introduces no injection risk.
		stmt := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s."Entity" `+
				`(ag_catalog.agtype_access_operator(properties, '"%s"'::ag_catalog.agtype))`,
			idx.name, GraphName, idx.property,
		)
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("create index %s: %w", idx.name, err)
		}
	}

	// Expression indexes have no statistics until ANALYZE runs, and without
	// them the planner may still choose a sequential scan.
	if _, err := r.pool.Exec(ctx, fmt.Sprintf(`ANALYZE %s."Entity"`, GraphName)); err != nil {
		return fmt.Errorf("analyze Entity label: %w", err)
	}

	return nil
}

// LinkEntities attaches a memory to the entities it mentions, creating any
// entity node that does not exist yet, and reports how many edges were
// written.
//
// Identity is model.NormalizeEntity of the name, scoped to the project. That
// is what makes two memories mentioning `Biscuit` and `Biscuit's` reach one
// node instead of three, and it is why the write is a MERGE: the second memory
// to mention an entity must attach to the node the first one created, not make
// its own.
//
// Entities are scoped to the project because the project is the tenant
// boundary everywhere else in this engine. A shared entity node would make one
// tenant's memories reachable from another's in a single hop.
//
// # Concurrency
//
// AGE's MERGE is not safe against concurrent writers, and this is the hottest
// place in the engine for that to matter: a conversation's memories are
// written in a loop and several conversations about the same person arrive at
// once, so every writer races for the same node. Measured with 12 concurrent
// LinkEntities calls naming one entity: six failed with `Entity failed to be
// updated: 3 (SQLSTATE XX000)`, two duplicate `biscuit` nodes were created,
// and only 6 of 12 memories were reachable through the entity afterwards.
// That is the exact failure entities exist to prevent, arriving silently.
//
// Serialising on the entity itself is what fixes it. Each entity gets a
// transaction-scoped advisory lock keyed on (project, normalized name), so
// concurrent writers for the *same* entity queue while writers for different
// entities do not contend at all. The lock is held for one short statement and
// released when the transaction ends, including on error.
//
// Retries cover the residual: two writers can still collide inside AGE's own
// vertex update path before either takes the lock, and that surfaces as the
// XX000 above rather than as a serialisation failure Postgres would retry
// itself.
//
// One statement per entity, not a batched UNWIND. Same measurement as
// CreateEdges: AGE cannot use the property index for `WHERE e.name = row.name`
// inside an UNWIND and plans a sequential scan of the whole label per row,
// which costs far more than the round trips it saves. The count is bounded by
// maxEntitiesPerMemory, so this is a handful of ~0.3ms statements.
func (r *AGERepository) LinkEntities(ctx context.Context, mem model.Memory, names []string) (int, error) {
	if len(names) == 0 || mem.ProjectID == "" {
		return 0, nil
	}

	memList, err := uuidLiteralList([]uuid.UUID{mem.ID})
	if err != nil {
		return 0, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	linked := 0
	seen := make(map[string]bool, len(names))

	for _, name := range names {
		normalized := model.NormalizeEntity(name)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true

		if err := r.linkOneEntity(ctx, mem, memList, normalized, strings.TrimSpace(name), now); err != nil {
			return linked, err
		}
		linked++
	}

	return linked, nil
}

// entityLinkAttempts is how many times a single entity link is retried through
// AGE's concurrent-update error.
//
// Three, because the advisory lock already serialises the common case and this
// only covers the window before it is taken. A higher number would trade
// latency on a genuinely broken database for no additional safety.
const entityLinkAttempts = 3

// linkOneEntity upserts one entity and its mentions edge, serialised against
// other writers for the same entity.
func (r *AGERepository) linkOneEntity(ctx context.Context, mem model.Memory, memList, normalized, display, now string) error {
	var lastErr error
	for attempt := 0; attempt < entityLinkAttempts; attempt++ {
		err := r.linkOneEntityOnce(ctx, mem, memList, normalized, display, now)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isConcurrentUpdate(err) {
			return fmt.Errorf("link entity %q: %w", normalized, err)
		}
	}
	return fmt.Errorf("link entity %q after %d attempts: %w", normalized, entityLinkAttempts, lastErr)
}

// linkOneEntityOnce runs the upsert in a transaction holding an advisory lock
// on the entity's identity.
func (r *AGERepository) linkOneEntityOnce(ctx context.Context, mem model.Memory, memList, normalized, display, now string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback is a no-op after a successful commit, and on any error path it
	// is what releases the advisory lock.
	defer func() { _ = tx.Rollback(ctx) }()

	// Transaction-scoped, so it cannot leak to the next user of this pooled
	// connection the way a session-level lock would.
	//
	// The two-argument form takes the project and the name as separate keys
	// rather than hashing a concatenation. Concatenating needs a separator
	// that cannot appear in either half, and the obvious one -- a NUL byte --
	// is not valid in Postgres text at all ("invalid byte sequence for
	// encoding UTF8"). Any printable separator is a character a project id or
	// an entity name could contain, which would let two different entities
	// share a lock.
	//
	// Writers for different entities therefore take different locks and never
	// queue behind each other; only writers for the same entity serialise.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
		mem.ProjectID, normalized,
	); err != nil {
		return err
	}

	// MERGE on the identity properties alone, then SET the rest with coalesce,
	// so the first writer's id and display name survive and every later
	// mention attaches rather than replacing. Same first-writer-wins shape as
	// CreateEdge.
	//
	// The memory endpoint is matched by literal id for the reason on
	// uuidLiteralList: the parameterized form reaches the index only while
	// planner statistics are fresh, and this runs during bulk ingest, which is
	// exactly when they are not.
	//
	// The memory's own project is checked too, not just the entity's. The id
	// and the project arrive in the same model.Memory value but from different
	// places -- the id from the graph, the project from the request -- so a
	// caller passing a memory id from one project with another project's id
	// would otherwise attach a project-B entity to a project-A memory. The
	// read path filters both sides, so that is silent graph corruption rather
	// than a leak, which makes it the kind that survives.
	q := fmt.Sprintf(
		`MATCH (m:Memory) WHERE m.id IN %s AND m.project_id = $project_id `+
			`MERGE (e:Entity {normalized_name: $normalized_name, project_id: $project_id}) `+
			`SET e.id = coalesce(e.id, $entity_id), e.name = coalesce(e.name, $name) `+
			`MERGE (m)-[r:%s]->(e) `+
			`SET r.id = coalesce(r.id, $edge_id), r.weight = coalesce(r.weight, 1.0), `+
			`r.created_at = coalesce(r.created_at, $created_at)`,
		memList, string(model.RelMentions),
	)

	encoded, ok, err := encodeParams(params{
		"normalized_name": normalized,
		"project_id":      mem.ProjectID,
		"entity_id":       uuid.New().String(),
		"name":            display,
		"edge_id":         uuid.New().String(),
		"created_at":      now,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, cypherSQL(q, ok), encoded); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// isConcurrentUpdate reports whether an error is AGE losing a race to update a
// vertex another transaction is touching.
//
// Matched on the message because AGE reports it as XX000 (internal_error),
// which is far too broad to retry on alone. The text is stable across AGE's
// releases and is the only signal it gives: it is raised from
// update_entity_tuple when the heap update returns anything other than
// TM_Ok, and carries the TM_Result as a bare integer.
func isConcurrentUpdate(err error) bool {
	return err != nil && strings.Contains(err.Error(), "failed to be updated")
}

// FindMemoriesByEntities returns the memories in a project that mention any of
// the given entity names, keyed by memory id.
//
// This is the hop. A query naming `Biscuit` reaches every memory about him in
// one traversal, however differently each is worded, which is what embedding
// similarity could not do: it clusters memories that resemble each other, and
// two facts about the same dog need not resemble each other at all.
//
// One statement for every name rather than a query each, because retrieval
// runs this on the read path where a round trip per query term would be paid
// on every request.
//
// limit bounds the result because an entity mentioned by most of a project --
// the speaker's own name, typically -- would otherwise return the whole
// corpus. Ordered by recency so the bound keeps the freshest mentions rather
// than an arbitrary page.
func (r *AGERepository) FindMemoriesByEntities(ctx context.Context, projectID string, names []string, limit int) ([]model.Memory, error) {
	if len(names) == 0 || limit <= 0 {
		return nil, nil
	}

	normalized := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		key := model.NormalizeEntity(n)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return nil, nil
	}

	// Every value is a bound parameter; only the number of placeholders varies
	// with the input, so the generated Cypher depends on how many names were
	// supplied and never on their contents.
	p := params{"project_id": projectID, "limit": limit}
	placeholders := make([]string, len(normalized))
	for i, n := range normalized {
		name := fmt.Sprintf("en%d", i)
		placeholders[i] = "$" + name
		p[name] = n
	}

	// The entity endpoint carries its label so the planner can use the
	// normalized_name index; an unlabeled match scans every vertex table. Both
	// project filters are present because the memory and the entity are scoped
	// independently, and relying on the edge to imply the memory's project
	// would trust an edge this query is itself traversing.
	//
	// DISTINCT runs before the LIMIT, which matters more than it looks. A
	// memory naming two of the query's entities matches the pattern twice, so
	// without it that memory consumes two of the limit's slots and pushes a
	// different memory out entirely -- and it is precisely the memories that
	// match the query best that match it more than once, so the rows lost are
	// disproportionately the good ones. Deduplicating in Go afterwards cannot
	// recover them: they were discarded by the database.
	//
	// created_at is projected alongside m rather than only sorted on. AGE
	// compiles DISTINCT to SQL's SELECT DISTINCT, which requires every ORDER BY
	// expression to appear in the select list ("for SELECT DISTINCT, ORDER BY
	// expressions must appear in select list", SQLSTATE 42P10). It is dropped
	// again by the final RETURN.
	q := fmt.Sprintf(
		`MATCH (m:Memory)-[:%s]->(e:Entity) `+
			`WHERE e.project_id = $project_id AND m.project_id = $project_id `+
			`AND e.normalized_name IN [%s] `+
			`WITH DISTINCT m, m.created_at AS created_at ORDER BY created_at DESC LIMIT $limit `+
			`RETURN properties(m)`,
		string(model.RelMentions), strings.Join(placeholders, ","),
	)

	rows, err := r.cypher(ctx, q, p)
	if err != nil {
		return nil, fmt.Errorf("find memories by entities: %w", err)
	}

	props, err := scanAgtype[memoryProps](rows)
	if err != nil {
		return nil, fmt.Errorf("scan entity matches: %w", err)
	}

	out := make([]model.Memory, 0, len(props))
	unique := make(map[uuid.UUID]bool, len(props))
	for _, pr := range props {
		mem := pr.toModel()
		// Belt and braces behind the DISTINCT above: a duplicate reaching the
		// caller would consume a candidate slot in ranking, which is the same
		// cost in a different place.
		if unique[mem.ID] {
			continue
		}
		unique[mem.ID] = true
		out = append(out, mem)
	}
	return out, nil
}

// CountEntityMatches reports, for each of the given memories, how many of
// the given normalized entity names it mentions.
//
// This is the ranking path's question -- "how much of the query's entity set
// does this candidate name?" -- asked of the database directly. It replaces
// loading every entity of every candidate (GetMemoryEntities) and counting
// the overlap in Go, which the read-path profile put at 38% of all
// allocations and 4.7 ms of server time per query: with the speaker's name
// in most memories, a 500-candidate pool carried about 1,500 mentions edges,
// each decoded from an agtype object, to answer a question about one or two
// names.
//
// The count is over distinct names, so a memory mentioning the same entity
// twice counts it once, matching ranking.EntityOverlap. Memories that match
// nothing are absent from the map, which reads as zero.
func (r *AGERepository) CountEntityMatches(ctx context.Context, ids []uuid.UUID, names []string) (map[uuid.UUID]int, error) {
	result := make(map[uuid.UUID]int)
	if len(ids) == 0 || len(names) == 0 {
		return result, nil
	}

	list, err := uuidLiteralList(ids)
	if err != nil {
		return nil, err
	}

	p := params{}
	placeholders := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		key := model.NormalizeEntity(n)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		name := fmt.Sprintf("en%d", len(placeholders))
		placeholders = append(placeholders, "$"+name)
		p[name] = key
	}
	if len(placeholders) == 0 {
		return result, nil
	}

	// The ids are a literal list for the planner's sake (see uuidLiteralList)
	// and the names are parameters; the two mix freely because the list
	// contains nothing caller-supplied beyond validated UUIDs. The WITH
	// carries the aggregate into a single-column map, which is the shape the
	// cypher() wrapper returns.
	q := fmt.Sprintf(
		`MATCH (m:Memory)-[:%s]->(e:Entity) WHERE m.id IN %s `+
			`AND e.normalized_name IN [%s] `+
			`WITH m.id AS memory_id, count(DISTINCT e.normalized_name) AS n `+
			`RETURN {memory_id: memory_id, n: n}`,
		string(model.RelMentions), list, strings.Join(placeholders, ","),
	)

	rows, err := r.cypher(ctx, q, p)
	if err != nil {
		return nil, fmt.Errorf("count entity matches: %w", err)
	}

	type row struct {
		MemoryID string `json:"memory_id"`
		N        int    `json:"n"`
	}
	rs, err := scanAgtype[row](rows)
	if err != nil {
		return nil, fmt.Errorf("scan entity match counts: %w", err)
	}
	for _, rr := range rs {
		id, perr := uuid.Parse(rr.MemoryID)
		if perr != nil || rr.N <= 0 {
			continue
		}
		result[id] = rr.N
	}
	return result, nil
}

// GetMemoryEntities returns the normalized entity names each of the given
// memories mentions.
//
// Read on the ranking path, so it takes every memory at once: a lookup per
// result would put a round trip per returned memory on every query, which is
// the cost GetContextEdges and IncrementAccessCounts were both batched to
// avoid.
func (r *AGERepository) GetMemoryEntities(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]string, error) {
	result := make(map[uuid.UUID][]string)
	if len(ids) == 0 {
		return result, nil
	}

	list, err := uuidLiteralList(ids)
	if err != nil {
		return nil, err
	}

	q := fmt.Sprintf(
		`MATCH (m:Memory)-[:%s]->(e:Entity) WHERE m.id IN %s `+
			`RETURN {memory_id: m.id, name: e.normalized_name}`,
		string(model.RelMentions), list,
	)

	rows, err := r.cypher(ctx, q, nil)
	if err != nil {
		return nil, fmt.Errorf("get memory entities: %w", err)
	}

	type row struct {
		MemoryID string `json:"memory_id"`
		Name     string `json:"name"`
	}
	rs, err := scanAgtype[row](rows)
	if err != nil {
		return nil, fmt.Errorf("scan memory entities: %w", err)
	}

	for _, rr := range rs {
		id, perr := uuid.Parse(rr.MemoryID)
		if perr != nil || rr.Name == "" {
			continue
		}
		result[id] = append(result[id], rr.Name)
	}
	return result, nil
}
