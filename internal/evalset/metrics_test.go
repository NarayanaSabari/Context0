package evalset

import (
	"math"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// TestScore_FirstCoveringDocSetsRank pins the rule that relevance is per
// evidence turn, not per doc: a second doc covering a turn that was already
// found must not add a second rank, and a turn's rank is set by whichever
// covering doc appears earliest in the ranked list.
func TestScore_FirstCoveringDocSetsRank(t *testing.T) {
	docA := uuid.New()  // covers D1:1, appears at rank 2
	docA2 := uuid.New() // also covers D1:1 (duplicate memory), appears at rank 3
	docB := uuid.New()  // covers D1:2, appears at rank 4
	filler1 := uuid.New()
	filler2 := uuid.New()

	q := Question{
		ID:           "q1",
		Conversation: "conv-1",
		Category:     "multi-hop",
		Evidence:     []string{"D1:1", "D1:2"},
	}
	present := map[string]bool{
		TurnKey("conv-1", "D1:1"): true,
		TurnKey("conv-1", "D1:2"): true,
	}
	sources := map[uuid.UUID][]string{
		docA:  {TurnKey("conv-1", "D1:1")},
		docA2: {TurnKey("conv-1", "D1:1")},
		docB:  {TurnKey("conv-1", "D1:2")},
	}
	retrieved := []uuid.UUID{filler1, docA, docA2, docB, filler2}

	res := Score(q, retrieved, sources, present)

	if res.ID != "q1" || res.Category != "multi-hop" || res.Conversation != "conv-1" {
		t.Errorf("Score copied fields wrong: %+v", res)
	}
	if res.EvidenceAnnotated != 2 {
		t.Errorf("EvidenceAnnotated = %d, want 2", res.EvidenceAnnotated)
	}
	if res.EvidencePresent != 2 {
		t.Errorf("EvidencePresent = %d, want 2", res.EvidencePresent)
	}
	// D1:1's rank is 2 (docA), not 3 (docA2): the first covering doc wins,
	// and the duplicate does not add a second entry.
	if !reflect.DeepEqual(res.Ranks, []int{2, 4}) {
		t.Errorf("Ranks = %v, want [2 4]", res.Ranks)
	}
	if !res.Scorable() {
		t.Error("Scorable() = false, want true: the corpus held evidence for this question")
	}
}

func TestScore_UnscorableWhenNoEvidencePresent(t *testing.T) {
	q := Question{ID: "q2", Conversation: "conv-1", Evidence: []string{"D9:9"}}
	present := map[string]bool{TurnKey("conv-1", "D1:1"): true}
	sources := map[uuid.UUID][]string{}

	res := Score(q, []uuid.UUID{uuid.New()}, sources, present)

	if res.EvidencePresent != 0 {
		t.Errorf("EvidencePresent = %d, want 0", res.EvidencePresent)
	}
	if res.Scorable() {
		t.Error("Scorable() = true, want false: none of this question's evidence is in the corpus")
	}
	if len(res.Ranks) != 0 {
		t.Errorf("Ranks = %v, want none", res.Ranks)
	}
}

// TestAggregateResults hand-computes every metric for one scorable question
// with ranks [2, 4] out of 2 present evidence turns, mixed with one
// unscorable question that must be excluded from every mean.
func TestAggregateResults(t *testing.T) {
	scored := QuestionResult{
		ID:                "q1",
		Category:          "multi-hop",
		EvidenceAnnotated: 2,
		EvidencePresent:   2,
		Ranks:             []int{2, 4},
	}
	unscorable := QuestionResult{
		ID:                "q2",
		Category:          "single-hop",
		EvidenceAnnotated: 1,
		EvidencePresent:   0,
	}

	agg := AggregateResults([]QuestionResult{scored, unscorable}, []int{1, 3, 5})

	if agg.N != 1 {
		t.Errorf("N = %d, want 1", agg.N)
	}
	if agg.Unscorable != 1 {
		t.Errorf("Unscorable = %d, want 1", agg.Unscorable)
	}

	wantHit := map[int]float64{1: 0, 3: 1, 5: 1}
	wantRecall := map[int]float64{1: 0, 3: 0.5, 5: 1}
	wantFull := map[int]float64{1: 0, 3: 0, 5: 1}
	for _, k := range []int{1, 3, 5} {
		if agg.Hit[k] != wantHit[k] {
			t.Errorf("Hit[%d] = %v, want %v", k, agg.Hit[k], wantHit[k])
		}
		if agg.Recall[k] != wantRecall[k] {
			t.Errorf("Recall[%d] = %v, want %v", k, agg.Recall[k], wantRecall[k])
		}
		if agg.Full[k] != wantFull[k] {
			t.Errorf("Full[%d] = %v, want %v", k, agg.Full[k], wantFull[k])
		}
	}

	if agg.MRR != 0.5 {
		t.Errorf("MRR = %v, want 0.5 (1/rank of the first hit, rank 2)", agg.MRR)
	}

	// NDCG@10 by hand: binary gains, dcg = 1/log2(2+1) + 1/log2(4+1) for
	// ranks 2 and 4; ideal = 1/log2(1+1) + 1/log2(2+1) for 2 relevant turns
	// filling ranks 1 and 2.
	dcg := 1/math.Log2(3) + 1/math.Log2(5)
	ideal := 1/math.Log2(2) + 1/math.Log2(3)
	wantNDCG := dcg / ideal
	if math.Abs(agg.NDCG-wantNDCG) > 1e-9 {
		t.Errorf("NDCG = %v, want %v", agg.NDCG, wantNDCG)
	}
}

func TestAggregateResults_EmptyInputYieldsZeros(t *testing.T) {
	agg := AggregateResults(nil, []int{1, 3, 5})

	if agg.N != 0 || agg.Unscorable != 0 {
		t.Errorf("N=%d Unscorable=%d, want 0 and 0", agg.N, agg.Unscorable)
	}
	for _, k := range []int{1, 3, 5} {
		if agg.Hit[k] != 0 || agg.Recall[k] != 0 || agg.Full[k] != 0 {
			t.Errorf("k=%d: Hit=%v Recall=%v Full=%v, want all 0", k, agg.Hit[k], agg.Recall[k], agg.Full[k])
		}
	}
	if agg.MRR != 0 || math.IsNaN(agg.MRR) {
		t.Errorf("MRR = %v, want 0 (not NaN)", agg.MRR)
	}
	if agg.NDCG != 0 || math.IsNaN(agg.NDCG) {
		t.Errorf("NDCG = %v, want 0 (not NaN)", agg.NDCG)
	}
}

func TestByCategory(t *testing.T) {
	results := []QuestionResult{
		{ID: "q1", Category: "single-hop"},
		{ID: "q2", Category: "adversarial"},
	}
	groups := ByCategory(results)

	if len(groups["all"]) != 2 {
		t.Errorf(`groups["all"] has %d entries, want 2`, len(groups["all"]))
	}
	if len(groups["single-hop"]) != 1 || groups["single-hop"][0].ID != "q1" {
		t.Errorf(`groups["single-hop"] = %v, want just q1`, groups["single-hop"])
	}
	if len(groups["adversarial"]) != 1 || groups["adversarial"][0].ID != "q2" {
		t.Errorf(`groups["adversarial"] = %v, want just q2`, groups["adversarial"])
	}
	// "answerable" is everything but adversarial.
	if len(groups["answerable"]) != 1 || groups["answerable"][0].ID != "q1" {
		t.Errorf(`groups["answerable"] = %v, want just q1 (adversarial excluded)`, groups["answerable"])
	}
}
