// bm25.go squashes raw ts_rank_cd scores onto [0, 1] and fuses them with the
// other retrieval signals.
//
// # Why raw ranks cannot be used directly
//
// ts_rank_cd is unbounded above and its scale depends on the query. Measured
// against a real Postgres 18 instance on one document, varying only the number
// of OR-ed query terms that match:
//
//	terms matched   1     2     3     5     8    12    20
//	ts_rank_cd     0.1   0.2   0.3   0.5   0.8   1.2   1.7
//
// So a rank is roughly bm25RankPerTerm per matched term, and a five-word
// question produces scores five times larger than a one-word one for exactly
// the same quality of match. Feeding those into a weighted sum makes the
// keyword signal's influence depend on how many words the user happened to
// type. That is the same calibration problem documented on relatedStdDevs in
// internal/service: no fixed constant serves two distributions an order of
// magnitude apart.
//
// # The shape, and where it comes from
//
// Mem0 solves this in mem0/utils/scoring.py with a logistic sigmoid whose
// midpoint and steepness vary with query length, and that shape is right. Its
// constants are not: their midpoints run 5.0 to 12.0, which are for their own
// BM25 implementation, and ts_rank_cd never leaves 0.1-2.0 in the measurements
// above. Copying them would map every real score onto the flat bottom of the
// curve, where a perfect match and no match differ in the fourth decimal
// place.
//
// The parameters here are therefore derived from that table rather than
// borrowed: the midpoint sits at a fixed fraction of what a query of that
// length could score, and the steepness scales inversely so the curve's
// transition covers the same fraction of the range whatever the length.
package ranking

import "math"

// bm25RankPerTerm is how much one matched query term contributes to
// ts_rank_cd, measured on Postgres 18 with the 'english' configuration.
//
// The measurement is in this file's doc comment: 1 term matched scores 0.1, 8
// terms score 0.8, 20 terms score 1.7. It is not exactly linear at the top --
// cover density discounts terms that are far apart -- but it is close enough
// over the range real queries occupy, and the sigmoid's job is to be
// forgiving about exactly this.
//
// Term frequency raises it above the line: a document repeating one term 20
// times scores 2.0 for a single-term query. That is the tail the sigmoid's
// upper bound exists to contain.
const bm25RankPerTerm = 0.1

// bm25MidpointCoverage is the fraction of a query's terms a memory must match
// to score 0.5 after normalisation.
//
// 0.4 rather than 0.5 because the terms are OR-ed and questions carry words
// that are not really search terms even after stop-word removal. Requiring
// half the query before a match counts as average would push most genuine hits
// into the bottom of the curve.
const bm25MidpointCoverage = 0.4

// bm25SteepnessScale sets how sharply the curve separates, before it is
// divided by the query's achievable range.
//
// Chosen so a memory matching every term of its query normalises to ~0.8
// rather than to 1.0. Saturating at the top would flatten the difference
// between a good match and a perfect one, which is exactly the ordering
// information ts_rank_cd was adopted to provide.
const bm25SteepnessScale = 2.3

// NormalizeBM25 maps a raw ts_rank_cd score onto [0, 1], adapting to the
// query's length.
//
// The curve is a logistic sigmoid over the raw score, with both parameters
// derived from what a query of this length can achieve:
//
//	midpoint  = bm25RankPerTerm * terms * bm25MidpointCoverage
//	steepness = bm25SteepnessScale / (bm25RankPerTerm * terms * bm25MidpointCoverage)
//
// Dividing the steepness by the same quantity the midpoint is set from is what
// makes the adaptation work: the curve's transition then spans a fixed
// fraction of the query's own range rather than a fixed absolute width, so a
// one-term and a fifteen-term query are graded on the same curve in relative
// terms. Under Mem0's fixed-width transition a long query's scores would all
// crowd into one end.
//
// Two properties matter beyond the scaling. It is bounded, so a document
// repeating one term cannot dominate the fused score. And it is monotonic, so
// it never reorders two memories relative to each other -- normalisation
// rescales the signal, it does not re-rank within it.
//
// A zero or negative raw score returns 0 rather than the ~0.2 the sigmoid
// would give: a memory scoring zero matched nothing but stop words, and
// handing it a fifth of the keyword signal's range would make "the" into
// evidence.
//
// NaN returns 0 too, and explicitly, because `raw <= 0` is false for NaN and
// every operation downstream would propagate it. This repository has already
// been bitten by exactly that: a zero embedding made pgvector's cosine
// distance NaN, which flowed into ranking, into edge weights, and out through
// the API as "json: unsupported value: NaN". A score arriving here is computed
// by the database from data this process did not choose.
func NormalizeBM25(raw float64, queryTerms int) float64 {
	if math.IsNaN(raw) || raw <= 0 || queryTerms <= 0 {
		return 0
	}

	midpoint := bm25RankPerTerm * float64(queryTerms) * bm25MidpointCoverage
	steepness := bm25SteepnessScale / midpoint

	return clamp01(1 / (1 + math.Exp(-steepness*(raw-midpoint))))
}

// semanticGate is the cosine similarity below which a candidate that *only*
// vector search found is discarded before the signals are combined.
//
// Mem0 applies its semantic threshold before combining, and the ordering is
// the substance of it: gating afterwards lets a weak semantic match be rescued
// by keyword overlap alone, which is how a memory sharing one common word with
// the query reaches the results.
//
// # Why it applies to vector-only candidates
//
// Mem0 retrieves semantically and then reranks with BM25 and entity overlap,
// so every candidate is a semantic candidate and the threshold prunes the
// pool. Here the three retrievers are independent and equal, and a memory
// full-text search returned was not rescued by keyword overlap -- it was
// independently retrieved on the strength of its own terms.
//
// Gating those too is not a stricter reading of the same rule, it is a
// different rule, and it costs real answers. Measured: a query for "What is
// Biscuit's greatest fear?" against a bag-of-words embedder scored the one
// memory about Biscuit below the gate, so the memory that matched the query's
// terms *and* named its subject was discarded while fifteen memories about
// unrelated pet studies survived on cosine similarity alone. The embedder is
// the weakest of this engine's four providers and the compose default, so this
// is not an exotic configuration.
//
// Deliberately low regardless. This is a floor on nonsense, not a relevance
// judgement -- the ranking that follows is what orders whatever clears it. Set
// high it would discard the paraphrased matches vector search exists to find;
// at zero it does nothing.
const semanticGate = 0.15

// PassesSemanticGate reports whether a candidate survives the semantic
// threshold.
//
// hasOtherEvidence is the caller's answer to "did any retriever other than
// vector search find this?". When it is true the gate does not apply, because
// the candidate was not rescued by a weak signal -- it was retrieved on
// evidence the gate says nothing about.
//
// hasCosine separately distinguishes "scored low" from "never scored". A
// candidate vector search never saw is unmeasured, not weak, and treating
// absence as failure would disable keyword and entity retrieval entirely
// whenever no embedder is configured.
func PassesSemanticGate(cosine float64, hasCosine, hasOtherEvidence bool) bool {
	if !hasCosine || hasOtherEvidence {
		return true
	}
	return cosine >= semanticGate
}

// Fusion weights, mirroring the structure of Mem0's
// `(semantic + bm25 + entity_boost) / max_possible` while keeping this
// engine's own normalisation.
//
// Theirs divides by a max_possible that adapts to which signals are present,
// which bounds the result but means the same pair of scores yields a different
// answer depending on whether a third signal happened to fire. Here the
// weights are fixed and sum to 1, so an absent signal contributes zero rather
// than rescaling the others: a memory's keyword score means the same thing
// whether or not the embedder was reachable.
//
// The split favours lexical evidence over cosine similarity, which is the same
// judgement RelevanceTier encodes in a stronger form. A memory containing the
// query's rare terms is better evidence than one an embedding places nearby,
// because cosine similarity is a statement about direction in a space rather
// than about whether the memory says what was asked.
const (
	fusionKeywordWeight  = 0.5
	fusionSemanticWeight = 0.35
	fusionEntityWeight   = 0.15
)

// FuseRelevance combines the three retrieval signals additively.
//
// Additive rather than tiered, which is the change this replaces.
// RelevanceTier ranks *any* lexical match above *any* non-match, mapping
// matches into [0.5, 1.0] and non-matches into [0, 0.5). That is a stronger
// claim than the evidence supports: under it a memory containing one common
// query word outranks a near-perfect semantic match sharing no token.
//
// The tier was the right call while the lexical signal was boolean CONTAINS,
// because a boolean cannot say how good a match is and the tier stood in for
// that missing information. With ts_rank_cd the signal is graded -- a rare
// term outweighs a common one by an order of magnitude, and a stop word scores
// exactly zero -- so the tier compensates for nothing and its cost is the case
// above.
//
// Every input is expected in [0, 1]: keyword through NormalizeBM25, semantic
// as cosine similarity, entity as EntityOverlap.
func FuseRelevance(keyword, semantic, entity float64) float64 {
	return clamp01(
		fusionKeywordWeight*clamp01(keyword) +
			fusionSemanticWeight*clamp01(semantic) +
			fusionEntityWeight*clamp01(entity),
	)
}
