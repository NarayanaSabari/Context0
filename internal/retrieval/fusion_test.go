package retrieval

import (
	"math"
	"testing"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

func relevanceOf(results []model.MemoryWithContext, id uuid.UUID) float64 {
	for _, r := range results {
		if r.Memory.ID == id {
			return r.Relevance
		}
	}
	return -1
}

// TestMergeResults_MinMaxRescalesPerQuery is the failure the min-max mode
// exists to fix. Two candidates: one the keyword retriever scored highest on
// common words, one the vector retriever placed first on meaning. Under the
// original fusion the keyword candidate wins on a saturated lexical score;
// under min-max the semantic candidate's cosine is rescaled to the top of the
// pool and can compete.
func TestMergeResults_MinMaxRescalesPerQuery(t *testing.T) {
	lexical, semantic := uuid.New(), uuid.New()

	// Keyword pool best-first. Raw ts_rank_cd in Score, sigmoid-normalised
	// relevance in Relevance, as the caller sets them.
	graph := []model.MemoryWithContext{
		{Memory: model.Memory{ID: lexical}, Score: 0.57, Relevance: 0.99},
		{Memory: model.Memory{ID: semantic}, Score: 0.07, Relevance: 0.19},
	}
	// Vector pool nearest-first. The semantic candidate is far ahead in
	// cosine terms, but by only 0.15 in absolute terms.
	vector := []model.MemoryWithContext{
		{Memory: model.Memory{ID: semantic}, Score: 0.75},
		{Memory: model.Memory{ID: lexical}, Score: 0.60},
	}

	f := DefaultFusion()
	f.Mode = FusionLinear
	linear := mergeResults(graph, vector, nil, nil, 0, f)
	f.Mode = FusionMinMax
	minmax := mergeResults(graph, vector, nil, nil, 0, f)

	if relevanceOf(linear, lexical) <= relevanceOf(linear, semantic) {
		t.Fatalf("precondition: linear fusion should favour the lexical candidate, got %v vs %v",
			relevanceOf(linear, lexical), relevanceOf(linear, semantic))
	}
	// The semantic candidate is the best of the vector pool, so its cosine
	// rescales to 1 and the lexical candidate's to 0; the lexical candidate's
	// keyword score is the pool's best and the semantic candidate's is
	// 0.07/0.57 of it.
	wk, ws, _ := f.Keyword, f.Semantic, f.Entity
	wantSemantic := (wk*(0.07/0.57) + ws*1) / (wk + ws + f.Entity)
	wantLexical := (wk*1 + ws*0) / (wk + ws + f.Entity)
	if got := relevanceOf(minmax, semantic); math.Abs(got-wantSemantic) > 1e-9 {
		t.Errorf("min-max relevance of the semantic candidate = %v, want %v", got, wantSemantic)
	}
	if got := relevanceOf(minmax, lexical); math.Abs(got-wantLexical) > 1e-9 {
		t.Errorf("min-max relevance of the lexical candidate = %v, want %v", got, wantLexical)
	}
}

// TestMergeResults_MinMaxWithoutVectorPool: with no embedder the cosine
// signal is absent for every candidate, and keyword evidence alone must still
// rank the pool rather than collapsing to a constant.
func TestMergeResults_MinMaxWithoutVectorPool(t *testing.T) {
	best, weaker := uuid.New(), uuid.New()
	graph := []model.MemoryWithContext{
		{Memory: model.Memory{ID: best}, Score: 0.5, Relevance: 0.9},
		{Memory: model.Memory{ID: weaker}, Score: 0.25, Relevance: 0.5},
	}
	f := DefaultFusion()
	f.Mode = FusionMinMax
	merged := mergeResults(graph, nil, nil, nil, 0, f)
	if relevanceOf(merged, best) <= relevanceOf(merged, weaker) {
		t.Errorf("keyword-only min-max should keep the pool's order: %v vs %v",
			relevanceOf(merged, best), relevanceOf(merged, weaker))
	}
	if got := relevanceOf(merged, best); got <= 0 {
		t.Errorf("the best keyword candidate scored %v; an absent vector pool must not zero the keyword signal", got)
	}
}

// TestMergeResults_RRFUsesRanksNotScores: two candidates whose scores differ
// by a hair and by a mile must fuse identically under RRF, because only the
// order within each retriever counts.
func TestMergeResults_RRFUsesRanksNotScores(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	f := DefaultFusion()
	f.Mode = FusionRRF
	// Equal weights, so mirrored ranks tie exactly.
	f.Keyword, f.Semantic = 1, 1

	close := mergeResults(
		[]model.MemoryWithContext{{Memory: model.Memory{ID: a}, Score: 0.50}, {Memory: model.Memory{ID: b}, Score: 0.49}},
		[]model.MemoryWithContext{{Memory: model.Memory{ID: b}, Score: 0.80}, {Memory: model.Memory{ID: a}, Score: 0.79}},
		nil, nil, 0, f)
	far := mergeResults(
		[]model.MemoryWithContext{{Memory: model.Memory{ID: a}, Score: 0.90}, {Memory: model.Memory{ID: b}, Score: 0.01}},
		[]model.MemoryWithContext{{Memory: model.Memory{ID: b}, Score: 0.95}, {Memory: model.Memory{ID: a}, Score: 0.10}},
		nil, nil, 0, f)

	for _, id := range []uuid.UUID{a, b} {
		if relevanceOf(close, id) != relevanceOf(far, id) {
			t.Errorf("RRF relevance depends on score magnitude: %v vs %v", relevanceOf(close, id), relevanceOf(far, id))
		}
	}
	// Symmetric ranks, so the two candidates tie exactly.
	if relevanceOf(close, a) != relevanceOf(close, b) {
		t.Errorf("symmetric ranks should tie under RRF: %v vs %v", relevanceOf(close, a), relevanceOf(close, b))
	}
	if got := relevanceOf(close, a); got <= 0 || got > 1 {
		t.Errorf("RRF relevance %v outside (0, 1]", got)
	}
}

// TestSetFusion_RejectsNonsense: the switch is startup-only and a bad value
// must be refused there rather than discovered as a zero-relevance query.
func TestSetFusion_RejectsNonsense(t *testing.T) {
	e := New(nil, nil)
	if err := e.SetFusion(Fusion{Mode: "tiered", Keyword: 1, Semantic: 1, Entity: 1, RRFK: 60}); err == nil {
		t.Error("unknown mode accepted")
	}
	if err := e.SetFusion(Fusion{Mode: FusionMinMax, Keyword: 0, Semantic: 0, Entity: 0}); err == nil {
		t.Error("all-zero weights accepted")
	}
	if err := e.SetFusion(Fusion{Mode: FusionMinMax, Keyword: -1, Semantic: 1, Entity: 0}); err == nil {
		t.Error("negative weight accepted")
	}
	if err := e.SetFusion(Fusion{Mode: FusionRRF, Keyword: 1, Semantic: 1, Entity: 0, RRFK: 0}); err == nil {
		t.Error("non-positive rrf k accepted")
	}
	if err := e.SetFusion(DefaultFusion()); err != nil {
		t.Errorf("the default fusion was rejected: %v", err)
	}
}
