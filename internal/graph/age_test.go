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
	"fmt"
	"os"
	"strings"
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
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	return repo, ctx
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
// injection hole. Every payload here would have broken out of the old
// single-quote-escaped string interpolation; with parameters they must all
// round-trip byte for byte, and the graph must be intact afterwards.
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

func TestNodeAndEdgeCount(t *testing.T) {
	repo, ctx := testRepo(t)
	projectID := newProjectID(t)

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

	if afterNodes-beforeNodes != 2 {
		t.Errorf("node count rose by %d, want 2", afterNodes-beforeNodes)
	}
	if afterEdges-beforeEdges != 1 {
		t.Errorf("edge count rose by %d, want 1", afterEdges-beforeEdges)
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
