// Package ranking implements the memory relevance scoring and ranking system.
//
// The scoring formula is a weighted linear combination of four signals:
//
//	score = w_recency * recency + w_frequency * frequency + w_edge * avgEdgeWeight + w_type * typePriority
//
// where:
//   - recency is an exponential decay factor with a 7-day half-life, producing
//     values in [0, 1] that decrease as the memory ages.
//   - frequency is log(1 + accessCount), rewarding memories that are retrieved
//     often without letting a single high-access memory dominate.
//   - avgEdgeWeight is the mean weight of the memory's context edges, boosting
//     memories that are well-connected in the graph.
//   - typePriority is a static priority per memory type: semantic (1.0) >
//     procedural (0.9) > episodic (0.6), reflecting that stable facts are
//     generally more useful than raw events.
//
// Default weights are Recency=0.35, Frequency=0.25, EdgeW=0.25, TypeBoost=0.15,
// giving the strongest influence to temporal freshness while still rewarding
// popularity, graph connectivity, and knowledge type.
package ranking

import (
	"math"
	"sort"
	"time"

	"github.com/context0/context0/pkg/model"
)

// Weights configures the relative importance of each scoring signal. All weights
// should sum to 1.0 for normalized scoring, though this is not enforced.
type Weights struct {
	Recency   float64 // weight for time-based recency
	Frequency float64 // weight for access count
	EdgeW     float64 // weight for average edge weight
	TypeBoost float64 // weight for type priority
}

// DefaultWeights returns production-ready ranking weights that prioritize recency
// (0.35) while balancing frequency (0.25), edge connectivity (0.25), and memory
// type (0.15).
func DefaultWeights() Weights {
	return Weights{
		Recency:   0.35,
		Frequency: 0.25,
		EdgeW:     0.25,
		TypeBoost: 0.15,
	}
}

// TypePriority maps memory types to static priority scores in [0, 1]. Semantic
// facts rank highest because they represent stable, reusable knowledge.
// Procedural memories (workflows, how-tos) are close behind. Episodic memories
// (events) rank lowest since they are often context-specific.
var TypePriority = map[model.MemoryType]float64{
	model.MemoryTypeSemantic:   1.0,
	model.MemoryTypeProcedural: 0.9,
	model.MemoryTypeEpisodic:   0.6,
}

// Score computes a relevance score for a single memory result by combining the
// four weighted signals: recency, frequency, average edge weight, and type priority.
// The caller provides the current time to ensure consistent scoring across a batch.
func Score(mem model.MemoryWithContext, w Weights, now time.Time) float64 {
	recency := recencyFactor(mem.Memory.CreatedAt, now)
	frequency := math.Log1p(float64(mem.Memory.AccessCount))
	avgEdgeWeight := averageEdgeWeight(mem.Context)
	typePrio := TypePriority[mem.Memory.Type]

	return w.Recency*recency +
		w.Frequency*frequency +
		w.EdgeW*avgEdgeWeight +
		w.TypeBoost*typePrio
}

// RankResults scores every memory in the result set, sorts them in descending
// order by score, and truncates to the requested top-K count. The Score field on
// each MemoryWithContext is updated in place before sorting.
func RankResults(results []model.MemoryWithContext, w Weights, topK int) []model.MemoryWithContext {
	now := time.Now().UTC()

	for i := range results {
		results[i].Score = Score(results[i], w, now)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	return results
}

// recencyFactor returns a value in [0, 1] representing how recent a memory is.
// It uses exponential decay: factor = exp(-0.693 * hours / halfLifeHours) where
// the half-life is 7 days (168 hours). A memory created just now scores 1.0;
// a memory from 7 days ago scores ~0.5; a memory from 14 days ago scores ~0.25.
func recencyFactor(createdAt, now time.Time) float64 {
	hoursSince := now.Sub(createdAt).Hours()
	if hoursSince < 0 {
		hoursSince = 0
	}
	halfLifeHours := 7.0 * 24.0 // 7 days
	return math.Exp(-0.693 * hoursSince / halfLifeHours)
}

// averageEdgeWeight computes the arithmetic mean weight of a memory's context
// edges. Returns 0 when there are no edges, which means isolated memories receive
// no edge-weight bonus in the scoring formula.
func averageEdgeWeight(edges []model.ContextEdge) float64 {
	if len(edges) == 0 {
		return 0
	}
	var sum float64
	for _, e := range edges {
		sum += e.Weight
	}
	return sum / float64(len(edges))
}
