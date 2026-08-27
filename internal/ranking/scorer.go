// Package ranking implements the memory relevance scoring and ranking system.
//
// The scoring formula is a weighted linear combination of four signals:
//
//	score = relevanceWeight * relevance +
//	        recencyWeight   * recency   +
//	        frequencyWeight * frequency +
//	        typeWeight      * typePriority
//
// where:
//   - relevance is the retrieval stage's query-match quality in [0, 1] -- cosine
//     similarity for vector hits, lexical overlap for graph hits, boosted when
//     both retrievers agree. This is the dominant signal: a memory that does not
//     answer the query should not surface merely because it is new or popular.
//   - recency is an exponential decay factor with a 7-day half-life, producing
//     values in [0, 1] that decrease as the memory ages.
//   - frequency is log(1 + accessCount) squashed into [0, 1], rewarding memories
//     that are retrieved often while keeping the signal bounded so a single
//     high-access memory cannot dominate the sum.
//   - typePriority is a static priority per memory type: semantic (1.0) >
//     procedural (0.9) > episodic (0.6), reflecting that stable facts are
//     generally more useful than raw events.
//
// All four signals are normalized to [0, 1] and the weights sum to 1.0, so the
// composite score is itself in [0, 1] and directly comparable across queries.
package ranking

import (
	"math"
	"sort"
	"time"

	"github.com/NarayanaSabari/Kora/pkg/model"
)

// Scoring weights. Query relevance dominates: the point of a search is to
// answer the query, and recency/frequency/type only break ties among memories
// that already match. The four weights sum to 1.0, keeping scores in [0, 1].
//
// relevance was 0.55 against a recency of 0.25, which did not deliver on that
// intent: recency swung the composite by a quarter of its full range while
// relevance spanned only 0.55, so recency could overturn a large relevance gap
// rather than break a tie within one. A perfect match 30 days old scored 0.663
// against 0.680 for a weak match stored today, and lost. Pinned by
// TestScore_RelevanceOutweighsRecency; the opposite guard, that recency still
// orders equally relevant memories, is TestScore_RecencyStillBreaksTies.
//
// The tie-break signals are sized from that same arithmetic. A signal of weight
// w spanning a range r can overturn any relevance gap smaller than w*r/0.75, so
// keeping each swing small is what makes "tie-break" true rather than
// aspirational: recency spans [0,1] at 0.10, and type spans just 0.10 at 0.05.
const (
	relevanceWeight = 0.75
	recencyWeight   = 0.10
	frequencyWeight = 0.10
	typeWeight      = 0.05
)

// frequencySaturation is the access count at which the frequency signal reaches
// roughly half its maximum. Access counts above this contribute progressively
// less, so a memory read thousands of times cannot swamp query relevance.
const frequencySaturation = 10.0

// TypePriority maps memory types to static priority scores. Semantic facts rank
// highest because they represent stable, reusable knowledge. Procedural
// memories (workflows, how-tos) are close behind. Episodic memories (events)
// rank lowest since they are often context-specific.
//
// The spread is deliberately narrow. It was 1.0 / 0.9 / 0.6, which at the old
// typeWeight of 0.10 let the prior move a score by 0.04 -- larger than the
// relevance differences it was competing with, so a query's best answer lost
// for being an event. "When did X happen?" is answered by an episodic memory
// almost by definition, so the questions that most need episodic recall were
// exactly the ones penalising it. Measured on LoCoMo: the ground-truth memory
// led on relevance by 0.012 and lost 0.040 to this prior.
//
// At a 0.10 spread and typeWeight 0.05 the full swing is 0.005, which can only
// reorder memories whose relevance differs by less than 0.007 -- a genuine tie.
// Both directions are pinned: TestScore_TypeStillBreaksTies and
// TestScore_RelevanceOutweighsTypePriority.
var TypePriority = map[model.MemoryType]float64{
	model.MemoryTypeSemantic:   1.0,
	model.MemoryTypeProcedural: 0.95,
	model.MemoryTypeEpisodic:   0.90,
}

// Score computes a relevance score in [0, 1] for a single memory result by
// combining the four weighted signals: query relevance, recency, frequency, and
// type priority. The caller provides the current time to ensure consistent
// scoring across a batch.
func Score(mem model.MemoryWithContext, now time.Time) float64 {
	relevance := clamp01(mem.Relevance)
	recency := recencyFactor(mem.Memory.CreatedAt, now)
	frequency := frequencyFactor(mem.Memory.AccessCount)
	typePrio := TypePriority[mem.Memory.Type]

	return relevanceWeight*relevance +
		recencyWeight*recency +
		frequencyWeight*frequency +
		typeWeight*typePrio
}

// RankResults scores every memory in the result set, sorts them in descending
// order by score, and truncates to the requested top-K count. The Score field on
// each MemoryWithContext is updated in place before sorting.
//
// Ordering is deterministic: the sort is stable and ties are broken by memory
// ID, so the same candidate set always produces the same page of results even
// though upstream retrieval merges through a map.
func RankResults(results []model.MemoryWithContext, topK int) []model.MemoryWithContext {
	now := time.Now().UTC()

	for i := range results {
		results[i].Score = Score(results[i], now)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		// Deterministic tie-break so equal-scoring memories never reorder
		// between identical queries.
		return results[i].Memory.ID.String() < results[j].Memory.ID.String()
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	return results
}

// recencyFactor returns a value in [0, 1] representing how recent a memory is.
// It uses exponential decay: factor = exp(-0.693 * hours / halfLifeHours) where
// the half-life is 90 days. A memory created just now scores 1.0; a memory from
// 90 days ago scores ~0.5; a memory from 180 days ago scores ~0.25.
//
// The half-life was 7 days, which is a feed's timescale, not a memory engine's:
// it drove anything older than a month to ~0.05, collapsing the signal into
// "written this week or not" and erasing the ordering among everything else.
// Kora stores long-lived facts whose value does not halve weekly, and its own
// consolidation job uses a 30-day half-life for decay, so 7 days was also
// inconsistent with the rest of the system.
func recencyFactor(createdAt, now time.Time) float64 {
	hoursSince := now.Sub(createdAt).Hours()
	if hoursSince < 0 {
		hoursSince = 0
	}
	halfLifeHours := 90.0 * 24.0 // 90 days
	return math.Exp(-0.693 * hoursSince / halfLifeHours)
}

// frequencyFactor maps an access count onto [0, 1) using a saturating curve:
// log1p(count) / log1p(count + frequencySaturation). A never-accessed memory
// scores 0, a memory at the saturation point scores ~0.5, and heavily accessed
// memories approach but never reach 1.0. Bounding this signal is what keeps a
// popular-but-irrelevant memory from outranking a precise match.
func frequencyFactor(accessCount int64) float64 {
	if accessCount <= 0 {
		return 0
	}
	count := float64(accessCount)
	return math.Log1p(count) / math.Log1p(count+frequencySaturation)
}

// clamp01 constrains a signal to [0, 1]. Retrieval scores arrive from external
// sources (pgvector cosine distance, ts_rank_cd, lexical overlap ratios) that
// can drift slightly outside the unit interval through floating-point error.
//
// NaN maps to 0 rather than passing through. Every comparison against NaN is
// false, so the two bounds checks below would return it unchanged and it would
// then contaminate the composite score, any edge weight derived from it, and
// the JSON response -- which this repository has already seen once, as "json:
// unsupported value: NaN" from a zero embedding making cosine distance
// undefined. A signal that is not a number is not evidence, so it scores zero.
func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
