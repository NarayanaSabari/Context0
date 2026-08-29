package retrieval

import (
	"context"
	"errors"
	"testing"

	"github.com/NarayanaSabari/Kora/internal/graph"
	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

// The read path promises, in comments at each site, that a failing retriever
// degrades rather than fails the query, and that one particular failure must
// *not* degrade. Mutation testing found every one of those promises
// unprotected: forcing each error branch to fire changed nothing any test
// noticed.
//
// These are the contracts, as tests. They use the Repo seam rather than a
// database, because the question is what the engine does when a dependency
// misbehaves, and a real Postgres is hard to make misbehave on demand.
//
// One mutant in this file's target survives on purpose and cannot be killed:
// forcing the `verr != nil` branch after SearchByVector emits a log line and
// changes nothing else, because the results are assigned before the check. It
// is an equivalent mutant, not a gap; do not write a test for it.

// fakeRepo answers with whatever the test sets, and records what it was asked.
type fakeRepo struct {
	textResults   []model.MemoryWithContext
	textErr       error
	searchable    bool
	searchableErr error
	queryResults  []model.MemoryWithContext
	queryErr      error
	vectorResults []model.MemoryWithContext
	vectorErr     error
	entityErr     error

	queryMemoriesCalled bool
	searchByVectorCalls int
}

func (f *fakeRepo) SearchByText(context.Context, string, []string, int) ([]model.MemoryWithContext, error) {
	return f.textResults, f.textErr
}

func (f *fakeRepo) KeywordsAreSearchable(context.Context, []string) (bool, error) {
	return f.searchable, f.searchableErr
}

func (f *fakeRepo) QueryMemories(context.Context, graph.QueryFilter) ([]model.MemoryWithContext, error) {
	f.queryMemoriesCalled = true
	return f.queryResults, f.queryErr
}

func (f *fakeRepo) SearchByVector(context.Context, []float32, string, int) ([]model.MemoryWithContext, error) {
	f.searchByVectorCalls++
	return f.vectorResults, f.vectorErr
}

func (f *fakeRepo) FindMemoriesByEntities(context.Context, string, []string, int) ([]model.Memory, error) {
	return nil, f.entityErr
}

func (f *fakeRepo) GetMemoryEntities(context.Context, []uuid.UUID) (map[uuid.UUID][]string, error) {
	return nil, nil
}

// fixedEmbedder returns a constant vector, or an error.
type fixedEmbedder struct{ err error }

func (e fixedEmbedder) Embed(string) ([]float32, error) {
	if e.err != nil {
		return nil, e.err
	}
	return make([]float32, 8), nil
}
func (e fixedEmbedder) Dimension() int { return 8 }

func memory(content string) model.MemoryWithContext {
	return model.MemoryWithContext{
		Memory: model.Memory{ID: uuid.New(), Content: content, Type: model.MemoryTypeSemantic, DecayScore: 1},
	}
}

// Keyword search is the one retriever whose failure is fatal.
//
// It is not a degradation: the other two exist to cover its gaps, not to
// stand in for it, and answering a keyword query from vector similarity alone
// would be a different answer presented as the same one.
func TestRetrieve_KeywordFailureIsAnError(t *testing.T) {
	repo := &fakeRepo{textErr: errors.New("postgres is on fire")}
	_, err := New(repo, fixedEmbedder{}).Retrieve(context.Background(), "anything at all", "proj", nil, 5)
	if err == nil {
		t.Fatal("keyword search failed and Retrieve returned no error: the caller cannot " +
			"tell a broken retriever from a project with nothing in it")
	}
}

// A failed searchability check must not trigger the fallback.
//
// The fallback answers "this query had nothing to search for" with the
// project's recent memories. Reaching it because the *check* failed would turn
// a precise empty answer -- the query ran, nothing matched -- into a page of
// unrelated memories, which is the exact behaviour full-text search replaced.
func TestRetrieve_UnknownSearchabilityDoesNotFallBack(t *testing.T) {
	repo := &fakeRepo{
		textResults:   nil, // a real search that matched nothing
		searchableErr: errors.New("cannot reach the dictionary"),
		queryResults:  []model.MemoryWithContext{memory("something unrelated")},
	}

	results, err := New(repo, nil).Retrieve(context.Background(), "zqxjklmw", "proj", nil, 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if repo.queryMemoriesCalled {
		t.Error("the searchability check failed and the fallback ran anyway: " +
			"an unanswerable check must be treated as a real search, not as an empty one")
	}
	if len(results) != 0 {
		t.Errorf("got %d results for a search that matched nothing; a precise empty "+
			"answer became a page of unrelated memories", len(results))
	}
}

// A query with nothing to search for is answered by the fallback, and a
// failure there is fatal: at that point there is no other source of candidates.
func TestRetrieve_FallbackFailureIsAnError(t *testing.T) {
	repo := &fakeRepo{queryErr: errors.New("graph query failed")}
	_, err := New(repo, nil).Retrieve(context.Background(), "", "proj", nil, 5)
	if err == nil {
		t.Fatal("the fallback failed and Retrieve returned no error")
	}
}

// Vector search failing is a degradation, not a failure: the caller gets a
// quietly worse answer built from the retrievers that did work.
func TestRetrieve_VectorFailureDegradesToKeywordResults(t *testing.T) {
	want := memory("Caroline adopted a rescue dog named Biscuit")
	repo := &fakeRepo{
		textResults: []model.MemoryWithContext{want},
		vectorErr:   errors.New("pgvector unavailable"),
	}

	results, err := New(repo, fixedEmbedder{}).Retrieve(context.Background(), "Biscuit", "proj", nil, 5)
	if err != nil {
		t.Fatalf("vector search failed and took the whole query with it: %v", err)
	}
	if len(results) != 1 || results[0].Memory.ID != want.Memory.ID {
		t.Errorf("got %d results; keyword evidence must survive a failed vector search", len(results))
	}
}

// An embedder that errors is the same degradation, one step earlier.
func TestRetrieve_EmbedderFailureDegradesToKeywordResults(t *testing.T) {
	want := memory("Caroline adopted a rescue dog named Biscuit")
	repo := &fakeRepo{textResults: []model.MemoryWithContext{want}}

	results, err := New(repo, fixedEmbedder{err: errors.New("provider down")}).
		Retrieve(context.Background(), "Biscuit", "proj", nil, 5)
	if err != nil {
		t.Fatalf("an unreachable embedding provider failed the query: %v", err)
	}
	if repo.searchByVectorCalls != 0 {
		t.Error("the query could not be embedded and vector search ran anyway")
	}
	if len(results) != 1 {
		t.Errorf("got %d results; a failed embedding must not lose the keyword hits", len(results))
	}
}

// No embedder configured is a supported deployment, not a degraded one: it is
// what a default install runs. Vector search must not be attempted at all.
func TestRetrieve_NoEmbedderSkipsVectorSearchEntirely(t *testing.T) {
	repo := &fakeRepo{textResults: []model.MemoryWithContext{memory("a stored fact")}}

	results, err := New(repo, nil).Retrieve(context.Background(), "fact", "proj", nil, 5)
	if err != nil {
		t.Fatalf("Retrieve without an embedder: %v", err)
	}
	if repo.searchByVectorCalls != 0 {
		t.Errorf("vector search was called %d times with no embedder configured", repo.searchByVectorCalls)
	}
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}

// Entity retrieval failing is the mildest degradation: it is supplementary, so
// losing it makes the answer quietly worse rather than wrong.
func TestRetrieve_EntityFailureDegrades(t *testing.T) {
	want := memory("Caroline works from Lisbon")
	repo := &fakeRepo{
		textResults: []model.MemoryWithContext{want},
		entityErr:   errors.New("entity lookup failed"),
	}

	results, err := New(repo, nil).Retrieve(context.Background(), "Where does Caroline work?", "proj", nil, 5)
	if err != nil {
		t.Fatalf("entity retrieval failed and took the query with it: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results; the other retrievers' evidence must survive", len(results))
	}
}

// The other half of the searchability contract: when the check succeeds and
// says the query has nothing to search for, the fallback *does* run.
//
// Both halves are needed. A test that only covers the failing check passes
// whether or not the successful one is wired up, which is how the fallback
// could be silently unreachable while looking guarded.
func TestRetrieve_UnsearchableQueryFallsBackToRecentMemories(t *testing.T) {
	fallback := memory("the project's most recent memory")
	repo := &fakeRepo{
		textResults:  nil,   // "the" matches nothing
		searchable:   false, // and PostgreSQL confirms there was nothing to match
		queryResults: []model.MemoryWithContext{fallback},
	}

	results, err := New(repo, nil).Retrieve(context.Background(), "the and of", "proj", nil, 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !repo.queryMemoriesCalled {
		t.Error("a query with nothing to search for did not reach the fallback: " +
			"a bare 'list everything' request must be answered by recency, not by nothing")
	}
	if len(results) != 1 || results[0].Memory.ID != fallback.Memory.ID {
		t.Errorf("got %d results; the fallback's memories must reach the caller", len(results))
	}
}

// Vector results reach the caller when everything works.
//
// The failure tests above all assert that a broken retriever does not lose the
// others' evidence, which passes trivially if the retriever never contributes
// anything. This is the case that fails if vector retrieval is disconnected.
func TestRetrieve_VectorOnlyHitsReachTheCaller(t *testing.T) {
	lexical := memory("Caroline uses PostgreSQL for the primary datastore")
	// Score carries the cosine on a vector hit, and it has to be a plausible
	// one: a vector-only candidate has no other evidence, so the semantic gate
	// judges it on this number alone.
	semantic := memory("the team moved off MySQL last spring")
	semantic.Score = 0.8
	repo := &fakeRepo{
		textResults:   []model.MemoryWithContext{lexical},
		vectorResults: []model.MemoryWithContext{semantic},
	}

	results, err := New(repo, fixedEmbedder{}).Retrieve(context.Background(), "PostgreSQL", "proj", nil, 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if repo.searchByVectorCalls != 1 {
		t.Fatalf("vector search ran %d times, want 1", repo.searchByVectorCalls)
	}

	var sawSemantic bool
	for _, r := range results {
		if r.Memory.ID == semantic.Memory.ID {
			sawSemantic = true
		}
	}
	if !sawSemantic {
		t.Error("a memory only vector search found did not reach the caller: " +
			"the retriever ran and its results were discarded")
	}
}

// The semantic gate: a memory only vector search found, and found weakly, is
// dropped rather than returned.
//
// This is the counterpart to the test above. Vector search returns its top-K
// whatever the similarity, so without a floor a query with no good semantic
// match still gets one, and the caller cannot tell the difference. A candidate
// another retriever also found is not gated, because it was retrieved on
// evidence the gate says nothing about.
func TestRetrieve_WeakVectorOnlyHitsAreGated(t *testing.T) {
	weak := memory("an unrelated memory the embedder placed vaguely nearby")
	weak.Score = 0.02

	repo := &fakeRepo{vectorResults: []model.MemoryWithContext{weak}}

	results, err := New(repo, fixedEmbedder{}).Retrieve(context.Background(), "something specific", "proj", nil, 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	for _, r := range results {
		if r.Memory.ID == weak.Memory.ID {
			t.Error("a vector-only hit below the semantic gate was returned: " +
				"a query with no good match must come back empty rather than plausible")
		}
	}
}
