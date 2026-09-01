package ranking

import (
	"math"
	"testing"
)

// TestMinMax_RescalesOntoUnitInterval pins the per-query rescaling the
// min-max fusion mode depends on: the pool's best scores 1, its worst 0, and
// values outside the range are clamped rather than extrapolated.
func TestMinMax_RescalesOntoUnitInterval(t *testing.T) {
	cases := []struct {
		name      string
		v, lo, hi float64
		want      float64
	}{
		{"best of pool", 0.85, 0.55, 0.85, 1},
		{"worst of pool", 0.55, 0.55, 0.85, 0},
		{"midpoint", 0.70, 0.55, 0.85, 0.5},
		{"below the pool clamps", 0.10, 0.55, 0.85, 0},
		{"above the pool clamps", 0.95, 0.55, 0.85, 1},
	}
	for _, c := range cases {
		if got := MinMax(c.v, c.lo, c.hi); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: MinMax(%v, %v, %v) = %v, want %v", c.name, c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestMinMax_DegenerateRangeIsOne: a pool where every candidate scored the
// same has no worst candidate, so each is as good as the best. Zero would
// silently delete the signal for exactly the queries where the retriever was
// most confident.
func TestMinMax_DegenerateRangeIsOne(t *testing.T) {
	if got := MinMax(0.7, 0.7, 0.7); got != 1 {
		t.Errorf("MinMax over a zero-width range = %v, want 1", got)
	}
	if got := MinMax(0.7, 0.9, 0.7); got != 1 {
		t.Errorf("MinMax over an inverted range = %v, want 1", got)
	}
}

// TestRRF_DecaysWithRank pins the reciprocal-rank shape: rank 1 contributes
// weight/(k+1), deeper ranks less, and an absent rank contributes nothing.
func TestRRF_DecaysWithRank(t *testing.T) {
	if got := RRF(1, 60, 1); math.Abs(got-1.0/61) > 1e-12 {
		t.Errorf("RRF(1, 60, 1) = %v, want 1/61", got)
	}
	if RRF(1, 60, 1) <= RRF(2, 60, 1) || RRF(2, 60, 1) <= RRF(100, 60, 1) {
		t.Error("RRF should decrease with rank")
	}
	if got := RRF(0, 60, 1); got != 0 {
		t.Errorf("RRF of an absent rank = %v, want 0", got)
	}
	if got := RRF(3, 60, 2); math.Abs(got-2*RRF(3, 60, 1)) > 1e-12 {
		t.Errorf("RRF should scale linearly with weight: %v", got)
	}
}

// TestFuseWeighted_NormalisesWeights: the weights are normalised to sum to
// one, so an ablation that passes 1/1/0 gets a score in [0, 1] and a
// present signal is never diluted by an absent one's weight being zero.
func TestFuseWeighted_NormalisesWeights(t *testing.T) {
	if got := FuseWeighted(1, 1, 1, 1, 1, 0); math.Abs(got-1) > 1e-9 {
		t.Errorf("FuseWeighted with weights 1/1/0 and full signals = %v, want 1", got)
	}
	if got := FuseWeighted(1, 0, 0, 2, 2, 0); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("FuseWeighted keyword-only = %v, want 0.5", got)
	}
	if got := FuseWeighted(1, 1, 1, 0, 0, 0); got != 0 {
		t.Errorf("FuseWeighted with no weights = %v, want 0", got)
	}
}

// TestFuseRelevance_MatchesDefaultWeights: FuseRelevance is FuseWeighted at
// the production weights, so the two cannot drift apart.
func TestFuseRelevance_MatchesDefaultWeights(t *testing.T) {
	wk, ws, we := DefaultFusionWeights()
	if math.Abs(wk+ws+we-1) > 1e-9 {
		t.Errorf("default fusion weights sum to %v, want 1", wk+ws+we)
	}
	for _, in := range [][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {0.3, 0.6, 0.5}} {
		want := FuseWeighted(in[0], in[1], in[2], wk, ws, we)
		if got := FuseRelevance(in[0], in[1], in[2]); math.Abs(got-want) > 1e-12 {
			t.Errorf("FuseRelevance%v = %v, FuseWeighted at the defaults = %v", in, got, want)
		}
	}
}
