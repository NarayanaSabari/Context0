package retrieval

import (
	"testing"

	"github.com/NarayanaSabari/Kora/internal/ranking"
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

// TestMergeResults_CarriesRelevanceForward guards the contract between
// retrieval and ranking: whatever the two retrievers determined about query
// match quality must survive the merge in the Relevance field, because that is
// the only channel ranking reads.
func TestMergeResults_CarriesRelevanceForward(t *testing.T) {
	graphOnly := uuid.New()
	vectorOnly := uuid.New()
	both := uuid.New()

	// Keyword hits carry the raw ts_rank_cd in Score and its normalised
	// form in Relevance, as Retrieve sets them; min-max fusion reads Score.
	graphResults := []model.MemoryWithContext{
		{Memory: model.Memory{ID: graphOnly}, Score: 0.8, Relevance: 0.8},
		{Memory: model.Memory{ID: both}, Score: 0.6, Relevance: 0.6},
	}
	// The repository reports cosine similarity in Score, not Relevance. A
	// third, weaker candidate keeps "both" off the bottom of the pool, where
	// min-max would grade its cosine at zero.
	weakest := uuid.New()
	vectorResults := []model.MemoryWithContext{
		{Memory: model.Memory{ID: vectorOnly}, Score: 0.7},
		{Memory: model.Memory{ID: both}, Score: 0.5},
		{Memory: model.Memory{ID: weakest}, Score: 0.3},
	}

	merged := mergeResults(graphResults, vectorResults, nil, nil, DefaultFusion())

	if len(merged) != 4 {
		t.Fatalf("expected 4 deduplicated results, got %d", len(merged))
	}

	byID := make(map[uuid.UUID]model.MemoryWithContext, len(merged))
	for _, m := range merged {
		byID[m.Memory.ID] = m
	}

	// Each signal is rescaled onto [0, 1] within its own pool before the
	// weighted sum, and the keyword weight is at least the semantic one, so
	// the pool's best lexical match outranks the pool's best vector-only
	// hit. Assert the ordering the ranking layer relies on, not the specific
	// arithmetic.
	if byID[graphOnly].Relevance <= byID[vectorOnly].Relevance {
		t.Errorf("the best keyword match (%f) must outrank a vector-only hit (%f)",
			byID[graphOnly].Relevance, byID[vectorOnly].Relevance)
	}
	if byID[both].Relevance <= byID[weakest].Relevance {
		t.Errorf("a memory found by both retrievers (%f) must outrank the weakest vector-only hit (%f)",
			byID[both].Relevance, byID[weakest].Relevance)
	}

	// Agreement between retrievers must still lift a memory above the same
	// memory matched lexically alone.
	lexicalOnly := mergeResults(
		[]model.MemoryWithContext{{Memory: model.Memory{ID: both}, Score: 0.6, Relevance: 0.6}},
		nil, nil, nil, DefaultFusion(),
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

	first := mergeResults(graphResults, nil, nil, nil, DefaultFusion())
	for i := 0; i < 20; i++ {
		got := mergeResults(graphResults, nil, nil, nil, DefaultFusion())
		for j := range got {
			if got[j].Memory.ID != first[j].Memory.ID {
				t.Fatalf("mergeResults order varies between identical calls at %d", j)
			}
		}
	}
}

// TestExactKeywordMatchOutranksVectorOnlyResult reproduces a correctness
// violation observed in a soak run: a memory was not returned by a query for a
// keyword that appears verbatim in its own content, while unrelated memories
// were.
//
// The cause was a units mismatch at the merge. The graph retriever filters by
// keyword, so every hit it returns genuinely contains the term, and graded
// them at 0.75 for a content hit; the vector retriever ran unfiltered and
// passed raw cosine similarity through, and a bag-of-words embedding
// routinely puts near-duplicate sentences above 0.85. A memory that did not
// contain the keyword at all entered the merged set at 0.87 and outranked
// one that did at 0.75; with top_k truncation the exact match was discarded
// and the write appeared unreadable by its own text.
//
// Under min-max fusion the same guarantee rests on the weights: both signals
// are rescaled onto [0, 1] within their pools, and the keyword weight is at
// least the semantic weight, so the best lexical match cannot lose to a
// candidate with no lexical evidence however confident the embedder is.
func TestExactKeywordMatchOutranksVectorOnlyResult(t *testing.T) {
	exact := model.MemoryWithContext{
		Memory: model.Memory{
			ID:      uuid.New(),
			Content: "soak kjgzoaii about prometheus metrics collection",
		},
	}
	// Graded the way Retrieve grades keyword hits: the raw ts_rank_cd of a
	// single matched term in Score, its normalised form in Relevance.
	exact.Score = 0.1
	exact.Relevance = ranking.NormalizeBM25(exact.Score, 1)

	// A vector-only hit: semantically near, but it does not contain the term.
	// 0.8715 is a real score observed from this deployment.
	vectorOnly := model.MemoryWithContext{
		Memory: model.Memory{ID: uuid.New(), Content: "I prefer prometheus for this"},
		Score:  0.8715425782901834,
	}

	merged := mergeResults(
		[]model.MemoryWithContext{exact},
		[]model.MemoryWithContext{vectorOnly},
		nil, nil, DefaultFusion(),
	)

	var exactRel, vectorRel float64
	for _, r := range merged {
		switch r.Memory.ID {
		case exact.Memory.ID:
			exactRel = r.Relevance
		case vectorOnly.Memory.ID:
			vectorRel = r.Relevance
		}
	}

	if exactRel <= vectorRel {
		t.Errorf("a memory containing the query term verbatim scored %.4f, "+
			"below a memory that does not contain it at all (%.4f): "+
			"lexical and cosine relevance are not on the same scale",
			exactRel, vectorRel)
	}
}
