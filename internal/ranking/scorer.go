// Package ranking implements the memory relevance scoring and ranking system.
//
// The scoring formula is a weighted linear combination of three signals:
//
//	score = recencyWeight * recency + frequencyWeight * frequency + typeWeight * typePriority
//
// where:
//   - recency is an exponential decay factor with a 7-day half-life, producing
//     values in [0, 1] that decrease as the memory ages.
//   - frequency is log(1 + accessCount), rewarding memories that are retrieved
//     often without letting a single high-access memory dominate.
//   - typePriority is a static priority per memory type: semantic (1.0) >
//     procedural (0.9) > episodic (0.6), reflecting that stable facts are
//     generally more useful than raw events.
package ranking

import (
	"math"
	"sort"
	"time"

	"github.com/context0/context0/pkg/model"
)

// Scoring weights. Recency dominates, followed by frequency and memory type.
const (
	recencyWeight   = 0.35
	frequencyWeight = 0.25
	typeWeight      = 0.15
)

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
// three weighted signals: recency, frequency, and type priority. The caller
// provides the current time to ensure consistent scoring across a batch.
func Score(mem model.MemoryWithContext, now time.Time) float64 {
	recency := recencyFactor(mem.Memory.CreatedAt, now)
	frequency := math.Log1p(float64(mem.Memory.AccessCount))
	typePrio := TypePriority[mem.Memory.Type]

	return recencyWeight*recency +
		frequencyWeight*frequency +
		typeWeight*typePrio
}

// RankResults scores every memory in the result set, sorts them in descending
// order by score, and truncates to the requested top-K count. The Score field on
// each MemoryWithContext is updated in place before sorting.
func RankResults(results []model.MemoryWithContext, topK int) []model.MemoryWithContext {
	now := time.Now().UTC()

	for i := range results {
		results[i].Score = Score(results[i], now)
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
