// relevance.go computes the retrieval-stage relevance signal that feeds the
// composite score in scorer.go.
//
// Two retrievers produce candidates, and each needs its match quality expressed
// on the same [0, 1] scale before the two sets can be merged:
//
//   - The vector retriever already emits cosine similarity in [0, 1], so it is
//     used directly.
//   - The graph retriever matches with Cypher CONTAINS, which is boolean: a
//     memory either contains a keyword or it does not. LexicalRelevance recovers
//     a graded score from those boolean matches so that a memory matching every
//     query term outranks one matching a single term.
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
