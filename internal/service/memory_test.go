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

	"github.com/google/uuid"
)

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

// A profile's size and its notion of "recent" are the caller's to choose.
//
// Both were hardcoded at 200 memories and 7 days, which made this engine's
// opinion about how long something stays current into everyone's. The clamps
// follow the contract top_k settled on: a request above the maximum is served
// at the maximum rather than refused, because a profile is a convenience view
// and failing one over an ambitious number helps nobody.
func TestProfileSizing(t *testing.T) {
	memories := []struct {
		name      string
		requested int32
		want      int32
	}{
		{"unset falls back to the documented default", 0, defaultProfileMemories},
		{"negative is treated as unset, not as an error", -1, defaultProfileMemories},
		{"a modest request is honoured exactly", 50, 50},
		{"the maximum is honoured exactly", maxProfileMemories, maxProfileMemories},
		{"above the maximum is served at the maximum", maxProfileMemories + 5000, maxProfileMemories},
	}
	for _, tt := range memories {
		t.Run("budget/"+tt.name, func(t *testing.T) {
			if got := profileMemoryBudget(tt.requested); got != tt.want {
				t.Errorf("profileMemoryBudget(%d) = %d, want %d", tt.requested, got, tt.want)
			}
		})
	}

	days := []struct {
		name      string
		requested int32
		want      int
	}{
		{"unset falls back to a week", 0, defaultProfileRecencyDays},
		{"negative is treated as unset", -30, defaultProfileRecencyDays},
		{"a day is a legitimate window", 1, 1},
		{"a year is the maximum", maxProfileRecencyDays, maxProfileRecencyDays},
		{"beyond a year is served at a year", 4000, maxProfileRecencyDays},
	}
	for _, tt := range days {
		t.Run("recency/"+tt.name, func(t *testing.T) {
			if got := profileRecencyDays(tt.requested); got != tt.want {
				t.Errorf("profileRecencyDays(%d) = %d, want %d", tt.requested, got, tt.want)
			}
		})
	}
}
