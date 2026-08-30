package ranking

import (
	"testing"
	"time"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

func TestRecencyFactor(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name      string
		createdAt time.Time
		wantMin   float64
		wantMax   float64
	}{
		{"just created", now, 0.99, 1.01},
		{"1 day ago", now.Add(-24 * time.Hour), 0.98, 1.0},
		{"90 days ago (half-life)", now.Add(-90 * 24 * time.Hour), 0.45, 0.55},
		{"180 days ago", now.Add(-180 * 24 * time.Hour), 0.2, 0.3},
		// The point of the 90-day half-life: a memory a month old is still a
		// live candidate. At the previous 7-day half-life this was ~0.05,
		// which is indistinguishable from a year-old memory.
		{"30 days ago is still substantial", now.Add(-30 * 24 * time.Hour), 0.7, 0.85},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recencyFactor(tt.createdAt, now)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("recencyFactor() = %f, want between %f and %f", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestScore(t *testing.T) {
	now := time.Now().UTC()

	recentSemantic := model.MemoryWithContext{
		Memory: model.Memory{
			ID:          uuid.New(),
			Type:        model.MemoryTypeSemantic,
			CreatedAt:   now,
			AccessCount: 10,
		},
	}

	oldEpisodic := model.MemoryWithContext{
		Memory: model.Memory{
			ID:          uuid.New(),
			Type:        model.MemoryTypeEpisodic,
			CreatedAt:   now.Add(-30 * 24 * time.Hour),
			AccessCount: 0,
		},
	}

	scoreRecent := Score(recentSemantic, now)
	scoreOld := Score(oldEpisodic, now)

	if scoreRecent <= scoreOld {
		t.Errorf("recent semantic (%f) should score higher than old episodic (%f)", scoreRecent, scoreOld)
	}
}

func TestRankResults(t *testing.T) {
	now := time.Now().UTC()

	results := []model.MemoryWithContext{
		{Memory: model.Memory{ID: uuid.New(), Type: model.MemoryTypeEpisodic, CreatedAt: now.Add(-30 * 24 * time.Hour), AccessCount: 0}},
		{Memory: model.Memory{ID: uuid.New(), Type: model.MemoryTypeSemantic, CreatedAt: now, AccessCount: 10}},
		{Memory: model.Memory{ID: uuid.New(), Type: model.MemoryTypeProcedural, CreatedAt: now.Add(-time.Hour), AccessCount: 5}},
	}

	ranked := RankResults(results, 2)

	if len(ranked) != 2 {
		t.Fatalf("expected 2 results, got %d", len(ranked))
	}

	// First result should have the highest score.
	if ranked[0].Score < ranked[1].Score {
		t.Errorf("results not properly ranked: first=%.3f, second=%.3f", ranked[0].Score, ranked[1].Score)
	}
}

// TestScore_RelevanceOutweighsRecency pins the property that makes this a
// memory engine rather than a feed: a memory that actually answers the query
// must outrank a poor match that happens to be newer.
//
// This did not hold. With recencyWeight at 0.25 and a 7-day half-life, recency
// swung the composite by a quarter of its full range while relevance spanned
// only 0.55, so a perfect match a month old scored 0.663 against 0.680 for a
// weak match stored today, and lost. Found while running the LoCoMo benchmark,
// where every memory is ingested seconds apart and the correct answers are the
// oldest rows.
//
// The numbers below are deliberately a worst case: the strongest possible
// relevance gap against the largest plausible age gap.
func TestScore_RelevanceOutweighsRecency(t *testing.T) {
	now := time.Now().UTC()

	perfectButOld := model.MemoryWithContext{
		Memory: model.Memory{
			ID:        uuid.New(),
			Type:      model.MemoryTypeSemantic,
			CreatedAt: now.Add(-30 * 24 * time.Hour),
		},
		Relevance: 1.0,
	}

	weakButNew := model.MemoryWithContext{
		Memory: model.Memory{
			ID:        uuid.New(),
			Type:      model.MemoryTypeSemantic,
			CreatedAt: now,
		},
		Relevance: 0.6,
	}

	old, fresh := Score(perfectButOld, now), Score(weakButNew, now)
	if old <= fresh {
		t.Errorf("a perfect match 30 days old (%.4f) must outrank a weak match stored today (%.4f): "+
			"recency is a tie-break, not a substitute for answering the query", old, fresh)
	}
}

// TestScore_RecencyStillBreaksTies is the guard on the other side: relevance
// dominating must not make recency inert. Two memories that match the query
// equally well should still be ordered newest-first, which is what makes an
// updated fact supersede the stale version of itself.
func TestScore_RecencyStillBreaksTies(t *testing.T) {
	now := time.Now().UTC()

	newer := model.MemoryWithContext{
		Memory:    model.Memory{ID: uuid.New(), Type: model.MemoryTypeSemantic, CreatedAt: now},
		Relevance: 0.8,
	}
	older := model.MemoryWithContext{
		Memory:    model.Memory{ID: uuid.New(), Type: model.MemoryTypeSemantic, CreatedAt: now.Add(-60 * 24 * time.Hour)},
		Relevance: 0.8,
	}

	if Score(newer, now) <= Score(older, now) {
		t.Errorf("at equal relevance the newer memory (%.4f) must outrank the older one (%.4f)",
			Score(newer, now), Score(older, now))
	}
}

// TestScore_RelevanceOutweighsTypePriority pins that the static type prior
// cannot overturn a relevance difference, for the same reason recency cannot.
//
// TypePriority ranks episodic memories at 0.6 against semantic at 1.0, on the
// theory that stable facts are more reusable than raw events. That is a
// reasonable prior for breaking a tie and a bad reason to lose a match: the
// answer to "when did X happen?" is an event, so the question type most in need
// of episodic memories was the one that systematically discarded them.
//
// Measured on LoCoMo: the memory holding the ground-truth answer led on
// relevance by 0.008 and lost 0.040 to the type prior, a five-fold override.
func TestScore_RelevanceOutweighsTypePriority(t *testing.T) {
	now := time.Now().UTC()

	betterEpisodic := model.MemoryWithContext{
		Memory:    model.Memory{ID: uuid.New(), Type: model.MemoryTypeEpisodic, CreatedAt: now},
		Relevance: 0.92,
	}
	worseSemantic := model.MemoryWithContext{
		Memory:    model.Memory{ID: uuid.New(), Type: model.MemoryTypeSemantic, CreatedAt: now},
		Relevance: 0.91,
	}

	episodic, semantic := Score(betterEpisodic, now), Score(worseSemantic, now)
	if episodic <= semantic {
		t.Errorf("the more relevant episodic memory (%.4f) must outrank the less relevant "+
			"semantic one (%.4f): type is a prior, not a veto over answering the query",
			episodic, semantic)
	}
}

// TestScore_TypeStillBreaksTies is the guard on the other side: at equal
// relevance a stable fact should still be preferred to a one-off event.
func TestScore_TypeStillBreaksTies(t *testing.T) {
	now := time.Now().UTC()

	semantic := model.MemoryWithContext{
		Memory:    model.Memory{ID: uuid.New(), Type: model.MemoryTypeSemantic, CreatedAt: now},
		Relevance: 0.8,
	}
	episodic := model.MemoryWithContext{
		Memory:    model.Memory{ID: uuid.New(), Type: model.MemoryTypeEpisodic, CreatedAt: now},
		Relevance: 0.8,
	}

	if Score(semantic, now) <= Score(episodic, now) {
		t.Errorf("at equal relevance the semantic memory (%.4f) must outrank the episodic one (%.4f)",
			Score(semantic, now), Score(episodic, now))
	}
}

// Score decides the order, and identity only breaks ties.
//
// TestRankResults asserts the output is sorted by score, but builds its
// memories with random UUIDs -- so it passes or fails on the luck of the draw
// if the comparator's branches are swapped. Mutation testing found exactly
// that: inverting the score comparison left the suite green.
//
// Fixed ids here, chosen so identity order contradicts score order. If the
// comparator ever ranks by id first, the higher-scoring memory comes second
// and this fails every time rather than one run in two.
func TestRankResults_ScoreBeatsIdentity(t *testing.T) {
	now := time.Now().UTC()
	first := uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	second := uuid.MustParse("ffffffff-ffff-ffff-ffff-fffffffffffe")

	// The lower-scoring memory sorts first by id, so id order and score order
	// disagree.
	weak := model.MemoryWithContext{
		Memory:    model.Memory{ID: first, Type: model.MemoryTypeSemantic, CreatedAt: now},
		Relevance: 0.1,
	}
	strong := model.MemoryWithContext{
		Memory:    model.Memory{ID: second, Type: model.MemoryTypeSemantic, CreatedAt: now},
		Relevance: 0.9,
	}

	ranked := RankResults([]model.MemoryWithContext{weak, strong}, 0)

	if ranked[0].Memory.ID != second {
		t.Errorf("ranked by identity rather than by score: got %s first, want the "+
			"higher-scoring %s", ranked[0].Memory.ID, second)
	}

	// And with equal scores, identity is what makes the order repeatable.
	tieA := model.MemoryWithContext{
		Memory:    model.Memory{ID: first, Type: model.MemoryTypeSemantic, CreatedAt: now},
		Relevance: 0.5,
	}
	tieB := model.MemoryWithContext{
		Memory:    model.Memory{ID: second, Type: model.MemoryTypeSemantic, CreatedAt: now},
		Relevance: 0.5,
	}
	for i := 0; i < 5; i++ {
		tied := RankResults([]model.MemoryWithContext{tieB, tieA}, 0)
		if tied[0].Memory.ID != first {
			t.Fatalf("equal scores ordered by %s, want the lower id %s: identical queries "+
				"must return identical pages", tied[0].Memory.ID, first)
		}
	}
}
