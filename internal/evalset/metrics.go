package evalset

import (
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

// QuestionResult is where one question's evidence landed in a ranked list.
type QuestionResult struct {
	ID           string
	Category     string
	Conversation string
	// EvidenceAnnotated is how many evidence turns the dataset names.
	EvidenceAnnotated int
	// EvidencePresent is how many of those the corpus holds a doc for. This
	// is the denominator of recall: what retrieval could have found.
	EvidencePresent int
	// Ranks holds, for each present evidence turn that was retrieved, the
	// 1-based rank of the first doc covering it. Sorted ascending. Shorter
	// than EvidencePresent when some evidence was not retrieved at all.
	Ranks []int
	// Retrieved is the ranked list of doc ids, for reproduction checks.
	Retrieved []uuid.UUID
	Latency   time.Duration
}

// Scorable reports whether the corpus held any evidence for the question.
func (r QuestionResult) Scorable() bool { return r.EvidencePresent > 0 }

// Score locates a question's evidence in a ranked list.
//
// Relevance is per evidence turn, not per doc: an extracted corpus can hold
// several memories aligned to one turn, and retrieving two of them is one
// piece of evidence found, not two. The first doc covering a turn sets its
// rank.
func Score(q Question, retrieved []uuid.UUID, sources map[uuid.UUID][]string, present map[string]bool) QuestionResult {
	res := QuestionResult{
		ID:                q.ID,
		Category:          q.Category,
		Conversation:      q.Conversation,
		EvidenceAnnotated: len(q.Evidence),
		Retrieved:         retrieved,
	}

	truth := make(map[string]bool, len(q.Evidence))
	for _, dia := range q.Evidence {
		key := TurnKey(q.Conversation, dia)
		if present[key] {
			truth[key] = true
		}
	}
	res.EvidencePresent = len(truth)

	found := make(map[string]int, len(truth))
	for i, id := range retrieved {
		for _, s := range sources[id] {
			if truth[s] {
				if _, seen := found[s]; !seen {
					found[s] = i + 1
				}
			}
		}
	}
	for _, rank := range found {
		res.Ranks = append(res.Ranks, rank)
	}
	sort.Ints(res.Ranks)
	return res
}

// Aggregate is the mean of each metric over the scorable questions.
type Aggregate struct {
	// N is how many questions were scored; Unscorable how many were skipped
	// because the corpus held none of their evidence.
	N          int
	Unscorable int
	// Hit is 1 when any evidence sits in the top k; Recall is the share of
	// present evidence in the top k; Full is 1 when all of it is. Keyed by k.
	Hit    map[int]float64
	Recall map[int]float64
	Full   map[int]float64
	// MRR and NDCG are at k = 10, the depth the published numbers used.
	MRR  float64
	NDCG float64
}

// MetricDepth is the k at which MRR and nDCG are reported.
const MetricDepth = 10

// Aggregate averages results over the scorable questions at each depth in
// ks. An empty input yields zero means, not NaN.
func AggregateResults(results []QuestionResult, ks []int) Aggregate {
	agg := Aggregate{
		Hit:    make(map[int]float64, len(ks)),
		Recall: make(map[int]float64, len(ks)),
		Full:   make(map[int]float64, len(ks)),
	}
	for _, r := range results {
		if !r.Scorable() {
			agg.Unscorable++
			continue
		}
		agg.N++
		for _, k := range ks {
			within := 0
			for _, rank := range r.Ranks {
				if rank <= k {
					within++
				}
			}
			if within > 0 {
				agg.Hit[k]++
			}
			agg.Recall[k] += float64(within) / float64(r.EvidencePresent)
			if within == r.EvidencePresent {
				agg.Full[k]++
			}
		}
		agg.MRR += mrr(r.Ranks, MetricDepth)
		agg.NDCG += ndcg(r.Ranks, r.EvidencePresent, MetricDepth)
	}
	if agg.N == 0 {
		return agg
	}
	n := float64(agg.N)
	for _, k := range ks {
		agg.Hit[k] /= n
		agg.Recall[k] /= n
		agg.Full[k] /= n
	}
	agg.MRR /= n
	agg.NDCG /= n
	return agg
}

func mrr(ranks []int, k int) float64 {
	if len(ranks) == 0 || ranks[0] > k {
		return 0
	}
	return 1 / float64(ranks[0])
}

// ndcg uses binary gains: each evidence turn is worth 1 at the rank it was
// first covered, and the ideal list covers min(relevant, k) turns from rank
// 1 down.
func ndcg(ranks []int, relevant, k int) float64 {
	var dcg float64
	for _, rank := range ranks {
		if rank <= k {
			dcg += 1 / math.Log2(float64(rank)+1)
		}
	}
	var ideal float64
	for i := 1; i <= relevant && i <= k; i++ {
		ideal += 1 / math.Log2(float64(i)+1)
	}
	if ideal == 0 {
		return 0
	}
	return dcg / ideal
}

// ByCategory groups results for per-category aggregation, plus "answerable"
// (everything but adversarial) and "all".
func ByCategory(results []QuestionResult) map[string][]QuestionResult {
	groups := make(map[string][]QuestionResult)
	for _, r := range results {
		groups[r.Category] = append(groups[r.Category], r)
		groups["all"] = append(groups["all"], r)
		if r.Category != "adversarial" {
			groups["answerable"] = append(groups["answerable"], r)
		}
	}
	return groups
}
