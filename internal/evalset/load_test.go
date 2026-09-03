package evalset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NarayanaSabari/Kora/internal/embedding"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// fakeStore records what LoadCorpus wrote, without a database.
type fakeStore struct {
	mu       sync.Mutex
	memories []model.Memory
	// embeddings and linked are keyed by memory id so a test can look up
	// what was written for one particular doc.
	embeddings map[uuid.UUID][]float32
	linked     map[uuid.UUID][]string
}

func (s *fakeStore) CreateMemory(ctx context.Context, mem model.Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memories = append(s.memories, mem)
	return nil
}

func (s *fakeStore) StoreEmbedding(ctx context.Context, memoryID uuid.UUID, projectID string, embedding []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.embeddings == nil {
		s.embeddings = make(map[uuid.UUID][]float32)
	}
	s.embeddings[memoryID] = embedding
	return nil
}

func (s *fakeStore) LinkEntities(ctx context.Context, mem model.Memory, names []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.linked == nil {
		s.linked = make(map[uuid.UUID][]string)
	}
	s.linked[mem.ID] = names
	return len(names), nil
}

// fakeEmbedder returns a fixed-width zero vector for every text, except one
// that is configured to fail, so a test can pin what happens partway through
// a load.
type fakeEmbedder struct {
	dim    int
	failOn string
}

var _ embedding.Embedder = (*fakeEmbedder)(nil)

func (e *fakeEmbedder) Embed(text string) ([]float32, error) {
	if e.failOn != "" && text == e.failOn {
		return nil, errors.New("embedder refused this text")
	}
	return make([]float32, e.dim), nil
}

func (e *fakeEmbedder) Dimension() int { return e.dim }

// TestLoadCorpus_WritesInOrderWithDefaults pins the write shape LoadCorpus
// promises: docs written in corpus order with a fresh memory's defaults, one
// embedding per doc, and entity links only for docs that actually carry
// entities.
func TestLoadCorpus_WritesInOrderWithDefaults(t *testing.T) {
	doc0 := Doc{ID: uuid.New(), Conversation: "conv-1", Content: "first", Type: model.MemoryTypeEpisodic, Entities: []string{"Paris"}, CreatedAt: time.Now()}
	doc1 := Doc{ID: uuid.New(), Conversation: "conv-1", Content: "second", Type: model.MemoryTypeEpisodic, CreatedAt: time.Now()}
	doc2 := Doc{ID: uuid.New(), Conversation: "conv-1", Content: "third", Type: model.MemoryTypeEpisodic, Entities: []string{"Bob", "Alice"}, CreatedAt: time.Now()}
	corpus := &Corpus{Docs: []Doc{doc0, doc1, doc2}}

	store := &fakeStore{}
	stats, err := LoadCorpus(context.Background(), store, corpus, &fakeEmbedder{dim: 4}, nil)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	if stats.Memories != 3 {
		t.Errorf("stats.Memories = %d, want 3", stats.Memories)
	}
	if stats.Entities != 3 { // 1 (doc0) + 0 (doc1) + 2 (doc2)
		t.Errorf("stats.Entities = %d, want 3", stats.Entities)
	}

	if len(store.memories) != 3 {
		t.Fatalf("store recorded %d memories, want 3", len(store.memories))
	}
	for i, want := range []uuid.UUID{doc0.ID, doc1.ID, doc2.ID} {
		if store.memories[i].ID != want {
			t.Errorf("memory %d written was %s, want %s (docs must be written in corpus order)", i, store.memories[i].ID, want)
		}
		if store.memories[i].AccessCount != 0 {
			t.Errorf("memory %d AccessCount = %d, want 0", i, store.memories[i].AccessCount)
		}
		if store.memories[i].DecayScore != 1 {
			t.Errorf("memory %d DecayScore = %v, want 1", i, store.memories[i].DecayScore)
		}
	}

	if len(store.embeddings) != 3 {
		t.Errorf("store recorded %d embeddings, want 3 (one per doc)", len(store.embeddings))
	}

	if _, ok := store.linked[doc0.ID]; !ok {
		t.Error("doc0 has entities but LinkEntities was never called for it")
	}
	if _, ok := store.linked[doc1.ID]; ok {
		t.Error("doc1 has no entities; LinkEntities must not be called for it")
	}
	if _, ok := store.linked[doc2.ID]; !ok {
		t.Error("doc2 has entities but LinkEntities was never called for it")
	}
}

// TestLoadCorpus_ProgressFiresAtMultiplesOf500 pins the reporting cadence a
// long-running load relies on to show it is alive.
func TestLoadCorpus_ProgressFiresAtMultiplesOf500(t *testing.T) {
	docs := make([]Doc, 1000)
	for i := range docs {
		docs[i] = Doc{ID: uuid.New(), Conversation: "conv-1", Content: fmt.Sprintf("doc-%d", i), Type: model.MemoryTypeEpisodic, CreatedAt: time.Now()}
	}
	corpus := &Corpus{Docs: docs}

	var calls [][2]int
	progress := func(done, total int) {
		calls = append(calls, [2]int{done, total})
	}

	stats, err := LoadCorpus(context.Background(), &fakeStore{}, corpus, &fakeEmbedder{dim: 2}, progress)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if stats.Memories != 1000 {
		t.Fatalf("stats.Memories = %d, want 1000", stats.Memories)
	}

	want := [][2]int{{500, 1000}, {1000, 1000}}
	if len(calls) != len(want) {
		t.Fatalf("progress called %d times: %v, want %v", len(calls), calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("progress call %d = %v, want %v", i, calls[i], want[i])
		}
	}
}

// TestLoadCorpus_EmbedderErrorAbortsWithDocID pins that a failed embed stops
// the load immediately, and that the error names which doc failed: with no
// fallback to a model allowed, this is the only signal an operator gets.
func TestLoadCorpus_EmbedderErrorAbortsWithDocID(t *testing.T) {
	doc0 := Doc{ID: uuid.New(), Conversation: "conv-1", Content: "ok-1", Type: model.MemoryTypeEpisodic, CreatedAt: time.Now()}
	doc1 := Doc{ID: uuid.New(), Conversation: "conv-1", Content: "bad-content", Type: model.MemoryTypeEpisodic, CreatedAt: time.Now()}
	doc2 := Doc{ID: uuid.New(), Conversation: "conv-1", Content: "ok-3", Type: model.MemoryTypeEpisodic, CreatedAt: time.Now()}
	corpus := &Corpus{Docs: []Doc{doc0, doc1, doc2}}

	store := &fakeStore{}
	stats, err := LoadCorpus(context.Background(), store, corpus, &fakeEmbedder{dim: 2, failOn: "bad-content"}, nil)
	if err == nil {
		t.Fatal("LoadCorpus with a failing embedder returned nil error")
	}
	if got := err.Error(); !strings.Contains(got, doc1.ID.String()) {
		t.Errorf("error %q does not name the failing doc's id %s", got, doc1.ID)
	}

	// doc0 fully succeeded and doc1's memory was created before its embed
	// failed; doc2 was never reached.
	if stats.Memories != 2 {
		t.Errorf("stats.Memories = %d, want 2", stats.Memories)
	}
	if len(store.memories) != 2 {
		t.Errorf("store recorded %d memories, want 2", len(store.memories))
	}
}
