package ranking

import (
	"math"
	"testing"
	"time"

	"github.com/NarayanaSabari/Kora/pkg/model"
	"github.com/google/uuid"
)

func TestLexicalRelevance(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		tags     []string
		keywords []string
		want     float64
	}{
		{
			name:     "no keywords means every candidate is equally relevant",
			content:  "anything at all",
			keywords: nil,
			want:     1.0,
		},
		{
			name:     "every keyword found in content",
			content:  "The project uses PostgreSQL and Go",
			keywords: []string{"postgresql", "go"},
			want:     0.75,
		},
		{
			name:     "tag match outranks a content match",
			content:  "unrelated prose",
			tags:     []string{"postgresql"},
			keywords: []string{"postgresql"},
			want:     1.0,
		},
		{
			name:     "half the keywords match",
			content:  "the project uses postgresql",
			keywords: []string{"postgresql", "kubernetes"},
			want:     0.375,
		},
		{
			name:     "no keyword matches",
			content:  "completely unrelated",
			keywords: []string{"postgresql"},
			want:     0.0,
		},
		{
			name:     "matching is case insensitive",
			content:  "We deploy with KUBERNETES",
			keywords: []string{"Kubernetes"},
			want:     0.75,
		},
		{
			name:     "duplicate keywords are counted once",
			content:  "postgresql everywhere",
			keywords: []string{"postgresql", "postgresql"},
			want:     0.75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LexicalRelevance(tt.content, tt.tags, tt.keywords)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("LexicalRelevance() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestLexicalRelevanceStaysInUnitInterval(t *testing.T) {
	// Many keywords all matching tags must not accumulate past 1.0.
	got := LexicalRelevance(
		"postgres kubernetes golang",
		[]string{"postgres", "kubernetes", "golang"},
		[]string{"postgres", "kubernetes", "golang"},
	)
	if got < 0 || got > 1 {
		t.Errorf("LexicalRelevance() = %f, want within [0, 1]", got)
	}
}

func TestCombineRelevance(t *testing.T) {
	// Agreement between retrievers must raise the score above either input
	// alone, without ever exceeding 1.0.
	both := CombineRelevance(0.6, 0.5)
	if both <= 0.6 {
		t.Errorf("agreement should boost above the stronger signal, got %f", both)
	}
	if both > 1.0 {
		t.Errorf("combined relevance %f exceeds 1.0", both)
	}

	if got := CombineRelevance(1.0, 1.0); got > 1.0 {
		t.Errorf("combined relevance %f exceeds 1.0", got)
	}

	// Combination is symmetric: retriever order must not matter.
	if a, b := CombineRelevance(0.3, 0.9), CombineRelevance(0.9, 0.3); math.Abs(a-b) > 1e-9 {
		t.Errorf("CombineRelevance not symmetric: %f vs %f", a, b)
	}
}

func TestFrequencyFactorIsBounded(t *testing.T) {
	if got := frequencyFactor(0); got != 0 {
		t.Errorf("never-accessed memory should score 0, got %f", got)
	}

	// Even an absurd access count must stay within the unit interval, which is
	// what stops a popular memory from swamping query relevance.
	for _, count := range []int64{1, 10, 1000, 1_000_000} {
		got := frequencyFactor(count)
		if got < 0 || got >= 1 {
			t.Errorf("frequencyFactor(%d) = %f, want within [0, 1)", count, got)
		}
	}

	// The signal must still be monotonic in access count.
	if frequencyFactor(100) <= frequencyFactor(5) {
		t.Error("frequencyFactor should increase with access count")
	}
}

// TestRelevanceOutranksRecencyAndPopularity is the regression test for the bug
// where the ranking layer recomputed Score from scratch and silently discarded
// everything the retrieval layer had determined. A precise match must beat a
// newer, more frequently accessed, higher-priority memory that does not answer
// the query -- otherwise hybrid search has no effect on what users see.
func TestRelevanceOutranksRecencyAndPopularity(t *testing.T) {
	now := time.Now().UTC()

	relevantButOld := model.MemoryWithContext{
		Memory: model.Memory{
			ID:          uuid.New(),
			Type:        model.MemoryTypeEpisodic,
			CreatedAt:   now.Add(-20 * 24 * time.Hour),
			AccessCount: 0,
		},
		Relevance: 1.0,
	}

	irrelevantButFresh := model.MemoryWithContext{
		Memory: model.Memory{
			ID:          uuid.New(),
			Type:        model.MemoryTypeSemantic,
			CreatedAt:   now,
			AccessCount: 500,
		},
		Relevance: 0.0,
	}

	ranked := RankResults([]model.MemoryWithContext{irrelevantButFresh, relevantButOld}, 0)

	if ranked[0].Memory.ID != relevantButOld.Memory.ID {
		t.Errorf("query-relevant memory should rank first; retrieval score was ignored by ranking")
	}
}

func TestScoreStaysInUnitInterval(t *testing.T) {
	now := time.Now().UTC()

	// Maximum on every signal.
	best := model.MemoryWithContext{
		Memory: model.Memory{
			Type:        model.MemoryTypeSemantic,
			CreatedAt:   now,
			AccessCount: math.MaxInt32,
		},
		Relevance: 1.0,
	}
	if got := Score(best, now); got > 1.0 {
		t.Errorf("Score() = %f, want <= 1.0", got)
	}

	// Minimum on every signal, including an out-of-range relevance that the
	// clamp must absorb.
	worst := model.MemoryWithContext{
		Memory: model.Memory{
			Type:      model.MemoryTypeEpisodic,
			CreatedAt: now.Add(-10 * 365 * 24 * time.Hour),
		},
		Relevance: -5,
	}
	if got := Score(worst, now); got < 0 {
		t.Errorf("Score() = %f, want >= 0.0", got)
	}
}

// TestRankResultsIsDeterministic guards the ordering guarantee: identical
// candidate sets must produce identical output, including when scores tie.
func TestRankResultsIsDeterministic(t *testing.T) {
	now := time.Now().UTC()

	// Three memories built to score identically on every signal.
	build := func() []model.MemoryWithContext {
		var out []model.MemoryWithContext
		for _, id := range []string{
			"11111111-1111-1111-1111-111111111111",
			"22222222-2222-2222-2222-222222222222",
			"33333333-3333-3333-3333-333333333333",
		} {
			out = append(out, model.MemoryWithContext{
				Memory: model.Memory{
					ID:        uuid.MustParse(id),
					Type:      model.MemoryTypeSemantic,
					CreatedAt: now,
				},
				Relevance: 0.5,
			})
		}
		return out
	}

	first := RankResults(build(), 0)
	for i := 0; i < 20; i++ {
		got := RankResults(build(), 0)
		for j := range got {
			if got[j].Memory.ID != first[j].Memory.ID {
				t.Fatalf("ranking is not deterministic at position %d: %s vs %s",
					j, got[j].Memory.ID, first[j].Memory.ID)
			}
		}
	}
}

// TestScoringSignalsRejectHostileInputs covers the guards that keep a bad input
// from producing NaN, which would corrupt ranking rather than merely misrank.
//
// NaN is the specific danger: every comparison against it is false, so a single
// NaN score makes sort.SliceStable's ordering arbitrary and silently scrambles
// the results the user gets back. None of these guards had a test -- found by
// mutation testing, which removed each one and saw the suite stay green.
func TestScoringSignalsRejectHostileInputs(t *testing.T) {
	t.Run("negative access count", func(t *testing.T) {
		// accessCount is read from the database, where an interrupted
		// decrement or a manual edit can leave it negative. Without the guard,
		// log1p of a negative number is NaN.
		for _, n := range []int64{-1, -5, -1000} {
			got := frequencyFactor(n)
			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Errorf("frequencyFactor(%d) = %v; a non-finite score makes sort ordering arbitrary", n, got)
			}
			if got != 0 {
				t.Errorf("frequencyFactor(%d) = %f, want 0", n, got)
			}
		}
	})

	t.Run("future timestamp", func(t *testing.T) {
		// Clock skew between the writer and the ranker puts createdAt in the
		// future. Without the guard the exponent is positive and recency
		// exceeds 1, letting a skewed memory outrank everything.
		now := time.Now()
		got := recencyFactor(now.Add(48*time.Hour), now)
		if got > 1.0 {
			t.Errorf("recencyFactor for a future timestamp = %f, want <= 1.0: "+
				"clock skew must not create an unbeatable score", got)
		}
		if math.IsNaN(got) {
			t.Errorf("recencyFactor for a future timestamp = NaN")
		}
	})

	t.Run("out-of-range relevance", func(t *testing.T) {
		// Cosine distance from pgvector and lexical ratios can drift outside
		// [0,1] through floating-point error. clamp01 is what stops that
		// leaking into the composite score.
		for _, in := range []float64{-0.1, -1e9, 1.0000001, 1e9} {
			got := clamp01(in)
			if got < 0 || got > 1 {
				t.Errorf("clamp01(%v) = %v, outside [0,1]", in, got)
			}
		}
		// The boundaries themselves must pass through unchanged, or every
		// perfect match is quietly discounted.
		if got := clamp01(0); got != 0 {
			t.Errorf("clamp01(0) = %v, want 0", got)
		}
		if got := clamp01(1); got != 1 {
			t.Errorf("clamp01(1) = %v, want 1", got)
		}
	})
}

// TestLexicalRelevanceIgnoresEmptyKeywords: a query that tokenises to an empty
// string must not count toward the denominator, or every result is scored
// against a keyword nothing can match.
func TestLexicalRelevanceIgnoresEmptyKeywords(t *testing.T) {
	// One real keyword plus noise that should be discarded.
	withNoise := LexicalRelevance("the memory mentions prometheus", nil,
		[]string{"prometheus", "", "   ", "prometheus"})
	clean := LexicalRelevance("the memory mentions prometheus", nil,
		[]string{"prometheus"})

	if withNoise != clean {
		t.Errorf("empty and duplicate keywords changed the score: %f with noise, %f clean",
			withNoise, clean)
	}
	if withNoise <= 0 {
		t.Errorf("a matching keyword scored %f, want > 0", withNoise)
	}

	// A query whose tokens are ALL empty carries no searchable terms, which is
	// the same situation as an empty keyword list: every candidate is equally
	// relevant, so relevance must be a constant that cannot distort the
	// recency, frequency and type signals that still differentiate results.
	//
	// Without the distinct==0 guard this divides by zero and yields NaN, which
	// makes sort ordering arbitrary. Found by mutation testing.
	for _, keywords := range [][]string{
		{""},
		{"", "  ", "\t"},
		{"the", ""}, // "the" survives tokenising but may be filtered upstream
	} {
		got := LexicalRelevance("any content at all", nil, keywords)
		if math.IsNaN(got) {
			t.Errorf("LexicalRelevance with keywords %#v = NaN; "+
				"a non-finite relevance makes ranking order arbitrary", keywords)
		}
		if got < 0 || got > 1 {
			t.Errorf("LexicalRelevance with keywords %#v = %f, outside [0,1]", keywords, got)
		}
	}

	// The documented contract for a query with no searchable terms.
	if got := LexicalRelevance("any content", nil, []string{"", "   "}); got != 1.0 {
		t.Errorf("LexicalRelevance with only empty keywords = %f, want 1.0 "+
			"(no lexical evidence means every candidate is equally relevant)", got)
	}
}
