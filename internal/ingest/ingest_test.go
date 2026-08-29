package ingest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/google/uuid"
)

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
	eng := New(repo, embedder, nil)

	var sb strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&sb, "Caroline decided to adopt rescue dog number %d in Lisbon.\n", i)
	}

	projectID := fmt.Sprintf("extract-concurrency-%d", time.Now().UnixNano())
	start := time.Now()
	stored, _, err := eng.Extract(ctx, sb.String(), projectID, uuid.Nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	elapsed := time.Since(start)

	// Nothing may be dropped. This is the assertion that fails on the serial
	// implementation once the conversation is long enough.
	if len(stored) != lines {
		t.Errorf("Extract returned %d memories, want %d -- the tail of the "+
			"conversation was dropped", len(stored), lines)
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
	eng := New(repo, embedding.NewBagOfWordsEmbedder(384), nil)

	projectID := fmt.Sprintf("semlink-%d", time.Now().UnixNano())

	// Deliberately free of any term in extractTopics. The first three lines are
	// closely related to each other; the fourth is unrelated, and exists so a
	// passing result cannot come from linking everything to everything.
	stored, relCount, err := eng.Extract(ctx,
		"Caroline said that she adopted a rescue dog named Biscuit last month.\n"+
			"Caroline said that Biscuit is a nervous rescue dog who hates thunderstorms.\n"+
			"Caroline said that she walks her rescue dog Biscuit twice a day.\n"+
			"Melanie said that the quarterly tax filing deadline is in April.",
		projectID, uuid.Nil)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(stored) < 4 {
		t.Fatalf("expected at least 4 memories, got %d", len(stored))
	}

	if relCount == 0 {
		t.Fatalf("a four-line conversation about a dog produced no relationships: "+
			"relatedness must come from meaning, not from a fixed list of technical terms "+
			"(got %d memories)", len(stored))
	}

	// Linking everything to everything would also satisfy the check above, and
	// would be worse than no graph: a dense graph carries no information and
	// makes every traversal expensive. The tax-deadline line shares no subject
	// with the other three, so it must not be linked to them.
	var taxID uuid.UUID

	for _, m := range stored {
		if strings.Contains(m.Content, "tax filing deadline") {
			taxID = m.ID
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
