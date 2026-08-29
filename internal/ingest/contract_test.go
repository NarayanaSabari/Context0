package ingest

import (
	"context"
	"errors"
	"testing"

	"github.com/NarayanaSabari/Kora/internal/extraction"
	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// The write path's promises, as tests.
//
// Mutation testing found five of them unprotected: contradiction detection
// running for the wrong memory types, a failed candidate lookup taking the
// write with it, a failed embedding batch doing the same, duplicate content
// hashes being looked up repeatedly, and New() ignoring a nil extractor.
// Every one of those is a comment in the source promising behaviour that no
// test checked.
//
// These use the Repo seam rather than a database. The question is what the
// engine does when a dependency misbehaves, which is hard to arrange in
// Postgres and trivial here.

type recordingRepo struct {
	createErr    error
	queryErr     error
	queryResults []model.MemoryWithContext
	hashErr      error
	hashResult   map[graph.ContentKey]model.Memory

	created         []model.Memory
	queryCalls      int
	hashLookups     [][]string
	embeddingsSaved int
}

func (r *recordingRepo) CreateMemory(_ context.Context, mem model.Memory) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, mem)
	return nil
}
func (r *recordingRepo) CreateEdges(context.Context, []model.Edge) error { return nil }
func (r *recordingRepo) StoreEmbedding(context.Context, uuid.UUID, string, []float32) error {
	r.embeddingsSaved++
	return nil
}
func (r *recordingRepo) LinkMemoryToSession(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (r *recordingRepo) LinkEntities(context.Context, model.Memory, []string) (int, error) {
	return 0, nil
}
func (r *recordingRepo) FindByContentHash(_ context.Context, _ string, hashes []string) (map[graph.ContentKey]model.Memory, error) {
	r.hashLookups = append(r.hashLookups, hashes)
	return r.hashResult, r.hashErr
}
func (r *recordingRepo) UpdateMemoryContent(context.Context, uuid.UUID, string, string) (bool, error) {
	return true, nil
}
func (r *recordingRepo) QueryMemories(context.Context, graph.QueryFilter) ([]model.MemoryWithContext, error) {
	r.queryCalls++
	return r.queryResults, r.queryErr
}
func (r *recordingRepo) SearchByVector(context.Context, []float32, string, int) ([]model.MemoryWithContext, error) {
	return nil, nil
}
func (r *recordingRepo) IncrementAccessCounts(context.Context, []uuid.UUID) error { return nil }

type stubEmbedder struct{ err error }

func (e stubEmbedder) Embed(string) ([]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	return make([]float32, 8), nil
}
func (e stubEmbedder) Dimension() int { return 8 }

// Contradiction detection runs for semantic memories only.
//
// An episodic memory records that something happened; a later event does not
// contradict an earlier one, it follows it. Running detection over episodes
// would supersede yesterday's walk with today's, and a supersedes edge is
// destructive to ranking order.
func TestDetectAndSupersede_OnlySemanticMemoriesAreCandidates(t *testing.T) {
	for _, tt := range []struct {
		name       string
		memType    model.MemoryType
		wantLookup bool
	}{
		{"semantic memories can contradict each other", model.MemoryTypeSemantic, true},
		{"episodic memories cannot: a later event follows an earlier one", model.MemoryTypeEpisodic, false},
		{"procedural memories cannot", model.MemoryTypeProcedural, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &recordingRepo{}
			eng := New(repo, nil, nil)

			eng.detectAndSupersede(context.Background(), model.Memory{
				ID: uuid.New(), Type: tt.memType, ProjectID: "proj", Content: "Caroline lives in Lisbon",
			})

			if got := repo.queryCalls > 0; got != tt.wantLookup {
				t.Errorf("candidate lookup ran = %v, want %v for a %s memory", got, tt.wantLookup, tt.memType)
			}
		})
	}
}

// A failed candidate lookup skips contradiction detection; it does not fail
// the write. The memory is already committed at this point, and losing a
// supersedes edge is a worse answer later, while failing here would be a lost
// write now.
func TestDetectAndSupersede_LookupFailureIsNotFatal(t *testing.T) {
	repo := &recordingRepo{queryErr: errors.New("graph unreachable")}
	eng := New(repo, nil, nil)

	superseded := eng.detectAndSupersede(context.Background(), model.Memory{
		ID: uuid.New(), Type: model.MemoryTypeSemantic, ProjectID: "proj", Content: "Caroline lives in Lisbon",
	})
	if len(superseded) != 0 {
		t.Errorf("got %d supersedes with a failed lookup; nothing could have been compared", len(superseded))
	}
}

// Content hashes are deduplicated before the lookup.
//
// A conversation that states the same fact twice would otherwise send the same
// hash twice, and the lookup is one query whose size is the number of hashes:
// the deduplication is what keeps a repetitive transcript from paying for its
// repetition.
func TestExistingByContent_LooksUpEachHashOnce(t *testing.T) {
	repo := &recordingRepo{hashResult: map[graph.ContentKey]model.Memory{}}
	eng := New(repo, nil, nil)

	same := "Caroline adopted a rescue dog named Biscuit."
	eng.existingByContent(context.Background(), "proj", []model.Memory{
		{ID: uuid.New(), Content: same},
		{ID: uuid.New(), Content: same},
		{ID: uuid.New(), Content: "Melanie signed up for a pottery class."},
		{ID: uuid.New(), Content: ""},
	})

	if len(repo.hashLookups) != 1 {
		t.Fatalf("made %d lookups, want 1: the batch exists to be one round trip", len(repo.hashLookups))
	}
	if got := len(repo.hashLookups[0]); got != 2 {
		t.Errorf("looked up %d hashes for two distinct non-empty contents (one stated twice): %v",
			got, repo.hashLookups[0])
	}
}

// A failed embedding batch loses the vectors, not the memories.
//
// Without an embedding a memory is absent from vector search and still
// findable by keyword and entity. Failing the write instead would discard
// facts the caller has already been told were extracted.
func TestEmbedExtracted_ProviderFailureLosesVectorsNotMemories(t *testing.T) {
	repo := &recordingRepo{}
	eng := New(repo, stubEmbedder{err: errors.New("provider down")}, nil)

	memories := []model.Memory{
		{ID: uuid.New(), Content: "Caroline adopted a rescue dog"},
		{ID: uuid.New(), Content: "Melanie took up pottery"},
	}
	vectors := eng.embedExtracted(context.Background(), memories)

	if len(vectors) != 0 {
		t.Errorf("got %d vectors from a failing provider, want none", len(vectors))
	}
}

// A nil extractor means the rule-based one, not a nil dereference on the first
// conversation.
func TestNew_NilExtractorFallsBackToTheRuleBasedOne(t *testing.T) {
	eng := New(&recordingRepo{}, nil, nil)
	if eng.extractor == nil {
		t.Fatal("New(nil extractor) left the engine without one")
	}
	if _, ok := eng.extractor.(extraction.RuleExtractor); !ok {
		t.Errorf("got %T, want the rule-based extractor", eng.extractor)
	}
}

// The other half of each contract above: the path where nothing fails.
//
// A test that only covers the failure passes whether or not the success is
// wired up at all, which is how a feature ends up looking guarded while being
// unreachable.

// A contradiction that is found produces a supersedes edge.
func TestDetectAndSupersede_FindsAContradiction(t *testing.T) {
	older := model.Memory{
		ID: uuid.New(), Type: model.MemoryTypeSemantic, ProjectID: "proj",
		Content: "Caroline is vegetarian.",
	}
	repo := &recordingRepo{queryResults: []model.MemoryWithContext{{Memory: older}}}
	eng := New(repo, nil, nil)

	superseded := eng.detectAndSupersede(context.Background(), model.Memory{
		ID: uuid.New(), Type: model.MemoryTypeSemantic, ProjectID: "proj",
		Content: "Caroline is not vegetarian.",
	})

	if len(superseded) == 0 {
		t.Fatal("a direct negation of an existing fact produced no supersedes: " +
			"contradiction detection ran and found nothing, or did not run")
	}
	if !superseded[older.ID] {
		t.Errorf("superseded %v, want the contradicted memory %s", superseded, older.ID)
	}
}

// A working embedder produces one vector per memory.
func TestEmbedExtracted_ReturnsAVectorPerMemory(t *testing.T) {
	eng := New(&recordingRepo{}, stubEmbedder{}, nil)

	memories := []model.Memory{
		{ID: uuid.New(), Content: "Caroline adopted a rescue dog"},
		{ID: uuid.New(), Content: "Melanie took up pottery"},
	}
	vectors := eng.embedExtracted(context.Background(), memories)

	if len(vectors) != len(memories) {
		t.Fatalf("got %d vectors for %d memories", len(vectors), len(memories))
	}
	for _, m := range memories {
		if len(vectors[m.ID]) == 0 {
			t.Errorf("memory %s got no vector; it would be absent from vector search", m.ID)
		}
	}
}

// An extractor that was supplied is the one used.
func TestNew_KeepsTheExtractorItWasGiven(t *testing.T) {
	want := extraction.RuleExtractor{}
	eng := New(&recordingRepo{}, nil, stubExtractor{})
	if _, isRule := eng.extractor.(extraction.RuleExtractor); isRule {
		t.Errorf("New replaced the supplied extractor with %T", want)
	}
}

// stubExtractor is distinguishable from the rule-based extractor by type.
type stubExtractor struct{}

func (stubExtractor) Extract(string) ([]extraction.ExtractedMemory, error) { return nil, nil }
