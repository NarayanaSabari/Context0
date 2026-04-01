package ranking

import (
	"math"
	"sort"
	"time"

	"github.com/context0/context0/pkg/model"
)

// Weights configures the ranking function.
type Weights struct {
	Recency   float64 // weight for time-based recency
	Frequency float64 // weight for access count
	EdgeW     float64 // weight for average edge weight
	TypeBoost float64 // weight for type priority
}

// DefaultWeights returns sensible default ranking weights.
func DefaultWeights() Weights {
	return Weights{
		Recency:   0.35,
		Frequency: 0.25,
		EdgeW:     0.25,
		TypeBoost: 0.15,
	}
}

// TypePriority maps memory types to priority scores.
// Semantic facts are generally more useful than raw episodes.
var TypePriority = map[model.MemoryType]float64{
	model.MemoryTypeSemantic:   1.0,
	model.MemoryTypeProcedural: 0.9,
	model.MemoryTypeEpisodic:   0.6,
}

// Score computes a relevance score for a memory result.
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

// RankResults scores and sorts memories by relevance, returning top-K.
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

// recencyFactor returns a value between 0 and 1 based on how recent the memory is.
// Uses exponential decay with a half-life of 7 days.
func recencyFactor(createdAt, now time.Time) float64 {
	hoursSince := now.Sub(createdAt).Hours()
	if hoursSince < 0 {
		hoursSince = 0
	}
	halfLifeHours := 7.0 * 24.0 // 7 days
	return math.Exp(-0.693 * hoursSince / halfLifeHours)
}

// averageEdgeWeight computes the mean weight of context edges.
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
