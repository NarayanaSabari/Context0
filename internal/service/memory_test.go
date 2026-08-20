package service

import (
	"context"
	"errors"
	"fmt"
	pb "github.com/NarayanaSabari/Kora/api/gen/kora/v1"
	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"empty", "", nil},
		{"stop words only", "what is the", nil},
		{"with keywords", "what database does this project use", []string{"database"}},
		{"multiple keywords", "postgres database migration", []string{"postgres", "database", "migration"}},
		{"short words filtered", "a b cd ef", []string{"cd", "ef"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKeywords(tt.query)
			if len(got) != len(tt.want) {
				t.Errorf("extractKeywords(%q) = %v, want %v", tt.query, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("keyword[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseQuery_Defaults(t *testing.T) {
	f := ParseQuery("test query", "proj1", nil, 0)

	if f.TopK != 5 {
		t.Errorf("default TopK = %d, want 5", f.TopK)
	}
	if f.ProjectID != "proj1" {
		t.Errorf("ProjectID = %q, want 'proj1'", f.ProjectID)
	}
}

func TestParseQuery_Limits(t *testing.T) {
	f := ParseQuery("test", "proj1", nil, 50)

	if f.TopK != 20 {
		t.Errorf("TopK should be capped at 20, got %d", f.TopK)
	}
}

func TestHasOverlappingTags(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"no overlap", []string{"a", "b"}, []string{"c", "d"}, false},
		{"overlap", []string{"a", "b"}, []string{"b", "c"}, true},
		{"case insensitive", []string{"Foo"}, []string{"foo"}, true},
		{"empty", nil, []string{"a"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasOverlappingTags(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("hasOverlappingTags(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestMergeResults_CarriesRelevanceForward guards the contract between
// retrieval and ranking: whatever the two retrievers determined about query
// match quality must survive the merge in the Relevance field, because that is
// the only channel ranking reads.
func TestMergeResults_CarriesRelevanceForward(t *testing.T) {
	graphOnly := uuid.New()
	vectorOnly := uuid.New()
	both := uuid.New()

	graphResults := []model.MemoryWithContext{
		{Memory: model.Memory{ID: graphOnly}, Relevance: 0.8},
		{Memory: model.Memory{ID: both}, Relevance: 0.6},
	}
	// The repository reports cosine similarity in Score, not Relevance.
	vectorResults := []model.MemoryWithContext{
		{Memory: model.Memory{ID: vectorOnly}, Score: 0.7},
		{Memory: model.Memory{ID: both}, Score: 0.5},
	}

	merged := mergeResults(graphResults, vectorResults, []string{"kw"})

	if len(merged) != 3 {
		t.Fatalf("expected 3 deduplicated results, got %d", len(merged))
	}

	byID := make(map[uuid.UUID]model.MemoryWithContext, len(merged))
	for _, m := range merged {
		byID[m.Memory.ID] = m
	}

	// Relevance is now tiered rather than passed through raw: candidates that
	// lexically matched the query outrank those the vector retriever surfaced
	// on similarity alone, because cosine similarity and lexical match are not
	// measured on the same scale. Assert the ordering the ranking layer relies
	// on, not the specific arithmetic.
	if byID[graphOnly].Relevance <= byID[vectorOnly].Relevance {
		t.Errorf("a keyword match (%f) must outrank a vector-only hit (%f)",
			byID[graphOnly].Relevance, byID[vectorOnly].Relevance)
	}
	if byID[both].Relevance <= byID[vectorOnly].Relevance {
		t.Errorf("a memory found by both retrievers (%f) must outrank a vector-only hit (%f)",
			byID[both].Relevance, byID[vectorOnly].Relevance)
	}

	// Agreement between retrievers must still lift a memory above the same
	// memory matched lexically alone.
	lexicalOnly := mergeResults(
		[]model.MemoryWithContext{{Memory: model.Memory{ID: both}, Relevance: 0.6}},
		nil, []string{"kw"},
	)
	if byID[both].Relevance <= lexicalOnly[0].Relevance {
		t.Errorf("cross-retriever agreement should boost relevance: %f with agreement vs %f without",
			byID[both].Relevance, lexicalOnly[0].Relevance)
	}
	for id, m := range byID {
		if m.Relevance > 1.0 || m.Relevance < 0 {
			t.Errorf("relevance for %s is %f, outside [0,1]", id, m.Relevance)
		}
	}
}

// TestMergeResults_IsDeterministic pins the ordering guarantee. Merging happens
// through a map, and Go randomizes map iteration, so without an explicit sort
// the candidate order would vary between identical queries.
func TestMergeResults_IsDeterministic(t *testing.T) {
	var graphResults []model.MemoryWithContext
	for i := 0; i < 10; i++ {
		graphResults = append(graphResults, model.MemoryWithContext{
			Memory:    model.Memory{ID: uuid.New()},
			Relevance: 0.5,
		})
	}

	first := mergeResults(graphResults, nil, []string{"kw"})
	for i := 0; i < 20; i++ {
		got := mergeResults(graphResults, nil, []string{"kw"})
		for j := range got {
			if got[j].Memory.ID != first[j].Memory.ID {
				t.Fatalf("mergeResults order varies between identical calls at %d", j)
			}
		}
	}
}

// failingEmbedder fails every Embed call, standing in for an embedding
// provider that is down, rate-limited, or misconfigured.
type failingEmbedder struct{ calls atomic.Int64 }

func (e *failingEmbedder) Embed(string) ([]float32, error) {
	e.calls.Add(1)
	return nil, errors.New("embedding provider unavailable")
}

func (e *failingEmbedder) Dimension() int { return 384 }

// TestStoreSucceedsWhenEmbeddingFails pins the tradeoff the embedding path is
// built around: an embedding failure must not lose the write.
//
// The provider is a network dependency and the most likely thing to be down.
// Failing the Store would turn a degraded vector index into total write
// unavailability, so the memory is stored and the failure is logged instead.
// The cost is that the memory is absent from vector search until it is
// re-embedded, which is why the log is at Error.
//
// Nothing asserted any of this: mutation testing skipped the whole embedding
// block and every test still passed.
func TestStoreSucceedsWhenEmbeddingFails(t *testing.T) {
	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	embedder := &failingEmbedder{}
	svc := NewMemoryService(repo, embedder)

	projectID := fmt.Sprintf("embed-failure-%d", time.Now().UnixNano())
	resp, err := svc.Store(ctx, &pb.StoreRequest{
		Content:   "the deploy target is production",
		ProjectId: projectID,
		Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
	})
	if err != nil {
		t.Fatalf("Store failed because embedding failed: %v -- "+
			"an unavailable embedding provider must not make the service "+
			"unable to accept writes", err)
	}
	if embedder.calls.Load() == 0 {
		t.Fatal("the embedder was never called; the test proves nothing")
	}

	// The memory must be readable, which is the whole point of not failing.
	id, err := uuid.Parse(resp.Memory.Id)
	if err != nil {
		t.Fatalf("bad memory id: %v", err)
	}
	got, err := repo.GetMemory(ctx, id)
	if err != nil {
		t.Fatalf("stored memory is unreadable after an embedding failure: %v", err)
	}
	if got.Content != "the deploy target is production" {
		t.Errorf("content = %q, want the stored content", got.Content)
	}

	// And it must still be findable by keyword, the fallback the comment in
	// StoreMemory relies on when it calls the failure non-fatal.
	results, err := repo.QueryMemories(ctx, graph.QueryFilter{
		ProjectID: projectID,
		Keywords:  []string{"deploy"},
		TopK:      10,
	})
	if err != nil {
		t.Fatalf("keyword query: %v", err)
	}
	if len(results) == 0 {
		t.Error("a memory whose embedding failed is not findable by keyword " +
			"either, so the write is effectively lost")
	}
}

// TestStoreLinksToSession covers the session_id branch of Store, which nothing
// exercised: a valid ID must link the memory to the session, and an invalid one
// must be rejected as a bad argument rather than stored unlinked.
//
// Silently dropping the link would be the worst outcome. The memory would exist
// but not belong to the session that produced it, so any later "what happened
// in this session" traversal returns an incomplete answer with no error to
// explain it.
func TestStoreLinksToSession(t *testing.T) {
	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	svc := NewMemoryService(repo, nil)
	sessions := NewSessionService(repo)

	projectID := fmt.Sprintf("session-link-%d", time.Now().UnixNano())
	start, err := sessions.StartSession(ctx, &pb.StartSessionRequest{
		ProjectId: projectID,
		AgentId:   "linker",
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	stored, err := svc.Store(ctx, &pb.StoreRequest{
		Content:   "a memory produced inside a session",
		ProjectId: projectID,
		Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
		SessionId: start.Session.Id,
	})
	if err != nil {
		t.Fatalf("store with a valid session_id: %v", err)
	}

	// The edge must exist, not merely the memory.
	sessID, err := uuid.Parse(start.Session.Id)
	if err != nil {
		t.Fatalf("bad session id: %v", err)
	}
	memID, err := uuid.Parse(stored.Memory.Id)
	if err != nil {
		t.Fatalf("bad memory id: %v", err)
	}
	var linked int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM cypher('context0', $$
			MATCH (s:Session {id: $sid})-[:contains]->(m:Memory {id: $mid})
			RETURN m
		$$, $1) AS (m agtype)`,
		fmt.Sprintf(`{"sid": %q, "mid": %q}`, sessID, memID),
	).Scan(&linked); err != nil {
		t.Fatalf("read session link: %v", err)
	}
	if linked != 1 {
		t.Errorf("found %d contains edges from session %v to memory %v, want 1: "+
			"the memory was stored but not linked, so the session's history is "+
			"silently incomplete", linked, sessID, memID)
	}

	// A malformed session_id is the caller's error and must be reported.
	for _, bad := range []string{"not-a-uuid", "12345"} {
		if _, err := svc.Store(ctx, &pb.StoreRequest{
			Content:   "memory with a bad session id",
			ProjectId: projectID,
			Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
			SessionId: bad,
		}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("Store with session_id %q returned %v, want InvalidArgument", bad, err)
		}
	}

	// No session_id at all is valid and must not be treated as an error.
	if _, err := svc.Store(ctx, &pb.StoreRequest{
		Content:   "a memory with no session",
		ProjectId: projectID,
		Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
	}); err != nil {
		t.Errorf("Store without a session_id failed: %v", err)
	}
}

// TestStoreWithoutEmbedderSucceeds: the embedder is optional, and a nil one
// disables vector search rather than breaking writes.
func TestStoreWithoutEmbedderSucceeds(t *testing.T) {
	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Explicitly nil: NewMemoryService documents this as supported.
	svc := NewMemoryService(repo, nil)

	projectID := fmt.Sprintf("no-embedder-%d", time.Now().UnixNano())
	if _, err := svc.Store(ctx, &pb.StoreRequest{
		Content:   "stored with vector search disabled",
		ProjectId: projectID,
		Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
	}); err != nil {
		t.Fatalf("Store with a nil embedder failed: %v -- the embedder is "+
			"documented as optional, so this configuration must accept writes", err)
	}
}

// TestCancelledStoreStillFinishesTheRecord covers a memory left permanently
// unsearchable by a client that hung up.
//
// Store commits the memory, then embeds it, links it to a session, runs
// contradiction detection and auto-links by tag. All of that ran on the
// caller's context, so a client that disconnected after CreateMemory left the
// memory stored with no embedding row: absent from vector search forever,
// while Store still returned success, so nothing would ever retry it.
//
// Measured before the fix, cancelling across a spread of delays: 2 of 6
// successful stores had no embedding. After: 0 of 32.
//
// Once the memory is committed, finishing its record belongs to the write
// rather than to the caller, so that work now runs on its own bounded context.
func TestCancelledStoreStillFinishesTheRecord(t *testing.T) {
	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	svc := NewMemoryService(repo, embedding.NewBagOfWordsEmbedder(384))

	projectID := fmt.Sprintf("cancel-finish-%d", time.Now().UnixNano())

	// A spread of deadlines, so cancellation lands at different points in the
	// pipeline rather than always the same one.
	var succeeded []uuid.UUID
	for i := 0; i < 40; i++ {
		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(i)*500*time.Microsecond)
		resp, err := svc.Store(reqCtx, &pb.StoreRequest{
			Content:   fmt.Sprintf("cancellation probe %d about kubernetes deployment", i),
			ProjectId: projectID,
			Type:      pb.MemoryType_MEMORY_TYPE_SEMANTIC,
		})
		cancel()
		if err == nil && resp != nil {
			id, perr := uuid.Parse(resp.Memory.Id)
			if perr != nil {
				t.Fatalf("Store returned an unparseable id %q", resp.Memory.Id)
			}
			succeeded = append(succeeded, id)
		}
	}

	if len(succeeded) == 0 {
		t.Fatal("no store succeeded; the test observed nothing")
	}

	// The invariant: anything Store reported as successful is complete. A
	// caller told "stored" has no reason to retry, so an incomplete record here
	// is permanent.
	var missing int
	for _, id := range succeeded {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM memory_embeddings WHERE memory_id=$1`, id).Scan(&n); err != nil {
			t.Fatalf("count embeddings: %v", err)
		}
		if n == 0 {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d of %d successful stores have no embedding row; those "+
			"memories are permanently absent from vector search and the caller "+
			"was told the write succeeded", missing, len(succeeded))
	}
}

// TestStoreFinishWorkIsBounded: dropping the caller's cancellation without a
// deadline of its own would let a stalled provider or database hold the
// request, and the goroutine serving it, open forever.
func TestStoreFinishWorkIsBounded(t *testing.T) {
	if storeFinishTimeout <= 0 {
		t.Fatal("the post-commit work has no timeout; a stalled dependency " +
			"would hold the request open indefinitely")
	}
	if storeFinishTimeout > 2*time.Minute {
		t.Errorf("storeFinishTimeout is %s, too long to bound a single write",
			storeFinishTimeout)
	}
}

// TestCancelledExtractStillFinishesItsMemories: Extract has the same shape as
// Store -- commit a memory, then embed it -- repeated per extracted memory, so
// it had the same defect.
//
// A client that hung up mid-loop left the memories already written with no
// embedding row, permanently absent from vector search. Measured before the
// fix across a spread of cancellation points: 3 of 10 persisted memories had
// no embedding, and the responses reported 7 while 10 had actually landed.
func TestCancelledExtractStillFinishesItsMemories(t *testing.T) {
	dsn := os.Getenv("KORA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KORA_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := graph.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := graph.NewAGERepository(pool, 384)
	if err := repo.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	svc := NewMemoryService(repo, embedding.NewBagOfWordsEmbedder(384))

	projectID := fmt.Sprintf("extract-cancel-%d", time.Now().UnixNano())
	conversation := "User: I prefer dark mode.\n" +
		"Assistant: Noted.\n" +
		"User: I always deploy on Fridays.\n" +
		"Assistant: Understood.\n" +
		"User: The backend uses Go."

	for i := 0; i < 30; i++ {
		reqCtx, cancel := context.WithTimeout(ctx, time.Duration(i)*time.Millisecond)
		_, _ = svc.Extract(reqCtx, &pb.ExtractRequest{
			Conversation: conversation,
			ProjectId:    projectID,
		})
		cancel()
	}

	results, err := repo.QueryMemories(ctx, graph.QueryFilter{ProjectID: projectID, TopK: 500})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no memory was extracted; the test observed nothing")
	}

	var missing int
	for _, r := range results {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM memory_embeddings WHERE memory_id=$1`, r.Memory.ID).Scan(&n); err != nil {
			t.Fatalf("count embeddings: %v", err)
		}
		if n == 0 {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d of %d extracted memories have no embedding row; a client "+
			"disconnecting mid-extraction leaves them permanently absent from "+
			"vector search", missing, len(results))
	}
}
