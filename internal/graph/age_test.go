// Package graph's integration tests exercise AGERepository against a real
// PostgreSQL + Apache AGE + pgvector instance. Nothing here is mocked: the
// value of these tests is that they run the actual Cypher this package emits
// through the actual parser, which is the only way to catch a query that is
// syntactically valid Go but malformed openCypher.
//
// The suite is skipped unless CONTEXT0_TEST_DATABASE_URL is set, so the default
// `go test ./...` stays hermetic:
//
//	docker compose up -d postgres
//	CONTEXT0_TEST_DATABASE_URL="postgres://context0:context0@localhost:5432/context0?sslmode=disable" \
//	  go test ./internal/graph/...
//
// Every test scopes its data to a unique project id and cleans up after itself,
// so the suite is safe to run repeatedly against a persistent database.
package graph

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/context0/context0/pkg/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testEmbeddingDim must match the width of the memory_embeddings column that
// the server creates by default, since the suite shares one database with it.
const testEmbeddingDim = 384

// testRepo connects to the test database and returns a repository with the
// schema initialized. It skips the test when no database is configured.
//
// Connecting is retried briefly. A container that has just started accepts TCP
// connections before it is ready to serve queries, and CI starts PostgreSQL
// moments before this runs, so a single attempt turns a cold start into a
// spurious failure.
func testRepo(t *testing.T) (*AGERepository, context.Context) {
	t.Helper()

	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set; skipping AGE integration tests")
	}

	ctx := context.Background()

	var pool *pgxpool.Pool
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		if pool, err = NewPool(ctx, dsn); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("connect to test database after retries: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := NewAGERepository(pool, testEmbeddingDim)
	initSchemaOnce(t, repo, ctx)

	return repo, ctx
}

// schemaOnce guards InitSchema so it runs once per test binary rather than
// once per test.
//
// InitSchema is startup code: it creates indexes and then ANALYZEs the Memory
// label so the expression indexes have statistics. That is correct at startup
// and expensive to repeat -- 182ms per call against this database's 188k
// vertices, times the 33 tests that build a repo. Against a shared database
// that turned into sustained background load, and the concurrency tests in
// internal/service read it as their own pool serialising: 14.4s for a batch
// that takes 1.2s when nothing else is running.
//
// Tests that specifically exercise InitSchema still call it directly.
var schemaOnce sync.Once

func initSchemaOnce(t *testing.T, repo *AGERepository, ctx context.Context) {
	t.Helper()
	var initErr error
	schemaOnce.Do(func() { initErr = repo.InitSchema(ctx) })
	if initErr != nil {
		t.Fatalf("init schema: %v", initErr)
	}
}

// newProjectID returns a project id unique to this test run so concurrent or
// repeated runs cannot observe each other's memories.
func newProjectID(t *testing.T) string {
	t.Helper()
	return "test-" + uuid.NewString()
}

// storeMemory creates a memory and registers its deletion for cleanup.
func storeMemory(t *testing.T, repo *AGERepository, ctx context.Context, mem model.Memory) model.Memory {
	t.Helper()
	if err := repo.CreateMemory(ctx, mem); err != nil {
		t.Fatalf("create memory: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.DeleteMemory(context.Background(), mem.ID)
	})
	return mem
}

// newMemory builds a valid memory with sensible defaults for the given project.
func newMemory(projectID, content string, tags ...string) model.Memory {
	return model.Memory{
		ID:         uuid.New(),
		Content:    content,
		Type:       model.MemoryTypeSemantic,
		ProjectID:  projectID,
		Tags:       tags,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
		DecayScore: 1.0,
	}
}

func TestCreateAndGetMemory(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	want := newMemory(projectID, "Project uses PostgreSQL 18", "database", "postgres")
	want.Type = model.MemoryTypeProcedural
	storeMemory(t, repo, ctx, want)

	got, err := repo.GetMemory(ctx, want.ID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}

	if got.ID != want.ID {
		t.Errorf("ID = %s, want %s", got.ID, want.ID)
	}
	if got.Content != want.Content {
		t.Errorf("Content = %q, want %q", got.Content, want.Content)
	}
	if got.Type != want.Type {
		t.Errorf("Type = %q, want %q", got.Type, want.Type)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if len(got.Tags) != len(want.Tags) {
		t.Errorf("Tags = %v, want %v", got.Tags, want.Tags)
	}
	if got.AccessCount != 0 {
		t.Errorf("AccessCount = %d, want 0 on a new memory", got.AccessCount)
	}
	if got.DecayScore != 1.0 {
		t.Errorf("DecayScore = %f, want 1.0 on a new memory", got.DecayScore)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
}

func TestGetMemory_NotFound(t *testing.T) {
	repo, ctx := testRepo(t)

	if _, err := repo.GetMemory(ctx, uuid.New()); err == nil {
		t.Error("expected an error for a memory that does not exist")
	}
}

// TestCreateMemory_HostileContent is the regression test for the Cypher
// injection hole.
//
// The old code interpolated content into the query text and defended it by
// replacing ' with \'. That escaping is not sound -- it inverts on a backslash,
// since content ending in \ turns the code's own closing quote into an escaped
// one -- and the payloads below are the values that broke it.
//
// The observed impact against the pre-fix code was denial and corruption
// rather than executed Cypher: six of these payloads made the generated query
// unparseable, so a legitimate memory simply could not be stored. Attempts to
// get an appended statement to run were not successful against AGE's parser
// (see TestCreateMemory_InjectedCypherDoesNotExecute, which asserts that
// directly). Either way the property is the same and worth pinning: content is
// data, and must round-trip byte for byte without touching the graph.
func TestCreateMemory_HostileContent(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// A canary the injection payloads would delete if they were executed.
	canary := storeMemory(t, repo, ctx, newMemory(projectID, "canary memory that must survive"))

	payloads := []struct {
		name    string
		content string
	}{
		{"single quote", "it's a memory"},
		{"escaped quote breakout", `x'}) DETACH DELETE (m) //`},
		{"trailing backslash", `path\`},
		{"backslash before quote", `C:\dir\' RETURN 1 //`},
		{"double quotes", `he said "hello"`},
		{"newlines", "line one\nline two"},
		{"cypher keywords", "MATCH (n) DETACH DELETE n"},
		{"dollar quoting", "$$ RETURN 1 $$"},
		// Closes the property map and appends a second statement, keeping the
		// quote count balanced so the whole thing still parses.
		{"map escape with balanced quotes",
			`x', decay_score: 1.0}) CREATE (z:Memory {id: 'INJECTED', content: 'PWNED'`},
		{"parameter lookalike", "$id and $content"},
		{"unicode and emoji", "café 日本語 🎉"},
		{"null-ish literal", `\u0000 not really null`},
	}

	for _, p := range payloads {
		t.Run(p.name, func(t *testing.T) {
			mem := newMemory(projectID, p.content, p.content)
			storeMemory(t, repo, ctx, mem)

			got, err := repo.GetMemory(ctx, mem.ID)
			if err != nil {
				t.Fatalf("get memory: %v", err)
			}
			if got.Content != p.content {
				t.Errorf("content round-trip failed:\n got %q\nwant %q", got.Content, p.content)
			}
			if len(got.Tags) != 1 || got.Tags[0] != p.content {
				t.Errorf("tag round-trip failed: got %v, want [%q]", got.Tags, p.content)
			}
		})
	}

	// The canary proves no payload executed as Cypher.
	if _, err := repo.GetMemory(ctx, canary.ID); err != nil {
		t.Fatalf("canary memory was destroyed, injection payload executed: %v", err)
	}
}

// TestCreateMemory_InjectedCypherDoesNotExecute asserts the property that
// actually matters for an injection fix: no attacker-supplied content can add,
// delete, or rewrite a node it does not own.
//
// The old code emitted:
//
//	CREATE (m:Memory {id: '...', content: 'CONTENT', type: '...', ...}) RETURN m
//
// so content that closes the property map and re-balances its quotes produces
// a statement that parses. Against AGE these attempts did not execute -- the
// appended clause landed inside a later string literal rather than as syntax --
// but that outcome depended on the exact shape of the surrounding query, which
// is a property no one should have to re-derive after every edit to it.
//
// The check is node count rather than a surviving canary, because this payload
// class adds nodes; a survivor-style assertion would not notice an extra one.
//
// The count is scoped to this test's project. A global MATCH (n) count made the
// assertion depend on nothing else in the suite writing to the same database,
// so it failed spuriously as soon as another package's tests ran concurrently
// against the shared instance. The injected payloads all target the Memory
// label in this project, so a project-scoped count still detects every one of
// them while staying immune to unrelated traffic.
func TestCreateMemory_InjectedCypherDoesNotExecute(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	countProjectNodes := func() int64 {
		t.Helper()
		rows, err := repo.cypher(ctx,
			`MATCH (n:Memory {project_id: $pid}) RETURN count(n)`,
			params{"pid": projectID})
		if err != nil {
			t.Fatalf("count project nodes: %v", err)
		}
		n, found, err := scanOne[int64](rows)
		if err != nil || !found {
			t.Fatalf("scan count: err=%v found=%v", err, found)
		}
		return n
	}

	// An injected CREATE lands outside this project, so also watch for any
	// node carrying the injected marker anywhere in the graph.
	countInjectedNodes := func() int64 {
		t.Helper()
		rows, err := repo.cypher(ctx,
			`MATCH (n) WHERE n.id = 'INJECTED' RETURN count(n)`, params{})
		if err != nil {
			t.Fatalf("count injected nodes: %v", err)
		}
		n, found, err := scanOne[int64](rows)
		if err != nil || !found {
			t.Fatalf("scan injected count: err=%v found=%v", err, found)
		}
		return n
	}

	before := countProjectNodes()

	payloads := []string{
		// Append a second CREATE.
		`x', decay_score: 1.0}) CREATE (z:Memory {id: 'INJECTED', content: 'PWNED'`,
		// Append a mass delete.
		`y', decay_score: 1.0}) WITH 1 AS n MATCH (v:Memory) DETACH DELETE v RETURN count(v`,
		// Append a property overwrite across the whole label.
		`z', decay_score: 1.0}) WITH 1 AS n MATCH (v:Memory) SET v.content = 'CORRUPTED' RETURN count(v`,
	}

	for _, p := range payloads {
		mem := newMemory(projectID, p, "injection")
		storeMemory(t, repo, ctx, mem)

		got, err := repo.GetMemory(ctx, mem.ID)
		if err != nil {
			t.Fatalf("stored memory unreadable: %v", err)
		}
		if got.Content != p {
			t.Errorf("payload was not stored verbatim:\n got %q\nwant %q", got.Content, p)
		}
	}

	after := countProjectNodes()

	// Exactly one node per stored memory: no injected extras, nothing deleted.
	if delta := after - before; delta != int64(len(payloads)) {
		t.Errorf("node count changed by %d, want %d -- injected Cypher executed",
			delta, len(payloads))
	}

	// The CREATE payload builds a node with id 'INJECTED' and no project_id,
	// which a project-scoped count alone would miss.
	if n := countInjectedNodes(); n != 0 {
		t.Errorf("found %d node(s) with id 'INJECTED' -- injected CREATE executed", n)
	}

	// And nothing was mass-overwritten.
	all, err := repo.QueryMemories(ctx, QueryFilter{ProjectID: projectID, TopK: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(all) != len(payloads) {
		t.Errorf("project holds %d memories, want %d", len(all), len(payloads))
	}
	for _, r := range all {
		if r.Memory.Content == "CORRUPTED" || r.Memory.Content == "PWNED" {
			t.Errorf("injected Cypher executed: found memory with content %q", r.Memory.Content)
		}
	}
}

// TestQueryMemories_HostileFilters checks the other half of the injection
// surface: values that reach the query through a filter rather than through
// stored content.
func TestQueryMemories_HostileFilters(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	canary := storeMemory(t, repo, ctx, newMemory(projectID, "postgres is the database"))

	hostile := []string{
		`' OR true OR '`,
		`'}) DETACH DELETE (m) //`,
		`x' RETURN properties(m) //`,
		`\`,
	}

	for _, h := range hostile {
		t.Run(h, func(t *testing.T) {
			// A hostile keyword must simply match nothing, not alter the query.
			results, err := repo.QueryMemories(ctx, QueryFilter{
				ProjectID: projectID,
				Keywords:  []string{h},
				TopK:      10,
			})
			if err != nil {
				t.Fatalf("query with hostile keyword failed: %v", err)
			}
			if len(results) != 0 {
				t.Errorf("hostile keyword %q matched %d memories, want 0", h, len(results))
			}

			// A hostile project id must scope to nothing.
			results, err = repo.QueryMemories(ctx, QueryFilter{ProjectID: h, TopK: 10})
			if err != nil {
				t.Fatalf("query with hostile project id failed: %v", err)
			}
			if len(results) != 0 {
				t.Errorf("hostile project id %q matched %d memories, want 0", h, len(results))
			}
		})
	}

	if _, err := repo.GetMemory(ctx, canary.ID); err != nil {
		t.Fatalf("canary memory was destroyed by a hostile filter: %v", err)
	}
}

func TestQueryMemories_Filters(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)
	otherProject := newProjectID(t)

	semantic := newMemory(projectID, "the database is postgresql", "database")
	episodic := newMemory(projectID, "we switched to kubernetes last week", "infra")
	episodic.Type = model.MemoryTypeEpisodic
	foreign := newMemory(otherProject, "the database is mysql", "database")

	storeMemory(t, repo, ctx, semantic)
	storeMemory(t, repo, ctx, episodic)
	storeMemory(t, repo, ctx, foreign)

	t.Run("scopes to project", func(t *testing.T) {
		got, err := repo.QueryMemories(ctx, QueryFilter{ProjectID: projectID, TopK: 10})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d memories, want 2 scoped to the project", len(got))
		}
		for _, r := range got {
			if r.Memory.ProjectID != projectID {
				t.Errorf("leaked memory from project %q", r.Memory.ProjectID)
			}
		}
	})

	t.Run("filters by type", func(t *testing.T) {
		got, err := repo.QueryMemories(ctx, QueryFilter{
			ProjectID: projectID,
			Types:     []model.MemoryType{model.MemoryTypeEpisodic},
			TopK:      10,
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(got) != 1 || got[0].Memory.ID != episodic.ID {
			t.Fatalf("type filter returned %d memories, want just the episodic one", len(got))
		}
	})

	t.Run("matches keyword in content", func(t *testing.T) {
		got, err := repo.QueryMemories(ctx, QueryFilter{
			ProjectID: projectID,
			Keywords:  []string{"postgresql"},
			TopK:      10,
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(got) != 1 || got[0].Memory.ID != semantic.ID {
			t.Fatalf("keyword filter returned %d memories, want just the postgresql one", len(got))
		}
	})

	t.Run("matches keyword in tags", func(t *testing.T) {
		got, err := repo.QueryMemories(ctx, QueryFilter{
			ProjectID: projectID,
			Keywords:  []string{"infra"},
			TopK:      10,
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(got) != 1 || got[0].Memory.ID != episodic.ID {
			t.Fatalf("tag filter returned %d memories, want just the infra-tagged one", len(got))
		}
	})

	t.Run("keyword matching is case insensitive", func(t *testing.T) {
		got, err := repo.QueryMemories(ctx, QueryFilter{
			ProjectID: projectID,
			Keywords:  []string{"POSTGRESQL"},
			TopK:      10,
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("uppercase keyword returned %d memories, want 1", len(got))
		}
	})

	t.Run("respects top-k", func(t *testing.T) {
		got, err := repo.QueryMemories(ctx, QueryFilter{ProjectID: projectID, TopK: 1})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d memories, want the limit of 1", len(got))
		}
	})
}

func TestIncrementAccessCountAndDecay(t *testing.T) {
	repo, ctx := testRepo(t)
	mem := storeMemory(t, repo, ctx, newMemory(newProjectID(t), "a memory to read repeatedly"))

	for i := 0; i < 3; i++ {
		if err := repo.IncrementAccessCount(ctx, mem.ID); err != nil {
			t.Fatalf("increment access count: %v", err)
		}
	}

	got, err := repo.GetMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if got.AccessCount != 3 {
		t.Errorf("AccessCount = %d, want 3", got.AccessCount)
	}

	if err := repo.UpdateDecayScore(ctx, mem.ID, 0.25); err != nil {
		t.Fatalf("update decay score: %v", err)
	}
	got, err = repo.GetMemory(ctx, mem.ID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if got.DecayScore != 0.25 {
		t.Errorf("DecayScore = %f, want 0.25", got.DecayScore)
	}
}

func TestDeleteMemory(t *testing.T) {
	repo, ctx := testRepo(t)
	mem := storeMemory(t, repo, ctx, newMemory(newProjectID(t), "a short-lived memory"))

	if err := repo.DeleteMemory(ctx, mem.ID); err != nil {
		t.Fatalf("delete memory: %v", err)
	}
	if _, err := repo.GetMemory(ctx, mem.ID); err == nil {
		t.Error("memory still readable after deletion")
	}
}

func TestCreateEdge(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	from := storeMemory(t, repo, ctx, newMemory(projectID, "we now use postgresql"))
	to := storeMemory(t, repo, ctx, newMemory(projectID, "we use mysql"))

	edge := model.Edge{
		ID:           uuid.New(),
		FromID:       from.ID,
		ToID:         to.ID,
		Relationship: model.RelSupersedes,
		Weight:       0.9,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
	}

	got, err := repo.CreateEdge(ctx, edge)
	if err != nil {
		t.Fatalf("create edge: %v", err)
	}
	if got.ID != edge.ID {
		t.Errorf("edge ID = %s, want %s", got.ID, edge.ID)
	}
	if got.Weight != edge.Weight {
		t.Errorf("edge weight = %f, want %f", got.Weight, edge.Weight)
	}

	// Re-asserting the same triple must be idempotent and preserve the
	// original properties rather than creating a duplicate edge.
	second := edge
	second.ID = uuid.New()
	second.Weight = 0.1

	reasserted, err := repo.CreateEdge(ctx, second)
	if err != nil {
		t.Fatalf("re-assert edge: %v", err)
	}
	if reasserted.ID != edge.ID {
		t.Errorf("re-assert returned edge ID %s, want the original %s", reasserted.ID, edge.ID)
	}
	if reasserted.Weight != edge.Weight {
		t.Errorf("re-assert returned weight %f, want the original %f", reasserted.Weight, edge.Weight)
	}

	_, edges, err := repo.GetSubgraph(ctx, from.ID)
	if err != nil {
		t.Fatalf("get subgraph: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("got %d edges after re-asserting the same triple, want 1", len(edges))
	}
}

// TestCreateEdge_RejectsUnknownRelationship guards the one value that cannot be
// parameterized. An unvalidated label would be interpolated straight into the
// Cypher text.
func TestCreateEdge_RejectsUnknownRelationship(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	from := storeMemory(t, repo, ctx, newMemory(projectID, "source memory"))
	to := storeMemory(t, repo, ctx, newMemory(projectID, "target memory"))

	hostile := []string{
		`relates_to]->(b) DETACH DELETE (b) //`,
		`not_a_real_relationship`,
		``,
	}

	for _, rel := range hostile {
		t.Run(rel, func(t *testing.T) {
			_, err := repo.CreateEdge(ctx, model.Edge{
				ID:           uuid.New(),
				FromID:       from.ID,
				ToID:         to.ID,
				Relationship: model.RelationshipType(rel),
				Weight:       1.0,
				CreatedAt:    time.Now().UTC(),
			})
			if err == nil {
				t.Errorf("CreateEdge accepted unknown relationship %q", rel)
			}
		})
	}

	// Both endpoints must still exist.
	if _, err := repo.GetMemory(ctx, to.ID); err != nil {
		t.Fatalf("target memory was destroyed by a hostile relationship label: %v", err)
	}
}

func TestGetSubgraphAndContextEdges(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	center := storeMemory(t, repo, ctx, newMemory(projectID, "central memory"))
	neighbor := storeMemory(t, repo, ctx, newMemory(projectID, "neighboring memory"))

	if _, err := repo.CreateEdge(ctx, model.Edge{
		ID:           uuid.New(),
		FromID:       center.ID,
		ToID:         neighbor.ID,
		Relationship: model.RelRelatesTo,
		Weight:       0.5,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("create edge: %v", err)
	}

	nodes, edges, err := repo.GetSubgraph(ctx, center.ID)
	if err != nil {
		t.Fatalf("get subgraph: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != neighbor.ID {
		t.Fatalf("subgraph returned %d neighbors, want just the one", len(nodes))
	}
	if len(edges) != 1 {
		t.Fatalf("subgraph returned %d edges, want 1", len(edges))
	}
	if edges[0].FromID != center.ID || edges[0].ToID != neighbor.ID {
		t.Errorf("edge direction lost: %s -> %s, want %s -> %s",
			edges[0].FromID, edges[0].ToID, center.ID, neighbor.ID)
	}
	if edges[0].Relationship != model.RelRelatesTo {
		t.Errorf("edge relationship = %q, want %q", edges[0].Relationship, model.RelRelatesTo)
	}

	ctxEdges, err := repo.GetContextEdges(ctx, []uuid.UUID{center.ID, neighbor.ID})
	if err != nil {
		t.Fatalf("get context edges: %v", err)
	}
	if len(ctxEdges[center.ID]) != 1 {
		t.Errorf("center has %d context edges, want 1", len(ctxEdges[center.ID]))
	}
	if got := ctxEdges[center.ID][0].TargetContent; got != neighbor.Content {
		t.Errorf("context edge target content = %q, want %q", got, neighbor.Content)
	}
}

func TestGetContextEdges_EmptyInput(t *testing.T) {
	repo, ctx := testRepo(t)

	got, err := repo.GetContextEdges(ctx, nil)
	if err != nil {
		t.Fatalf("get context edges with no ids: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d context edges for an empty id list, want 0", len(got))
	}
}

func TestSessionLifecycle(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	sess := model.Session{
		ID:        uuid.New(),
		ProjectID: projectID,
		AgentID:   "agent's tester", // apostrophe: hostile to the old escaping
		StartedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	mem := storeMemory(t, repo, ctx, newMemory(projectID, "a memory from this session"))
	if err := repo.LinkMemoryToSession(ctx, sess.ID, mem.ID); err != nil {
		t.Fatalf("link memory to session: %v", err)
	}

	nodes, _, err := repo.GetSubgraph(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get subgraph: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != mem.ID {
		t.Errorf("session subgraph returned %d nodes, want the linked memory", len(nodes))
	}

	ended, err := repo.EndSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("end session: %v", err)
	}
	if ended.EndedAt == nil || ended.EndedAt.IsZero() {
		t.Error("EndedAt was not set when the session ended")
	}
	if ended.AgentID != sess.AgentID {
		t.Errorf("AgentID = %q, want %q", ended.AgentID, sess.AgentID)
	}
}

func TestEmbeddingRoundTripAndVectorSearch(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	target := storeMemory(t, repo, ctx, newMemory(projectID, "the memory we want back"))
	other := storeMemory(t, repo, ctx, newMemory(projectID, "an unrelated memory"))

	// Two clearly separated directions in the embedding space.
	targetVec := make([]float32, testEmbeddingDim)
	targetVec[0] = 1
	otherVec := make([]float32, testEmbeddingDim)
	otherVec[testEmbeddingDim-1] = 1
	if err := repo.StoreEmbedding(ctx, target.ID, projectID, targetVec); err != nil {
		t.Fatalf("store embedding: %v", err)
	}
	if err := repo.StoreEmbedding(ctx, other.ID, projectID, otherVec); err != nil {
		t.Fatalf("store embedding: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.DeleteMemory(context.Background(), target.ID)
		_ = repo.DeleteMemory(context.Background(), other.ID)
	})

	results, err := repo.SearchByVector(ctx, targetVec, projectID, 2)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("vector search returned no results")
	}
	if results[0].Memory.ID != target.ID {
		t.Errorf("nearest neighbor = %q, want %q", results[0].Memory.Content, target.Content)
	}
	if results[0].Score <= results[len(results)-1].Score && len(results) > 1 {
		t.Error("results are not ordered by descending similarity")
	}

	// Scoping must exclude other projects entirely.
	scoped, err := repo.SearchByVector(ctx, targetVec, newProjectID(t), 5)
	if err != nil {
		t.Fatalf("scoped vector search: %v", err)
	}
	if len(scoped) != 0 {
		t.Errorf("vector search leaked %d memories across projects", len(scoped))
	}
}

// TestNodeAndEdgeCount checks that the graph-wide counters reflect writes.
//
// NodeCount and EdgeCount aggregate the whole graph, so an exact delta assumes
// this test is the only writer. Against a shared database -- which is how the
// suite runs, and how CI runs it -- another package's tests write concurrently
// and the exact delta fails spuriously. The graph-wide check is therefore a
// lower bound, and the exact accounting is asserted per project, where this
// test owns every write.
func TestNodeAndEdgeCount(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	countScoped := func(q string) int64 {
		t.Helper()
		rows, err := repo.cypher(ctx, q, params{"pid": projectID})
		if err != nil {
			t.Fatalf("scoped count: %v", err)
		}
		n, found, err := scanOne[int64](rows)
		if err != nil || !found {
			t.Fatalf("scan scoped count: err=%v found=%v", err, found)
		}
		return n
	}
	const nodeQ = `MATCH (n:Memory {project_id: $pid}) RETURN count(n)`
	const edgeQ = `MATCH (a:Memory {project_id: $pid})-[e]->(b:Memory {project_id: $pid}) RETURN count(e)`

	if n := countScoped(nodeQ); n != 0 {
		t.Fatalf("fresh project already holds %d nodes", n)
	}

	beforeNodes, err := repo.NodeCount(ctx)
	if err != nil {
		t.Fatalf("node count: %v", err)
	}
	beforeEdges, err := repo.EdgeCount(ctx)
	if err != nil {
		t.Fatalf("edge count: %v", err)
	}

	from := storeMemory(t, repo, ctx, newMemory(projectID, "counted memory one"))
	to := storeMemory(t, repo, ctx, newMemory(projectID, "counted memory two"))
	if _, err := repo.CreateEdge(ctx, model.Edge{
		ID:           uuid.New(),
		FromID:       from.ID,
		ToID:         to.ID,
		Relationship: model.RelRelatesTo,
		Weight:       1.0,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create edge: %v", err)
	}

	afterNodes, err := repo.NodeCount(ctx)
	if err != nil {
		t.Fatalf("node count: %v", err)
	}
	afterEdges, err := repo.EdgeCount(ctx)
	if err != nil {
		t.Fatalf("edge count: %v", err)
	}

	// Exact, within the project this test owns.
	if n := countScoped(nodeQ); n != 2 {
		t.Errorf("project holds %d nodes, want 2", n)
	}
	if n := countScoped(edgeQ); n != 1 {
		t.Errorf("project holds %d edges, want 1", n)
	}

	// Graph-wide: the counters must return a plausible live count rather than a
	// constant. The delta cannot be asserted at all against a shared database:
	// another package's tests delete rows concurrently, and this read a delta
	// of -12 on a run where nothing was wrong. What must hold is that the
	// counters see at least what this test just wrote and are not stuck at
	// zero -- a counter returning a constant, or ignoring the Memory label
	// entirely, fails here whatever else is running.
	if afterNodes < 2 {
		t.Errorf("graph-wide node count is %d after writing 2 memories; "+
			"the counter is not reading live data", afterNodes)
	}
	if afterEdges < 1 {
		t.Errorf("graph-wide edge count is %d after writing 1 edge; "+
			"the counter is not reading live data", afterEdges)
	}
	if beforeNodes < 0 || beforeEdges < 0 {
		t.Errorf("counters returned negative values: nodes=%d edges=%d",
			beforeNodes, beforeEdges)
	}
}

func TestInitSchemaIsIdempotent(t *testing.T) {
	repo, ctx := testRepo(t)

	// testRepo already ran it once; running it again must be a no-op rather
	// than failing on an existing graph or table.
	for i := 0; i < 2; i++ {
		if err := repo.InitSchema(ctx); err != nil {
			t.Fatalf("InitSchema call %d failed: %v", i+2, err)
		}
	}
}

// TestInitSchema_RejectsDimensionMismatch covers the startup guard. Without it
// a changed embedding model leaves the existing column too narrow, and because
// the service layer discards StoreEmbedding errors, every memory would be
// stored unsearchable by vector with nothing reported.
func TestInitSchema_RejectsDimensionMismatch(t *testing.T) {
	repo, ctx := testRepo(t)

	mismatched := NewAGERepository(repo.pool, testEmbeddingDim*2)
	err := mismatched.InitSchema(ctx)
	if err == nil {
		t.Fatal("InitSchema accepted an embedding dimension that does not match the existing column")
	}
	if !strings.Contains(err.Error(), "embedding dimension mismatch") {
		t.Errorf("error does not explain the mismatch: %v", err)
	}
}

// TestInitSchema_ZeroDimAdoptsExistingSchema is the counterpart to the guard
// above, and guards a regression the guard itself introduced.
//
// Callers that never produce embeddings pass dimension 0 to mean "whatever the
// database already uses" -- cmd/consolidate does exactly this, because a
// maintenance job has no business knowing which embedding model the server runs.
// When 0 was coerced to the 384 default, the mismatch check then aborted that
// job on any deployment using a different model, taking down decay and pruning
// for the whole cluster.
func TestInitSchema_ZeroDimAdoptsExistingSchema(t *testing.T) {
	repo, ctx := testRepo(t)

	// testRepo has already created the table at testEmbeddingDim.
	adopting := NewAGERepository(repo.pool, 0)
	if err := adopting.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema with dimension 0 must adopt the existing schema, got: %v", err)
	}
	if adopting.embeddingDim != testEmbeddingDim {
		t.Errorf("adopted dimension = %d, want %d", adopting.embeddingDim, testEmbeddingDim)
	}

	// Adoption must not weaken the guard: an explicit, wrong dimension is
	// still an error.
	wrong := NewAGERepository(repo.pool, testEmbeddingDim*2)
	if err := wrong.InitSchema(ctx); err == nil {
		t.Error("an explicit mismatched dimension must still be rejected")
	}
}

// TestConcurrentWritesUseDistinctConnections exercises the AfterConnect hook
// that sets search_path. A pool that applied it only to the first connection
// would fail here as soon as a second connection is opened.
func TestConcurrentWritesUseDistinctConnections(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			mem := newMemory(projectID, fmt.Sprintf("concurrent memory %d", i))
			if err := repo.CreateMemory(ctx, mem); err != nil {
				errs <- err
				return
			}
			t.Cleanup(func() { _ = repo.DeleteMemory(context.Background(), mem.ID) })
			errs <- nil
		}(i)
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}

	got, err := repo.QueryMemories(ctx, QueryFilter{ProjectID: projectID, TopK: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != workers {
		t.Errorf("got %d memories after %d concurrent writes", len(got), workers)
	}
}

// TestPoolRequiresValidDSN keeps the connection error path honest.
func TestPoolRequiresValidDSN(t *testing.T) {
	if _, err := NewPool(context.Background(), "not-a-valid-dsn"); err == nil {
		t.Error("expected an error for a malformed connection string")
	}
}

// TestQueryPlansUseIndexes is a performance regression test.
//
// Several query shapes in this package look interchangeable but are not: AGE
// silently falls back to a sequential scan over the whole label for some of
// them, which is invisible in a correctness test and only shows up as latency
// that grows with the corpus. Each shape below was measured during the 2026-08
// performance audit; see docs/research/performance-audit-2026-08.md.
//
// The assertion is index *capability*, not the planner's choice: with
// enable_seqscan off, a shape that can reach the index will, and one that
// cannot still won't. Asserting on the unmodified plan would make the test a
// function of table size -- on a small table a sequential scan is genuinely
// cheapest, so the test would fail on a fresh CI database while passing
// locally against a large one.
//
// Plans are captured through pgx rather than psql because AGE requires the
// third cypher() argument to be a real bind parameter, so psql cannot reproduce
// the parameterized plans the server actually runs.
func TestQueryPlansUseIndexes(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	for i := 0; i < 50; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID, fmt.Sprintf("plan probe %d", i), "plan"))
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(`ANALYZE %s."Memory"`, GraphName)); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	idA, idB := uuid.NewString(), uuid.NewString()

	cases := []struct {
		name  string
		query string
		args  string
	}{
		{
			// GetMemory, DeleteMemory, IncrementAccessCount, UpdateDecayScore.
			"lookup by id",
			`MATCH (m:Memory) WHERE m.id = $id RETURN properties(m)`,
			`{"id": "` + idA + `"}`,
		},
		{
			// QueryMemories: the filter on nearly every read.
			"project filter",
			`MATCH (m:Memory) WHERE m.project_id = $project_id RETURN properties(m) ORDER BY m.created_at DESC LIMIT $top_k`,
			`{"project_id": "` + projectID + `", "top_k": 5}`,
		},
		{
			// GetContextEdges and IncrementAccessCounts. The equivalent-looking
			// `WHERE m.id IN $ids` cannot reach the index at all, which is why
			// both callers use UNWIND.
			"UNWIND over an id list",
			`UNWIND $ids AS wanted MATCH (m:Memory) WHERE m.id = wanted RETURN properties(m)`,
			`{"ids": ["` + idA + `", "` + idB + `"]}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := explainWithoutSeqScan(t, repo, ctx, c.query, c.args)
			if !strings.Contains(plan, "Index Scan") && !strings.Contains(plan, "Bitmap Index Scan") {
				t.Errorf("query cannot use an index, so it will degrade as the corpus grows:\n%s", plan)
			}
		})
	}
}

// TestParameterizedInListCannotUseIndex documents, and pins, the reason
// GetContextEdges and IncrementAccessCounts are written with UNWIND.
//
// A parameterized `WHERE m.id IN $ids` reads as the obvious way to batch by id,
// and it is the shape a future contributor is most likely to "simplify" back
// to. It cannot reach the property index even with sequential scans disabled,
// and measured 30.9ms against 2.3ms for UNWIND at 50k vertices.
//
// If AGE ever fixes this, this test fails and the UNWIND workaround can go.
func TestParameterizedInListCannotUseIndex(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	for i := 0; i < 50; i++ {
		storeMemory(t, repo, ctx, newMemory(projectID, fmt.Sprintf("in-list probe %d", i), "plan"))
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(`ANALYZE %s."Memory"`, GraphName)); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	plan := explainWithoutSeqScan(t, repo, ctx,
		`MATCH (m:Memory) WHERE m.id IN $ids RETURN properties(m)`,
		`{"ids": ["`+uuid.NewString()+`"]}`)

	if strings.Contains(plan, "Index Scan") || strings.Contains(plan, "Bitmap Index Scan") {
		t.Logf("AGE now plans a parameterized IN list against the index:\n%s", plan)
		t.Error("parameterized IN can now use the index; the UNWIND workaround in " +
			"GetContextEdges and IncrementAccessCounts is no longer needed")
	}
}

// explainWithoutSeqScan returns the plan for a parameterized Cypher query with
// sequential scans disabled, which reveals whether the shape can reach an index
// at all rather than whether the planner happened to prefer one.
func explainWithoutSeqScan(t *testing.T, repo *AGERepository, ctx context.Context, query, args string) string {
	t.Helper()

	conn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	// Scoped to this pooled connection and reset before release, so it cannot
	// leak into another test.
	if _, err := conn.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}
	defer func() { _, _ = conn.Exec(ctx, `SET enable_seqscan = on`) }()

	sql := fmt.Sprintf(
		`EXPLAIN SELECT * FROM ag_catalog.cypher('%s', $$ %s $$, $1) AS (result ag_catalog.agtype)`,
		GraphName, query,
	)
	rows, err := conn.Query(ctx, sql, args)
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}

	return strings.Join(plan, "\n")
}

// TestSearchByVector_ProjectFilterDoesNotLoseResults is a correctness
// regression test, not a performance one.
//
// pgvector applies a WHERE filter AFTER the HNSW index has selected its
// candidates. When the query vector sits far from the target project's cluster
// -- the normal case, since a user's question is not a copy of something they
// already stored -- the index's candidate set can contain none of that project,
// and the search returns nothing. Reproduced on a 5k corpus over 20 projects:
// 0 results against 250 matching memories, which reads to a caller as "nothing
// relevant here" rather than as a failure.
//
// SearchByVector enables hnsw.iterative_scan for the filtered case, which makes
// pgvector keep scanning until the limit is satisfied. Same setup, 10 results.
//
// Two conditions are both required to reproduce, which is why this test is the
// slowest in the package: enough rows that the planner prefers HNSW over a
// sequential scan (a seq scan filters correctly and hides the bug), and a query
// vector drawn from a different cluster than the target project.
func TestSearchByVector_ProjectFilterDoesNotLoseResults(t *testing.T) {
	repo, ctx := testRepo(t)
	target := newProjectID(t)
	other := newProjectID(t)

	const targetCount = 250
	const otherCount = 2500

	// Two well-separated clusters, built from real text so the vectors have the
	// same distribution the bag-of-words embedder produces in production.
	embedder := newTestEmbedder(testEmbeddingDim)

	for i := 0; i < otherCount; i++ {
		mem := newMemory(other, fmt.Sprintf("kubernetes deployment rollout note %d", i))
		storeMemory(t, repo, ctx, mem)
		if err := repo.StoreEmbedding(ctx, mem.ID, other, embedder(mem.Content)); err != nil {
			t.Fatalf("store embedding: %v", err)
		}
	}
	for i := 0; i < targetCount; i++ {
		mem := newMemory(target, fmt.Sprintf("postgresql database migration note %d", i))
		storeMemory(t, repo, ctx, mem)
		if err := repo.StoreEmbedding(ctx, mem.ID, target, embedder(mem.Content)); err != nil {
			t.Fatalf("store embedding: %v", err)
		}
	}

	// A query from the other cluster: this is what makes the HNSW candidate set
	// miss the target project entirely.
	query := embedder("kubernetes deployment rollout note 7")

	// Confirm the precondition rather than assuming it. If the planner chose a
	// sequential scan, post-filtering works correctly and this test would pass
	// for the wrong reason -- it would be asserting nothing.
	// The bug only exists when the planner uses the HNSW index; a sequential
	// scan post-filters correctly. Whether it does depends on table statistics
	// and on what other tests have left in the shared database, so rather than
	// skipping (which would make this test assert nothing most of the time),
	// force the index for the duration of the check.
	forceVectorIndex(t, repo, ctx)

	got, err := repo.SearchByVector(ctx, query, target, 10)
	if err != nil {
		t.Fatalf("scoped vector search: %v", err)
	}

	if len(got) == 0 {
		t.Fatal("scoped vector search returned nothing despite 250 matching memories; " +
			"the HNSW post-filter discarded the whole result set")
	}
	if len(got) != 10 {
		t.Errorf("scoped vector search returned %d of a requested 10", len(got))
	}

	for i, r := range got {
		if r.Memory.ProjectID != target {
			t.Errorf("result %d leaked from project %q", i, r.Memory.ProjectID)
		}
		// Equal similarities are expected when documents share text. pgvector
		// computes distance in float32, so nominally-equal scores differ in
		// the low bits; the tolerance has to exceed that noise or the check
		// fails intermittently on ties.
		if i > 0 && r.Score > got[i-1].Score+1e-6 {
			t.Errorf("results not ordered by descending similarity at %d: %f then %f",
				i, got[i-1].Score, r.Score)
		}
	}
}

// TestSearchByVector_HydratesInOneQuery guards the batched hydration.
//
// Each hit used to be fetched with its own GetMemory call, so a top_k of 20
// issued up to 40 round trips before the response could be written. This
// asserts the observable contract that batching must preserve: every hit is
// returned, in similarity order, with its content intact.
func TestSearchByVector_HydratesInOneQuery(t *testing.T) {
	repo, ctx := testRepo(t)
	project := newProjectID(t)

	const count = 15
	contents := make(map[uuid.UUID]string, count)
	for i := 0; i < count; i++ {
		mem := newMemory(project, fmt.Sprintf("hydration probe %d", i))
		storeMemory(t, repo, ctx, mem)
		contents[mem.ID] = mem.Content

		v := make([]float32, testEmbeddingDim)
		v[i%testEmbeddingDim] = 1
		if err := repo.StoreEmbedding(ctx, mem.ID, project, v); err != nil {
			t.Fatalf("store embedding: %v", err)
		}
	}

	query := make([]float32, testEmbeddingDim)
	query[0] = 1

	got, err := repo.SearchByVector(ctx, query, project, count)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}

	// Recall is the subject of the test above and depends on index composition,
	// which other tests in this package influence. Here the contract is that
	// whatever is returned is hydrated correctly, so assert on that rather than
	// on the count.
	if len(got) == 0 {
		t.Fatal("vector search returned nothing")
	}

	for i, r := range got {
		want, ok := contents[r.Memory.ID]
		if !ok {
			t.Errorf("result %d has an id this test never stored: %s", i, r.Memory.ID)
			continue
		}
		if r.Memory.Content != want {
			t.Errorf("result %d content = %q, want %q", i, r.Memory.Content, want)
		}
		if r.Memory.ProjectID != project {
			t.Errorf("result %d leaked from project %q", i, r.Memory.ProjectID)
		}
		// Similarity is legitimately 0 for orthogonal vectors, so only the
		// top hit -- which matches the query exactly -- must be non-zero. That
		// is enough to prove hydration carried the score through.
		if i == 0 && r.Score <= 0 {
			t.Errorf("top hit has similarity %f; hydration dropped the score", r.Score)
		}
		if i > 0 && r.Score > got[i-1].Score+1e-6 {
			t.Errorf("hydration lost similarity ordering at %d", i)
		}
	}
}

// newTestEmbedder returns a deterministic hashed bag-of-words embedder, mirroring
// what internal/embedding produces, so vector tests exercise realistic
// distributions rather than one-hot vectors that cluster unnaturally.
func newTestEmbedder(dim int) func(string) []float32 {
	return func(text string) []float32 {
		vec := make([]float32, dim)
		for _, tok := range strings.Fields(strings.ToLower(text)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(tok))
			sum := h.Sum32()
			vec[sum%uint32(dim)] += 1
			vec[(sum/7)%uint32(dim)] += 0.5
		}
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		if norm > 0 {
			norm = math.Sqrt(norm)
			for i := range vec {
				vec[i] = float32(float64(vec[i]) / norm)
			}
		}
		return vec
	}
}

// forceVectorIndex disables sequential scans for the rest of the test, so a
// project-scoped similarity search is planned against the HNSW index.
//
// This is what makes the post-filter problem observable. Left to its own
// judgement the planner may pick a sequential scan -- correct, and immune to
// the bug -- depending on table statistics and on what other tests have left in
// the shared database. Forcing the index removes that variance, so the test
// asserts the same thing on every run.
func forceVectorIndex(t *testing.T, repo *AGERepository, ctx context.Context) {
	t.Helper()

	if _, err := repo.pool.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("force index scan: %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(), `SET enable_seqscan = on`)
	})
}

// TestCreateEdges_MatchesCreateEdgeSemantics pins the batched writer against
// the per-edge one. Storing a tagged semantic memory fans out into an edge per
// contradiction and per tag match, so this runs on the caller's Store latency
// and must behave identically to the single-edge path it replaced.
func TestCreateEdges_MatchesCreateEdgeSemantics(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	center := storeMemory(t, repo, ctx, newMemory(projectID, "batch edge center"))
	var targets []model.Memory
	for i := 0; i < 5; i++ {
		targets = append(targets,
			storeMemory(t, repo, ctx, newMemory(projectID, fmt.Sprintf("batch edge target %d", i))))
	}

	now := time.Now().UTC().Truncate(time.Second)
	var edges []model.Edge
	for i, target := range targets {
		// Mixed relationship types: the batch groups by type because a label
		// cannot be parameterized.
		rel := model.RelRelatesTo
		if i%2 == 1 {
			rel = model.RelSupersedes
		}
		edges = append(edges, model.Edge{
			ID:           uuid.New(),
			FromID:       center.ID,
			ToID:         target.ID,
			Relationship: rel,
			Weight:       0.5,
			CreatedAt:    now,
		})
	}

	if err := repo.CreateEdges(ctx, edges); err != nil {
		t.Fatalf("create edges: %v", err)
	}

	_, got, err := repo.GetSubgraph(ctx, center.ID)
	if err != nil {
		t.Fatalf("get subgraph: %v", err)
	}
	if len(got) != len(edges) {
		t.Fatalf("graph holds %d edges, want %d", len(got), len(edges))
	}

	byTarget := make(map[uuid.UUID]model.Edge, len(got))
	for _, e := range got {
		byTarget[e.ToID] = e
	}
	for _, want := range edges {
		e, ok := byTarget[want.ToID]
		if !ok {
			t.Errorf("no edge written to %s", want.ToID)
			continue
		}
		if e.Relationship != want.Relationship {
			t.Errorf("edge to %s has relationship %q, want %q", want.ToID, e.Relationship, want.Relationship)
		}
		if e.Weight != want.Weight {
			t.Errorf("edge to %s has weight %f, want %f", want.ToID, e.Weight, want.Weight)
		}
		if e.FromID != center.ID {
			t.Errorf("edge direction lost: from %s, want %s", e.FromID, center.ID)
		}
	}

	// Re-asserting must be idempotent, like CreateEdge.
	if err := repo.CreateEdges(ctx, edges); err != nil {
		t.Fatalf("re-assert edges: %v", err)
	}
	_, after, err := repo.GetSubgraph(ctx, center.ID)
	if err != nil {
		t.Fatalf("get subgraph: %v", err)
	}
	if len(after) != len(edges) {
		t.Errorf("re-asserting produced %d edges, want %d; MERGE is not idempotent here",
			len(after), len(edges))
	}
}

// TestCreateEdges_RejectsUnknownRelationship keeps the batched path under the
// same label allowlist as CreateEdge, since the label is interpolated.
func TestCreateEdges_RejectsUnknownRelationship(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	from := storeMemory(t, repo, ctx, newMemory(projectID, "batch label source"))
	to := storeMemory(t, repo, ctx, newMemory(projectID, "batch label target"))

	err := repo.CreateEdges(ctx, []model.Edge{{
		ID:           uuid.New(),
		FromID:       from.ID,
		ToID:         to.ID,
		Relationship: model.RelationshipType(`relates_to]->(x) DETACH DELETE (x) //`),
		Weight:       1.0,
		CreatedAt:    time.Now().UTC(),
	}})
	if err == nil {
		t.Fatal("CreateEdges accepted a hostile relationship label")
	}
	if !strings.Contains(err.Error(), "unknown relationship") {
		t.Errorf("rejected for the wrong reason: %v", err)
	}

	if _, err := repo.GetMemory(ctx, to.ID); err != nil {
		t.Fatalf("target memory was destroyed: %v", err)
	}
}

// TestCreateEdges_EmptyIsNoOp: the callers pass whatever they collected, which
// is frequently nothing.
func TestCreateEdges_EmptyIsNoOp(t *testing.T) {
	repo, ctx := testRepo(t)
	if err := repo.CreateEdges(ctx, nil); err != nil {
		t.Errorf("CreateEdges(nil) = %v, want no error", err)
	}
}

// TestSearchByVector_DoesNotHoldTwoConnections guards against a pool deadlock.
//
// The scoped search needs a transaction, because hnsw.iterative_scan is set
// with SET LOCAL. Hydrating the results inside that transaction's lifetime
// would hold two pool connections per call -- one for the similarity search and
// one for the graph fetch -- so concurrency at MaxConns deadlocked: every
// caller held a connection and waited for one that would never be released.
//
// Observed before the fix at both MaxConns=4 and the production default of 10:
// every request failed with "hydrate vector hits: context deadline exceeded"
// after blocking for the full timeout.
func TestSearchByVector_DoesNotHoldTwoConnections(t *testing.T) {
	repo, ctx := testRepo(t)
	project := newProjectID(t)

	embedder := newTestEmbedder(testEmbeddingDim)
	for i := 0; i < 30; i++ {
		mem := newMemory(project, fmt.Sprintf("connection probe %d", i))
		storeMemory(t, repo, ctx, mem)
		if err := repo.StoreEmbedding(ctx, mem.ID, project, embedder(mem.Content)); err != nil {
			t.Fatalf("store embedding: %v", err)
		}
	}

	query := embedder("connection probe 3")

	// Saturate the pool: if a single call needs two connections, this cannot
	// complete.
	concurrency := int(repo.pool.Stat().MaxConns())
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Comfortably longer than the query needs, far shorter than the
			// test timeout, so a deadlock surfaces as a clear failure.
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, err := repo.SearchByVector(c, query, project, 5)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent scoped vector search failed, pool likely exhausted: %v", err)
		}
	}
}

// TestQueryMemories_KeywordMatchSurvivesTieBreak is a correctness regression
// test for a bug the soak harness found: a memory was not retrievable by its
// own distinctive keyword moments after being written.
//
// QueryMemories applies `ORDER BY created_at DESC LIMIT k` in Cypher, which
// runs before the ranking layer sees anything. created_at had second precision,
// so a busy project put large groups of memories on the same timestamp -- 153
// in one second under load. Ordering within a tie group is arbitrary, so with
// LIMIT equal to topK the one memory that actually matched the query could be
// discarded before ranking ever ran.
//
// Two things fix it and both are checked here: millisecond timestamps break
// most ties at the source, and the query over-fetches a candidate pool so
// ranking chooses from more than exactly topK rows.
func TestQueryMemories_KeywordMatchSurvivesTieBreak(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// Force the exact condition: every memory shares one timestamp, and the
	// needle is the OLDEST so a created_at DESC ordering puts it last. Writing
	// quickly is not enough to reproduce reliably, and a test that only
	// sometimes exercises the bug is not a regression test.
	shared := time.Now().UTC().Add(-time.Hour)

	needleMem := newMemory(projectID, "the distinctive zqxjklmw marker lives here", "needle")
	needleMem.CreatedAt = shared
	needle := storeMemory(t, repo, ctx, needleMem)

	const noise = 120
	for i := 0; i < noise; i++ {
		m := newMemory(projectID, fmt.Sprintf("filler %d about deployment and rollout", i), "filler")
		m.CreatedAt = shared
		storeMemory(t, repo, ctx, m)
	}

	got, err := repo.QueryMemories(ctx, QueryFilter{
		ProjectID: projectID,
		Keywords:  []string{"zqxjklmw"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var found bool
	for _, r := range got {
		if r.Memory.ID == needle.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the only memory matching the keyword was not returned "+
			"(got %d results); it was discarded by the pre-ranking LIMIT", len(got))
	}
}

// TestCreateMemory_TimestampsHaveSubSecondPrecision pins the storage format.
// Reverting to second precision reintroduces the tie groups that made
// pre-ranking truncation lossy.
func TestCreateMemory_TimestampsHaveSubSecondPrecision(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

	// Written as fast as possible: at second precision these would collide.
	const n = 25
	seen := make(map[string]int, n)
	for i := 0; i < n; i++ {
		mem := newMemory(projectID, fmt.Sprintf("timestamp probe %d", i))
		mem.CreatedAt = time.Now().UTC()
		storeMemory(t, repo, ctx, mem)

		got, err := repo.GetMemory(ctx, mem.ID)
		if err != nil {
			t.Fatalf("get memory: %v", err)
		}
		// Nanoseconds, not a formatted string. RFC3339Nano strips trailing
		// zeros, so a write landing exactly on a millisecond boundary renders
		// as "...49Z" with no fractional part and looks identical to a
		// second-precision value -- which failed this test roughly once in a
		// thousand runs while the storage format was entirely correct.
		seen[got.CreatedAt.Format(time.RFC3339Nano)]++
		// The real property: the millisecond component that was written must
		// come back. Truncating to seconds is what produced the 153-memory tie
		// groups, and it is visible here as a stored value that differs from
		// the written one by the sub-second part.
		if want, got2 := mem.CreatedAt.Truncate(time.Millisecond).UTC(),
			got.CreatedAt.Truncate(time.Millisecond).UTC(); !want.Equal(got2) {
			t.Errorf("timestamp lost precision in storage: wrote %s, read back %s",
				want.Format(time.RFC3339Nano), got2.Format(time.RFC3339Nano))
			break
		}
	}

	// Not a strict "all distinct" assertion: several writes genuinely land in
	// the same millisecond, and how many depends on machine speed. The bug was
	// an entire second collapsing to one value (153 memories shared a
	// timestamp), so the property under test is that distinct sub-second values
	// survive the round trip at all.
	if len(seen) < 3 {
		t.Errorf("%d rapid writes produced only %d distinct timestamps; "+
			"sub-second precision appears to be lost", n, len(seen))
	}
}

// TestUUIDLiteralListRejectsNonUUID guards the one place this repository builds
// query text from values rather than passing parameters.
//
// Inlining is safe because the input is []uuid.UUID -- 16 bytes rendered as 36
// characters from [0-9a-f-], with no way to express a quote or a brace. This
// test is the tripwire for that assumption: if the signature ever loosens to
// strings, or uuid's String() changes, the guard must reject rather than
// silently reopen the injection hole that c9a1c8a closed.
func TestUUIDLiteralListRejectsNonUUID(t *testing.T) {
	if !isPlainUUID("3f2a9c1e-0000-4000-8000-abcdefabcdef") {
		t.Error("a canonical uuid was rejected")
	}

	for _, bad := range []string{
		"",
		"3f2a9c1e-0000-4000-8000-abcdefabcde",   // too short
		"3f2a9c1e-0000-4000-8000-abcdefabcdef1", // too long
		"3f2a9c1e-0000-4000-8000-abcdefabcde'",  // quote
		"3f2a9c1e'-0000-4000-8000-abcdefabcde",  // quote mid-string
		`3f2a9c1e\-0000-4000-8000-abcdefabcde`,  // backslash
		"3f2a9c1e-0000-4000-8000-abcdefabcdeZ",  // non-hex
		"' OR 1=1 --                         ",  // injection attempt at length 36
	} {
		if isPlainUUID(bad) {
			t.Errorf("isPlainUUID(%q) accepted a value that is not a plain uuid", bad)
		}
	}
}

// TestUUIDLiteralListRenders checks the Cypher list literal shape, since a
// malformed list is a syntax error at query time rather than a compile error.
func TestUUIDLiteralListRenders(t *testing.T) {
	a := uuid.MustParse("3f2a9c1e-0000-4000-8000-abcdefabcdef")
	b := uuid.MustParse("11111111-2222-4333-8444-555555555555")

	got, err := uuidLiteralList([]uuid.UUID{a, b})
	if err != nil {
		t.Fatalf("uuidLiteralList: %v", err)
	}
	want := "['3f2a9c1e-0000-4000-8000-abcdefabcdef','11111111-2222-4333-8444-555555555555']"
	if got != want {
		t.Errorf("uuidLiteralList =\n  %s\nwant\n  %s", got, want)
	}

	empty, err := uuidLiteralList(nil)
	if err != nil {
		t.Fatalf("uuidLiteralList(nil): %v", err)
	}
	if empty != "[]" {
		t.Errorf("uuidLiteralList(nil) = %q, want []", empty)
	}
}

// TestSearchByVector_ScopedSearchIsNotLostWithoutStatistics covers a recall
// failure that appears when the planner has no statistics for the embeddings
// table.
//
// pgvector applies a WHERE filter to whatever the HNSW index returns. With
// current statistics the planner sees the project filter is selective, uses the
// project index, and everything is fine. Without them it estimates one row,
// drives the query from the HNSW index instead, and the filter is applied to
// whatever that returned -- so a project holding two matching rows returns one.
//
// That is not a rare state. It is where a table sits after a bulk import, after
// a restore, and during the window before autoanalyze catches up on a
// fast-growing table.
//
// Measured directly against this deployment with pg_statistic cleared:
//
//	plain filtered query   1 of 2 rows
//	the shipped CTE form   2 of 2 rows
//
// The earlier version of this test seeded 2,400 rows and asserted the same
// property, which passed with the fix reverted: the surrounding table already
// held 147,000 rows with fresh statistics, so the planner made the right choice
// regardless and the test proved nothing. It now removes the statistics, which
// is the condition that actually distinguishes the two forms.
func TestSearchByVector_ScopedSearchIsNotLostWithoutStatistics(t *testing.T) {
	repo, ctx := testRepo(t)

	project := newProjectID(t)
	target := storeMemory(t, repo, ctx, newMemory(project, "the one that matches"))
	other := storeMemory(t, repo, ctx, newMemory(project, "the one that does not"))

	targetVec := make([]float32, testEmbeddingDim)
	targetVec[0] = 1
	otherVec := make([]float32, testEmbeddingDim)
	otherVec[testEmbeddingDim-1] = 1
	if err := repo.StoreEmbedding(ctx, target.ID, project, targetVec); err != nil {
		t.Fatalf("store embedding: %v", err)
	}
	if err := repo.StoreEmbedding(ctx, other.ID, project, otherVec); err != nil {
		t.Fatalf("store embedding: %v", err)
	}

	// Put the planner in the state a fresh import or restore leaves it in.
	// Restored afterwards so the rest of the suite is unaffected.
	if _, err := repo.pool.Exec(ctx,
		`DELETE FROM pg_statistic WHERE starelid = 'public.memory_embeddings'::regclass`); err != nil {
		t.Skipf("cannot clear planner statistics (needs table ownership): %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(), `ANALYZE public.memory_embeddings`)
	})

	results, err := repo.SearchByVector(ctx, targetVec, project, 2)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("scoped search returned %d of 2 memories in its project with no "+
			"planner statistics: the filter is being applied after the index "+
			"search instead of before it", len(results))
	}
	if results[0].Memory.ID != target.ID {
		t.Errorf("nearest neighbour = %q, want %q", results[0].Memory.Content, target.Content)
	}
	if results[0].Score <= 0 {
		t.Errorf("top hit similarity = %f, want > 0", results[0].Score)
	}
}

// TestSearchByVector_SurvivesIndexChurn covers a recall failure that only
// appears after deletes.
//
// An HNSW index keeps entries for deleted rows until VACUUM removes them, and a
// scan stops once it has examined hnsw.max_scan_tuples. On a table with many
// dead entries the budget is spent reaching them, so live rows that plainly
// match are never returned: a two-row project returned one row, with the wrong
// one first, intermittently -- 6 failures in 20 runs against a churned table,
// and zero immediately after a VACUUM.
//
// This is not a synthetic worry. Deletes come from consolidation pruning and
// from DeleteMemory, so churn is the normal state of a long-lived deployment.
func TestSearchByVector_SurvivesIndexChurn(t *testing.T) {
	repo, ctx := testRepo(t)

	// Create and delete enough embeddings to leave dead entries behind.
	churn := newProjectID(t)
	for i := 0; i < 200; i++ {
		mem := newMemory(churn, fmt.Sprintf("churn %d", i))
		if err := repo.CreateMemory(ctx, mem); err != nil {
			t.Fatalf("create: %v", err)
		}
		vec := make([]float32, testEmbeddingDim)
		vec[i%testEmbeddingDim] = 1
		if err := repo.StoreEmbedding(ctx, mem.ID, churn, vec); err != nil {
			t.Fatalf("store embedding: %v", err)
		}
		if err := repo.DeleteMemory(ctx, mem.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}

	// Now the real data, in its own project.
	project := newProjectID(t)
	target := storeMemory(t, repo, ctx, newMemory(project, "the one that matches"))
	other := storeMemory(t, repo, ctx, newMemory(project, "the one that does not"))

	targetVec := make([]float32, testEmbeddingDim)
	targetVec[0] = 1
	otherVec := make([]float32, testEmbeddingDim)
	otherVec[testEmbeddingDim-1] = 1
	if err := repo.StoreEmbedding(ctx, target.ID, project, targetVec); err != nil {
		t.Fatalf("store embedding: %v", err)
	}
	if err := repo.StoreEmbedding(ctx, other.ID, project, otherVec); err != nil {
		t.Fatalf("store embedding: %v", err)
	}

	results, err := repo.SearchByVector(ctx, targetVec, project, 2)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("search returned %d of 2 matching memories after index churn; "+
			"dead HNSW entries exhausted the scan budget", len(results))
	}
	if results[0].Memory.ID != target.ID {
		t.Errorf("nearest neighbour = %q, want %q", results[0].Memory.Content, target.Content)
	}
	if results[0].Score <= 0 {
		t.Errorf("top hit similarity = %f, want > 0", results[0].Score)
	}
}

// TestEndSessionPreservesOriginalTimestamp: a repeated end must be rejected
// without disturbing the session that was already recorded.
//
// EndSession set ended_at unconditionally, so a second call overwrote the
// original timestamp and returned success. That both corrupted session
// duration and let the caller decrement ActiveSessions twice.
func TestEndSessionPreservesOriginalTimestamp(t *testing.T) {
	repo, ctx := testRepo(t)

	sess := model.Session{
		ID:        uuid.New(),
		ProjectID: "end-session-idempotency",
		AgentID:   "agent",
		StartedAt: time.Now().UTC(),
	}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	ended, err := repo.EndSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("first EndSession: %v", err)
	}
	if ended.EndedAt == nil {
		t.Fatal("first EndSession returned a session with no ended_at")
	}
	original := *ended.EndedAt

	// A full second apart, so an overwrite is visible at the stored
	// timestamp's resolution rather than hidden by it.
	time.Sleep(1100 * time.Millisecond)

	if _, err := repo.EndSession(ctx, sess.ID); !errors.Is(err, ErrSessionAlreadyEnded) {
		t.Fatalf("second EndSession returned %v, want ErrSessionAlreadyEnded", err)
	}

	// Read the vertex back rather than trusting the returned value.
	const q = `MATCH (s:Session {id: $id}) RETURN properties(s)`
	rows, err := repo.cypher(ctx, q, params{"id": sess.ID.String()})
	if err != nil {
		t.Fatalf("query session: %v", err)
	}
	props, found, err := scanOne[sessionProps](rows)
	if err != nil || !found {
		t.Fatalf("scan session: err=%v found=%v", err, found)
	}
	stored := props.toModel()
	if stored.EndedAt == nil {
		t.Fatal("ended_at was cleared by the rejected call")
	}
	if !stored.EndedAt.Equal(original) {
		t.Errorf("ended_at moved from %v to %v: the rejected end overwrote the "+
			"original timestamp, so session duration is measured from the last "+
			"stray request instead of the real end", original, *stored.EndedAt)
	}
}

// TestEndSessionUnknownIDIsNotFound keeps the two failure modes distinct, so a
// stale retry is not reported as a bad session ID.
func TestEndSessionUnknownIDIsNotFound(t *testing.T) {
	repo, ctx := testRepo(t)

	_, err := repo.EndSession(ctx, uuid.New())
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("EndSession on an unknown ID returned %v, want ErrSessionNotFound", err)
	}
	if errors.Is(err, ErrSessionAlreadyEnded) {
		t.Error("an unknown session was reported as already ended")
	}
}
