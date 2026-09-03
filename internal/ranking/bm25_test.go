package ranking

import (
	"math"
	"testing"
)

// The property the whole adaptation exists for: a memory matching all of its
// query's terms scores about the same however long the query is.
//
// Without it the keyword signal's influence depends on how many words the user
// typed. Measured on Postgres 18, ts_rank_cd gives ~0.1 per matched term, so a
// five-word question produces scores five times larger than a one-word one for
// exactly the same quality of match.
func TestNormalizeBM25_AFullMatchScoresTheSameAtEveryQueryLength(t *testing.T) {
	var scores []float64
	for _, terms := range []int{1, 2, 3, 5, 8, 15, 25} {
		// A full match: bm25RankPerTerm per term.
		raw := bm25RankPerTerm * float64(terms)
		got := NormalizeBM25(raw, terms)
		scores = append(scores, got)

		if got < 0.85 {
			t.Errorf("a query of %d terms matching all of them normalised to %v; "+
				"a complete match must land near the top of the range", terms, got)
		}
	}

	// And they must agree with each other, which is the actual claim.
	for i := 1; i < len(scores); i++ {
		if math.Abs(scores[i]-scores[0]) > 0.02 {
			t.Errorf("full matches normalise to %v and %v at different query "+
				"lengths; the curve is not adapting", scores[0], scores[i])
		}
	}
}

// The same, for the bottom of the range: matching one term of a long query is
// weak evidence and must normalise low, while matching the only term of a
// one-word query is a complete match and must not.
//
// Under a fixed-width curve these two produce the same number, because the raw
// score is identical -- which is the calibration failure.
func TestNormalizeBM25_OneTermMeansLessInALongerQuery(t *testing.T) {
	oneOfOne := NormalizeBM25(bm25RankPerTerm, 1)
	oneOfFive := NormalizeBM25(bm25RankPerTerm, 5)
	oneOfFifteen := NormalizeBM25(bm25RankPerTerm, 15)

	if !(oneOfOne > oneOfFive && oneOfFive > oneOfFifteen) {
		t.Errorf("the same raw score normalised to %v, %v and %v for queries of "+
			"1, 5 and 15 terms; matching one word of a long question is weaker "+
			"evidence than matching the whole of a short one",
			oneOfOne, oneOfFive, oneOfFifteen)
	}
	if oneOfFifteen > 0.3 {
		t.Errorf("matching 1 of 15 terms normalised to %v, which is not weak enough "+
			"to be outranked by a genuine match", oneOfFifteen)
	}
}

// Normalisation rescales the signal; it must never reorder within it. A
// monotonic curve is what guarantees that, and it is why this is a sigmoid
// rather than anything cleverer.
func TestNormalizeBM25_IsMonotonic(t *testing.T) {
	for _, terms := range []int{1, 3, 8, 20} {
		prev := -1.0
		for raw := 0.0; raw <= 3.0; raw += 0.05 {
			got := NormalizeBM25(raw, terms)
			if got < prev {
				t.Fatalf("at %d terms, raw %.2f normalised to %v after a previous "+
					"score of %v; normalisation must not reorder memories",
					terms, raw, got, prev)
			}
			prev = got
		}
	}
}

// A zero rank means the memory matched nothing but stop words. The sigmoid's
// value there is substantially non-zero, so it is special-cased: handing "the"
// a fifth of the keyword signal's range would make it into evidence.
func TestNormalizeBM25_AZeroRankIsZero(t *testing.T) {
	for _, terms := range []int{1, 5, 20} {
		if got := NormalizeBM25(0, terms); got != 0 {
			t.Errorf("a zero rank at %d terms normalised to %v; a memory matching "+
				"only stop words has no keyword evidence at all", terms, got)
		}
		if got := NormalizeBM25(-1, terms); got != 0 {
			t.Errorf("a negative rank at %d terms normalised to %v", terms, got)
		}
	}
	// A query with no terms cannot be matched, so nothing can score against it.
	if got := NormalizeBM25(0.5, 0); got != 0 {
		t.Errorf("a rank against a zero-term query normalised to %v", got)
	}
}

// The upper bound is what stops a document repeating one term from dominating.
// Measured: a document repeating one term 20 times scores 2.0 raw for a
// single-term query, against 0.1 for a normal match.
func TestNormalizeBM25_IsBounded(t *testing.T) {
	for _, raw := range []float64{2.0, 10, 1000, math.Inf(1)} {
		got := NormalizeBM25(raw, 1)
		if got < 0 || got > 1 {
			t.Errorf("raw %v normalised to %v, outside [0, 1]", raw, got)
		}
	}
}

// The semantic gate runs before the signals are combined, and that ordering is
// the substance of it: gating afterwards lets keyword overlap alone rescue a
// candidate the embedding says is unrelated.
func TestPassesSemanticGate_DistinguishesUnscoredFromLowScoring(t *testing.T) {
	// Scored, and clearly unrelated.
	if PassesSemanticGate(0.01, true, false) {
		t.Error("a candidate the embedder scored near zero passed the gate; keyword " +
			"overlap alone would then be enough to return it")
	}
	// Scored, and plausible. The gate is a floor on nonsense, not a relevance
	// judgement: setting it high would discard the paraphrased matches vector
	// search exists to find.
	if !PassesSemanticGate(0.5, true, false) {
		t.Error("a moderately similar candidate was gated out")
	}
	// Never scored. This is the case that must not be treated as failure, or
	// keyword and entity retrieval stop working whenever no embedder is
	// configured.
	if !PassesSemanticGate(0, false, false) {
		t.Error("a candidate vector search never scored was gated out; that would " +
			"disable keyword and entity retrieval whenever the embedder is absent")
	}
}

// The headline behaviour change: fusion is additive, so a memory that merely
// contains a query word no longer automatically outranks a strong semantic
// match sharing no token.
//
// This is the case RelevanceTier gets wrong. It maps any lexical match into
// [0.5, 1.0] and any non-match into [0, 0.5), so one common word beats a
// near-perfect embedding match by construction.
func TestFuseRelevance_AWeakKeywordMatchDoesNotBeatAStrongSemanticMatch(t *testing.T) {
	// One term of a long query, which NormalizeBM25 grades low.
	weakKeyword := FuseRelevance(NormalizeBM25(bm25RankPerTerm, 15), 0, 0)
	// No keyword overlap at all, but the embedder is confident.
	strongSemantic := FuseRelevance(0, 0.92, 0)

	if weakKeyword >= strongSemantic {
		t.Errorf("a memory matching one word of a 15-word query scored %v against "+
			"%v for a 0.92 semantic match sharing no token; that is the tiering "+
			"behaviour additive fusion replaces", weakKeyword, strongSemantic)
	}
}

// The other direction, which is what the tier was protecting: a memory
// genuinely matching the query's terms beats one the embedder places merely
// nearby.
//
// Both signals now arrive min-max normalised per query (see
// retrieval.FusionMinMax), so 1.0 here means "the best in its pool" rather
// than a raw cosine. The claim is therefore about the weights: the keyword
// weight is at least the semantic one, so the pool's best lexical match beats
// the pool's best semantic match when the latter has no lexical evidence,
// and a half-strength lexical match does not. The first direction is the
// soak-run guarantee TestExactKeywordMatchOutranksVectorOnlyResult reproduces
// end to end; the second is what min-max fusion was adopted for.
func TestFuseRelevance_AStrongKeywordMatchStillBeatsSemanticSimilarityAlone(t *testing.T) {
	strongKeyword := FuseRelevance(1.0, 0, 0)
	halfKeyword := FuseRelevance(0.5, 0, 0)
	topSemantic := FuseRelevance(0, 1.0, 0)

	if strongKeyword <= topSemantic {
		t.Errorf("the pool's best lexical match scored %v against %v for its best "+
			"semantic match with no lexical evidence; the keyword weight must stay "+
			"at or above the semantic weight for a rare-token query to return the "+
			"memory that contains the token", strongKeyword, topSemantic)
	}
	if halfKeyword >= topSemantic {
		t.Errorf("a half-strength lexical match scored %v against %v for the pool's "+
			"best semantic match; that is the saturated-keyword behaviour min-max "+
			"fusion replaces", halfKeyword, topSemantic)
	}
}

// Signals compose: agreement between retrievers must score above any of them
// alone, which is what makes the fusion worth having over picking one.
func TestFuseRelevance_AgreementScoresAboveAnySingleSignal(t *testing.T) {
	keyword := NormalizeBM25(bm25RankPerTerm*2, 3)

	only := FuseRelevance(keyword, 0, 0)
	withSemantic := FuseRelevance(keyword, 0.8, 0)
	withBoth := FuseRelevance(keyword, 0.8, 1.0)

	if !(withBoth > withSemantic && withSemantic > only) {
		t.Errorf("adding signals scored %v, %v, %v; each additional retriever "+
			"agreeing is additional evidence", only, withSemantic, withBoth)
	}
}

// An absent signal must contribute zero rather than rescaling the others.
//
// Mem0 divides by a max_possible that adapts to which signals fired, so the
// same pair of scores yields a different answer depending on whether a third
// happened to be present. Fixed weights mean a memory's keyword score means
// the same thing whether or not the embedder was reachable.
func TestFuseRelevance_AnAbsentSignalDoesNotRescaleTheOthers(t *testing.T) {
	keyword := NormalizeBM25(bm25RankPerTerm*3, 3)

	withoutEntity := FuseRelevance(keyword, 0.6, 0)
	withEntity := FuseRelevance(keyword, 0.6, 1.0)

	// The entity signal's full contribution, and nothing else, separates them.
	delta := withEntity - withoutEntity
	if math.Abs(delta-fusionEntityWeight) > 1e-9 {
		t.Errorf("adding a full entity match changed the score by %v, want exactly "+
			"%v; an absent signal must contribute zero rather than rescale the rest",
			delta, fusionEntityWeight)
	}
}

// The weights sum to 1, so the fused score is in [0, 1] and comparable across
// queries. That is what lets it feed ranking.Score, whose own weights assume
// every input is normalised.
func TestFuseRelevance_StaysInRange(t *testing.T) {
	if total := fusionKeywordWeight + fusionSemanticWeight + fusionEntityWeight; math.Abs(total-1) > 1e-9 {
		t.Errorf("the fusion weights sum to %v, not 1; the result would not be "+
			"comparable with the other normalised signals", total)
	}
	if got := FuseRelevance(1, 1, 1); math.Abs(got-1) > 1e-9 {
		t.Errorf("every signal at maximum fused to %v, want 1", got)
	}
	if got := FuseRelevance(0, 0, 0); got != 0 {
		t.Errorf("every signal at zero fused to %v, want 0", got)
	}
	// Out-of-range inputs are clamped rather than propagated: cosine distance
	// from pgvector can drift slightly outside [0, 1] through floating-point
	// error.
	if got := FuseRelevance(2, -1, 5); got < 0 || got > 1 {
		t.Errorf("out-of-range inputs fused to %v", got)
	}
}

// The gate applies to candidates only vector search found, not to every
// candidate that happens to have a low cosine score.
//
// Mem0 retrieves semantically and then reranks, so every candidate is a
// semantic candidate and the threshold prunes the pool. Here the retrievers
// are independent, and a memory full-text search returned was not rescued by
// keyword overlap -- it was retrieved on the strength of its own terms.
//
// Measured with the gate applied indiscriminately: a query for "What is
// Biscuit's greatest fear?" against the bag-of-words embedder scored the one
// memory about Biscuit below the gate, so the memory matching the query's
// terms and naming its subject was discarded while fifteen memories about
// unrelated pet studies survived on cosine similarity alone.
func TestPassesSemanticGate_DoesNotDiscardIndependentlyRetrievedCandidates(t *testing.T) {
	// Same low cosine score, different provenance.
	const low = 0.02

	if PassesSemanticGate(low, true, false) {
		t.Error("a vector-only candidate below the gate survived; the gate exists " +
			"to stop exactly that")
	}
	if !PassesSemanticGate(low, true, true) {
		t.Error("a candidate keyword or entity retrieval found was discarded for " +
			"its cosine score; it was retrieved on evidence the gate says nothing " +
			"about, and the weakest of this engine's embedders scores real matches " +
			"this low")
	}
}

// NaN must not propagate. `raw <= 0` is false for NaN, so without an explicit
// check every operation downstream carries it: into the composite score, into
// any edge weight derived from it, and out through the JSON response.
//
// This repository has already been bitten by exactly that path -- a zero
// embedding made pgvector's cosine distance NaN, which surfaced later as
// "json: unsupported value: NaN". These scores are computed by the database
// from data this process did not choose.
func TestNormalizeBM25_AbsorbsNaN(t *testing.T) {
	for _, terms := range []int{1, 5, 20} {
		if got := NormalizeBM25(math.NaN(), terms); got != 0 {
			t.Errorf("NormalizeBM25(NaN, %d) = %v; a score that is not a number is "+
				"not evidence", terms, got)
		}
	}
	if got := NormalizeBM25(math.Inf(-1), 5); got != 0 {
		t.Errorf("NormalizeBM25(-Inf, 5) = %v", got)
	}
	if got := NormalizeBM25(math.Inf(1), 5); got != 1 {
		t.Errorf("NormalizeBM25(+Inf, 5) = %v, want 1", got)
	}
}

// The same, one level down: every fusion path ends in clamp01, so it is the
// last place a NaN can be stopped.
func TestFuseRelevance_AbsorbsNaN(t *testing.T) {
	for _, args := range [][3]float64{
		{math.NaN(), 0.5, 0.5},
		{0.5, math.NaN(), 0.5},
		{0.5, 0.5, math.NaN()},
	} {
		got := FuseRelevance(args[0], args[1], args[2])
		if math.IsNaN(got) {
			t.Errorf("FuseRelevance%v returned NaN; it would contaminate the "+
				"composite score and the JSON response", args)
		}
		if got < 0 || got > 1 {
			t.Errorf("FuseRelevance%v = %v, outside [0, 1]", args, got)
		}
	}
}
