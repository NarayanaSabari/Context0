// relevance.go computes the retrieval-stage relevance signal that feeds the
// composite score in scorer.go.
//
// # Superseded by bm25.go
//
// LexicalRelevance and RelevanceTier are no longer on the query path. They
// existed because the keyword retriever matched with Cypher CONTAINS, which is
// boolean -- a memory either contains a term or it does not -- so LexicalRelevance
// recovered a graded score from those booleans and RelevanceTier ordered that
// score against cosine similarity, which is on an unrelated scale.
//
// Keyword retrieval now uses ts_rank_cd, which grades the match directly, so
// neither reconstruction is needed and ranking.FuseRelevance combines the
// signals additively instead.
//
// They are kept, and kept tested, for two reasons. The reasoning in
// RelevanceTier's comment is the record of a real failure -- an unfiltered
// vector hit at 0.87 cosine displacing a verbatim match -- and that constraint
// still binds the fusion weights that replaced it. And the tier is the
// fallback if the full-text path ever has to be reverted, which is a change
// that touches the database schema and is therefore worth keeping cheap.
//
// CombineRelevance and the entity helpers below are live.
package ranking

import "strings"

// agreementBoost is the fraction of the weaker signal added when both the graph
// and the vector retriever independently surface the same memory. Agreement
// between two different retrieval strategies is genuine evidence of relevance,
// but it is a tie-breaking nudge rather than a doubling: the boost is capped so
// a merged score can never exceed 1.0.
const agreementBoost = 0.25

// LexicalRelevance scores how well a memory's text matches the query keywords,
// returning a value in [0, 1].
//
// The score is the fraction of distinct query keywords that appear in the
// memory's content or tags, with a tag match counting slightly higher than a
// content match: a tag is a deliberate human label, so matching one is a
// stronger signal than incidentally containing the word in prose.
//
// An empty keyword set means the query carried no searchable terms (a bare
// "list everything" request). In that case every candidate is equally relevant
// and this returns 1.0, which makes relevance a constant that cannot distort
// the recency/frequency/type signals that still differentiate the results.
func LexicalRelevance(content string, tags []string, keywords []string) float64 {
	if len(keywords) == 0 {
		return 1.0
	}

	lowerContent := strings.ToLower(content)
	lowerTags := make([]string, len(tags))
	for i, t := range tags {
		lowerTags[i] = strings.ToLower(t)
	}

	var total float64
	seen := make(map[string]bool, len(keywords))
	distinct := 0

	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" || seen[kw] {
			continue
		}
		seen[kw] = true
		distinct++

		// A tag hit is the strongest lexical evidence available.
		matchedTag := false
		for _, t := range lowerTags {
			if strings.Contains(t, kw) {
				matchedTag = true
				break
			}
		}
		switch {
		case matchedTag:
			total += 1.0
		case strings.Contains(lowerContent, kw):
			total += 0.75
		}
	}

	if distinct == 0 {
		return 1.0
	}
	return clamp01(total / float64(distinct))
}

// RelevanceTier separates candidates that lexically match the query from those
// that do not, so the two retrievers' scores can be compared at all.
//
// The problem this solves: the graph retriever filters by keyword, so every hit
// it returns genuinely contains the term, and LexicalRelevance grades a content
// hit at 0.75. The vector retriever runs unfiltered and reports raw cosine
// similarity, and a bag-of-words embedding routinely scores near-duplicate
// sentences above 0.85. Those numbers are both "in [0,1]" and are not on the
// same scale: a memory that does not contain the query term at all entered the
// merged set at 0.87 and displaced one that did at 0.75. Observed in a soak run
// as a write not being readable by a keyword unique to its own content.
//
// Cosine similarity is a statement about direction in an embedding space, not
// about evidence, and it cannot be calibrated against lexical matching by
// scaling constants -- any fixed weighting still lets a sufficiently confident
// embedding outvote a verbatim match.
//
// So the two are ordered rather than averaged. A candidate that contains a
// query term is always ranked above one that does not; within each tier the
// available signal orders the results. When the query carries no keywords there
// is no lexical evidence to prefer and similarity alone decides.
func RelevanceTier(lexical, cosine float64, hasKeywords bool) float64 {
	lexical, cosine = clamp01(lexical), clamp01(cosine)
	if !hasKeywords {
		return cosine
	}
	if lexical > 0 {
		// Matched tier: [0.5, 1.0]. Lexical strength leads, similarity refines.
		return clamp01(0.5 + 0.5*CombineRelevance(lexical, cosine))
	}
	// Unmatched tier: [0, 0.5). Similarity is the only signal available, and it
	// can never lift a candidate past one that actually matched.
	return clamp01(cosine) * 0.499
}

// CombineRelevance merges the relevance signals for a memory that both
// retrievers returned. The result is the stronger of the two signals plus a
// bounded share of the weaker one, so agreement lifts a memory above either
// retriever's individual verdict without letting the sum run past 1.0.
func CombineRelevance(a, b float64) float64 {
	a, b = clamp01(a), clamp01(b)
	strong, weak := a, b
	if b > a {
		strong, weak = b, a
	}
	return clamp01(strong + agreementBoost*weak*(1-strong))
}

// entityBoost is how far a memory may rise for naming the same entity the
// query does.
//
// Sized so it breaks ties rather than overturning them, which is the same
// arithmetic the ranking weights use: relevance carries weight 0.75 in the
// composite score, so a relevance bonus of b can only reorder memories whose
// relevance differs by less than b. At 0.05 that is a genuine tie.
//
// Deliberately far below Mem0's ENTITY_BOOST_WEIGHT of 0.5, because their
// fusion divides by an adaptive max_possible and ours adds into an already
// normalised score. A boost of their magnitude here would put any memory
// naming the query's subject above every memory that actually answers it, and
// the subject is named by most of a project's memories -- "Caroline" appears
// in nearly every memory of a corpus about Caroline.
//
// Both directions are pinned: TestEntityBoost_BreaksTiesBetweenEqualMatches
// and TestEntityBoost_CannotOverturnARealRelevanceDifference.
const entityBoost = 0.05

// entityMatchRelevance is the lexical-equivalent strength of naming every
// entity the query names.
//
// This is the value that decides whether entity retrieval does anything at
// all. A candidate only entity retrieval found has no lexical score and no
// cosine score, so entering at zero puts it in RelevanceTier's unmatched tier,
// permanently below every memory containing any query word however common --
// and the whole recall half of the feature is then unreachable in any project
// with more than a handful of keyword matches. Measured before this constant
// existed: a query for "What is Biscuit's greatest fear?" returned three
// memories about pet noise studies and not the one about Biscuit.
//
// So an entity match is treated as what it is: evidence that the memory is
// about the thing the question asks about. 0.5 places a full entity match
// inside the matched tier, above a memory that happens to contain one common
// query word (one of three keywords tiers to ~0.63) and below one that matches
// the query properly (~0.88 and up). That ordering is the claim -- naming the
// subject is necessary but not sufficient, so it beats a weak lexical match
// and loses to a strong one.
//
// Both directions are pinned by TestEntityMatch_BeatsAWeakLexicalMatch and
// TestEntityMatch_LosesToAStrongLexicalMatch.
const entityMatchRelevance = 0.5

// EntityRelevance converts entity overlap into a value on the same scale as
// LexicalRelevance, so the two can be compared before tiering.
//
// They are genuinely comparable, which is why this is not the tier problem
// cosine has: both are "the fraction of what the query asked for that this
// memory carries". A cosine score is a statement about direction in an
// embedding space and cannot be calibrated against either.
func EntityRelevance(overlap float64) float64 {
	return clamp01(entityMatchRelevance * clamp01(overlap))
}

// EntityOverlap reports the weighted share of the query's entities a memory
// names, in [0, 1].
//
// Entity match is evidence of a different kind from either retriever's:
// lexical matching says the words line up and cosine similarity says the
// meanings do, while a shared entity says the two are about the same thing in
// the world. A memory can be about Biscuit without containing the word --
// "the dog hates thunderstorms" -- which is exactly the case both retrievers
// miss and the graph exists to catch.
//
// Comparison is on normalized names, which the caller supplies already
// normalized from the store. An empty query entity set returns 0 rather than
// 1: with nothing to match, no memory has earned anything, and returning 1
// would hand every candidate the full boost and cancel it out.
//
// weights scales each matched entity by how much it discriminates, in [0, 1]
// per entity; a nil map weighs every entity fully, which is the pre-IDF
// behaviour. The weight exists because a shared entity is only evidence when
// sharing it is informative. "Caroline" is named by nearly every memory of a
// corpus about Caroline, so naming her says nothing about which memory
// answers -- measured on the ablation baseline, where this signal pulled
// generic entity matches into adversarial questions and hurt 6-to-1 -- while
// a shared "Biscuit" named by three memories out of thousands is nearly an
// answer by itself. An entity missing from the map weighs 1.0: an unknown
// entity cannot be matched at all, so its weight only ever pads the
// denominator through distinct, exactly as it did before weighting.
func EntityOverlap(memoryEntities, queryEntities []string, weights map[string]float64) float64 {
	if len(queryEntities) == 0 || len(memoryEntities) == 0 {
		return 0
	}

	have := make(map[string]bool, len(memoryEntities))
	for _, e := range memoryEntities {
		have[e] = true
	}

	var matched float64
	distinct := 0
	seen := make(map[string]bool, len(queryEntities))
	for _, q := range queryEntities {
		if q == "" || seen[q] {
			continue
		}
		seen[q] = true
		distinct++
		if have[q] {
			w := 1.0
			if weights != nil {
				if ww, ok := weights[q]; ok {
					w = clamp01(ww)
				}
			}
			matched += w
		}
	}
	if distinct == 0 {
		return 0
	}
	return clamp01(matched / float64(distinct))
}

// ApplyEntityBoost raises a memory's relevance in proportion to how much of
// the query's entity set it names.
//
// Additive rather than tiered, unlike RelevanceTier. The tier there exists
// because lexical and cosine scores are not on a comparable scale, and a
// verbatim match must never lose to a confident embedding. Entity overlap has
// no such problem: it is already a proportion in [0, 1], so it can simply
// contribute, and contributing a small amount is what keeps it a tie-break.
//
// Clamped, so the boost can never push a candidate past the top of the scale
// and flatten the ordering among the memories that were already strongest.
func ApplyEntityBoost(relevance, overlap float64) float64 {
	return clamp01(clamp01(relevance) + entityBoost*clamp01(overlap))
}
