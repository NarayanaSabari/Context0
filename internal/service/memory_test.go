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
	"strings"
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
		// Keyword matching is CONTAINS against stored content, so punctuation
		// carried into a keyword can never match: "group?" is not a substring
		// of "...support group yesterday". Questions are the normal way users
		// query a memory engine, and the trailing "?" attaches to the last and
		// often most specific word, so this silently dropped the best keyword
		// in the query. Found via the LoCoMo benchmark.
		{"trailing question mark stripped", "when did Caroline go to the LGBTQ support group?",
			[]string{"caroline", "go", "lgbtq", "support", "group"}},
		{"internal punctuation stripped", "what about the postgres, database; migration!",
			[]string{"about", "postgres", "database", "migration"}},
		// Punctuation inside a token is meaningful and must survive: these are
		// real identifiers, not sentence punctuation.
		{"intra-word punctuation kept", "the user's api-key and node.js setup",
			[]string{"user's", "api-key", "and", "node.js", "setup"}},
		{"punctuation-only tokens dropped", "deploy -- now", []string{"deploy", "now"}},
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

// TestParseQuery_Limits pins the bound on top_k.
//
// A cap has to exist: top_k sizes the candidate pools and the hydrated result
// set, so an unbounded value is a memory-exhaustion vector from an
// unauthenticated field. But the cap was 20 against a proto that documents
// top_k only as "Maximum number of results to return", so a caller asking for
// 50 silently received 20 with no error and no indication. Comparable engines
// retrieve at 30 or more by default, which made the undocumented clamp a
// quality ceiling as well as a surprise.
func TestParseQuery_Limits(t *testing.T) {
	if f := ParseQuery("test", "proj1", nil, 50); f.TopK != 50 {
		t.Errorf("TopK = %d for a request of 50: values within the documented "+
			"maximum must be honoured, not silently reduced", f.TopK)
	}

	if f := ParseQuery("test", "proj1", nil, maxTopK); f.TopK != maxTopK {
		t.Errorf("TopK = %d at the documented maximum of %d", f.TopK, maxTopK)
	}

	// Beyond the maximum the request is still clamped rather than refused:
	// ParseQuery has no error return, and the alternative is failing a query
	// that the engine can serve perfectly well by returning the most it
	// supports. The bound is documented in memory.proto so this is no longer a
	// surprise.
	if f := ParseQuery("test", "proj1", nil, maxTopK+1000); f.TopK != maxTopK {
		t.Errorf("TopK = %d beyond the maximum, want it clamped to %d", f.TopK, maxTopK)
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

	merged := mergeResults(graphResults, vectorResults, nil, nil)

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
		nil, nil, nil,
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

	first := mergeResults(graphResults, nil, nil, nil)
	for i := 0; i < 20; i++ {
		got := mergeResults(graphResults, nil, nil, nil)
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

// slowEmbedder simulates a cloud embedding provider: every call is a network
// round trip. The delay is what makes the serial-versus-parallel difference
// observable; a local embedder returns too fast to expose it.
type slowEmbedder struct {
	delay time.Duration
	calls atomic.Int64
}

func (e *slowEmbedder) Embed(string) ([]float32, error) {
	e.calls.Add(1)
	time.Sleep(e.delay)
	return make([]float32, 384), nil
}

func (e *slowEmbedder) Dimension() int { return 384 }

// TestExtractEmbedsConcurrently pins the fix for silent data loss on long
// conversations.
//
// Extract used to embed inside the store loop, one blocking network call per
// extracted memory. The surrounding context is bounded by storeFinishTimeout
// (30s), so a conversation whose memory count multiplied by the provider
// latency exceeded that budget had its tail silently dropped: the request
// still returned 200, and the response simply contained fewer memories than
// the conversation held.
//
// Measured against a real Gemini endpoint, a 50-turn transcript returned 61 of
// 100 memories in 30.4s -- pinned exactly at the timeout -- and logged no
// deadline error at all. Turns 31 through 49 were gone. The same payload after
// the fix returned all 100 in 6.9s.
//
// This test fails on the serial implementation: 40 memories at 100ms each is
// 4s serially against a 2s budget, so the tail is lost.
func TestExtractEmbedsConcurrently(t *testing.T) {
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

	const (
		lines      = 40
		perEmbed   = 100 * time.Millisecond
		serialCost = lines * perEmbed // 4s, well past any sane budget
	)

	embedder := &slowEmbedder{delay: perEmbed}
	svc := NewMemoryService(repo, embedder)

	var sb strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&sb, "Caroline decided to adopt rescue dog number %d in Lisbon.\n", i)
	}

	projectID := fmt.Sprintf("extract-concurrency-%d", time.Now().UnixNano())
	start := time.Now()
	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		Conversation: sb.String(),
		ProjectId:    projectID,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	elapsed := time.Since(start)

	// Nothing may be dropped. This is the assertion that fails on the serial
	// implementation once the conversation is long enough.
	if len(resp.Memories) != lines {
		t.Errorf("Extract returned %d memories, want %d -- the tail of the "+
			"conversation was dropped", len(resp.Memories), lines)
	}

	// Every extracted memory must have been embedded, not skipped.
	if got := embedder.calls.Load(); got != int64(lines) {
		t.Errorf("embedder called %d times, want %d", got, lines)
	}

	// The wall clock proves the calls actually overlapped rather than merely
	// completing. Serial execution cannot beat serialCost.
	if elapsed >= serialCost {
		t.Errorf("Extract took %v, which is at or beyond the serial cost %v: "+
			"the embedding calls did not run concurrently", elapsed, serialCost)
	}
}

// TestExtractReportsRealRelationshipCount pins that ExtractResponse.
// RelationshipsCreated is the number of edges actually written to the graph.
//
// It was incremented once per stored memory, regardless of whether any edge
// was created: `s.autoLinkByTags(...); relCount++`. Extract therefore reported
// a relationship count equal to its memory count on every call. The number was
// not merely imprecise, it was measuring a different thing, and it is the one
// signal a caller has for whether the graph is being built at all.
//
// Found while investigating why a 6,760-memory benchmark corpus had produced
// exactly one edge: the API had been reporting thousands of relationships the
// whole time, which is what kept the dead graph invisible.
//
// The conversation below is deliberately non-technical, which is the case that
// produces no tags and therefore no tag-derived edges.
func TestExtractReportsRealRelationshipCount(t *testing.T) {
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

	projectID := fmt.Sprintf("relcount-%d", time.Now().UnixNano())
	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId: projectID,
		Conversation: "Caroline said that she moved to Lisbon last spring.\n" +
			"Caroline said that she eats bacalhau every Friday with her sister.\n" +
			"Caroline said that she runs along the river each morning.",
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(resp.Memories) == 0 {
		t.Fatal("extraction produced no memories; the rest of the test is meaningless")
	}

	// Count what actually landed in the graph, by asking the graph.
	ids := make([]uuid.UUID, 0, len(resp.Memories))
	for _, m := range resp.Memories {
		id, err := uuid.Parse(m.Id)
		if err != nil {
			t.Fatalf("memory id %q is not a uuid: %v", m.Id, err)
		}
		ids = append(ids, id)
	}

	edges, err := repo.GetContextEdges(ctx, ids)
	if err != nil {
		t.Fatalf("get context edges: %v", err)
	}
	// GetContextEdges is undirected: it returns both the outgoing and incoming
	// view, so an edge between two memories that are both in ids appears once
	// from each end. Counted as unique pairs rather than rows, or a correct
	// implementation would look like double-reporting.
	unique := make(map[string]bool)
	for id, list := range edges {
		for _, e := range list {
			a, b := id.String(), e.TargetID.String()
			if a > b {
				a, b = b, a
			}
			unique[a+"|"+b] = true
		}
	}
	actual := len(unique)

	// Entity edges are counted separately because GetContextEdges only
	// traverses memory-to-memory patterns, so a mentions edge is invisible to
	// it. They are reported to the caller like any other relationship, and
	// for an untagged, semantically-isolated memory they are frequently the
	// only edges it gets.
	entities, err := repo.GetMemoryEntities(ctx, ids)
	if err != nil {
		t.Fatalf("get memory entities: %v", err)
	}
	for _, names := range entities {
		actual += len(names)
	}

	if int(resp.RelationshipsCreated) != actual {
		t.Errorf("RelationshipsCreated = %d but the graph holds %d edges for these memories: "+
			"the field must count edges written, not memories processed",
			resp.RelationshipsCreated, actual)
	}
}

// TestExtractLinksSemanticallyRelatedMemories pins that a conversation with no
// recognised technical vocabulary still produces a connected graph.
//
// Relatedness was decided solely by hasOverlappingTags, and tags came from
// extractTopics, a hardcoded map of roughly forty DevOps terms (postgresql,
// kubernetes, grpc, oauth and so on). Any conversation outside that vocabulary
// produced no tags, so no two memories ever shared one, so no relates_to edge
// was ever created. Measured on a LoCoMo benchmark corpus: 6,760 memories and
// exactly one edge, with 97% of memories carrying no tags at all.
//
// That makes the graph -- the engine's headline feature over flat vector
// search -- inert for every domain except the one hardcoded here. Relatedness
// has to come from what the memories mean, which is what the embeddings the
// engine already computes are for.
func TestExtractLinksSemanticallyRelatedMemories(t *testing.T) {
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

	projectID := fmt.Sprintf("semlink-%d", time.Now().UnixNano())

	// Deliberately free of any term in extractTopics. The first three lines are
	// closely related to each other; the fourth is unrelated, and exists so a
	// passing result cannot come from linking everything to everything.
	resp, err := svc.Extract(ctx, &pb.ExtractRequest{
		ProjectId: projectID,
		Conversation: "Caroline said that she adopted a rescue dog named Biscuit last month.\n" +
			"Caroline said that Biscuit is a nervous rescue dog who hates thunderstorms.\n" +
			"Caroline said that she walks her rescue dog Biscuit twice a day.\n" +
			"Melanie said that the quarterly tax filing deadline is in April.",
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(resp.Memories) < 4 {
		t.Fatalf("expected at least 4 memories, got %d", len(resp.Memories))
	}

	if resp.RelationshipsCreated == 0 {
		t.Fatalf("a four-line conversation about a dog produced no relationships: "+
			"relatedness must come from meaning, not from a fixed list of technical terms "+
			"(got %d memories)", len(resp.Memories))
	}

	// Linking everything to everything would also satisfy the check above, and
	// would be worse than no graph: a dense graph carries no information and
	// makes every traversal expensive. The tax-deadline line shares no subject
	// with the other three, so it must not be linked to them.
	var taxID uuid.UUID

	for _, m := range resp.Memories {
		id, err := uuid.Parse(m.Id)
		if err != nil {
			t.Fatalf("memory id %q is not a uuid: %v", m.Id, err)
		}

		if strings.Contains(m.Content, "tax filing deadline") {
			taxID = id
		}
	}
	if taxID == uuid.Nil {
		t.Fatal("the unrelated tax memory was not extracted; the isolation check cannot run")
	}

	edges, err := repo.GetContextEdges(ctx, []uuid.UUID{taxID})
	if err != nil {
		t.Fatalf("get context edges: %v", err)
	}
	for _, e := range edges[taxID] {
		t.Errorf("the tax-deadline memory was linked to %q: "+
			"linking unrelated memories makes the graph dense and uninformative",
			e.TargetContent)
	}
}
