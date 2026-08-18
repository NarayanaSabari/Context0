package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestLatencyBucketsResolveObservedRange is the reason these buckets are not
// prometheus.DefBuckets.
//
// Measured against a live deployment under load: p50 114ms, p95 145ms, p99
// 187ms. With DefBuckets all three land in the single [0.1, 0.25] bucket, which
// held 79% of samples. histogram_quantile interpolates linearly inside a
// bucket, so those three percentiles were indistinguishable from each other and
// from any regression that stayed within the bucket's 150ms of headroom.
//
// The requirement is therefore that the operating range is resolved by several
// boundaries, not merely covered by one.
func TestLatencyBucketsResolveObservedRange(t *testing.T) {
	// Values observed from this service: unloaded, loaded, and saturated.
	for _, obs := range []struct {
		name string
		p50  float64
		p99  float64
	}{
		{"unloaded", 0.003, 0.016},
		{"loaded", 0.115, 0.187},
		{"saturated", 0.395, 0.800},
	} {
		var between int
		for _, b := range latencyBuckets {
			if b >= obs.p50 && b <= obs.p99 {
				between++
			}
		}
		if between < 2 {
			t.Errorf("%s: only %d bucket boundaries between p50 (%.3fs) and p99 (%.3fs); "+
				"percentiles in this range cannot be told apart",
				obs.name, between, obs.p50, obs.p99)
		}
	}
}

// TestLatencyBucketsAreOrdered: Prometheus requires strictly increasing
// boundaries and panics on registration otherwise, which would take down
// startup rather than degrade a metric.
func TestLatencyBucketsAreOrdered(t *testing.T) {
	for i := 1; i < len(latencyBuckets); i++ {
		if latencyBuckets[i] <= latencyBuckets[i-1] {
			t.Fatalf("buckets are not strictly increasing at index %d: %v >= %v",
				i, latencyBuckets[i-1], latencyBuckets[i])
		}
	}
	if latencyBuckets[0] > 0.001 {
		t.Errorf("smallest bucket is %v; sub-millisecond work would be indistinguishable from 0",
			latencyBuckets[0])
	}
}

// TestDefBucketsWouldNotResolveThisService documents why the default was wrong
// here, so a future change back to DefBuckets fails with the reason attached.
func TestDefBucketsWouldNotResolveThisService(t *testing.T) {
	// The measured loaded percentiles.
	p50, p95, p99 := 0.115, 0.145, 0.187

	bucketOf := func(v float64, buckets []float64) float64 {
		for _, b := range buckets {
			if v <= b {
				return b
			}
		}
		return -1
	}

	d50 := bucketOf(p50, prometheus.DefBuckets)
	if bucketOf(p95, prometheus.DefBuckets) != d50 || bucketOf(p99, prometheus.DefBuckets) != d50 {
		t.Skip("DefBuckets no longer collapses these percentiles; revisit this reasoning")
	}

	// Confirm ours does better, which is the actual assertion.
	if bucketOf(p50, latencyBuckets) == bucketOf(p99, latencyBuckets) {
		t.Errorf("p50 (%.3fs) and p99 (%.3fs) share bucket %v: same defect as DefBuckets",
			p50, p99, bucketOf(p50, latencyBuckets))
	}
}

// TestPoolWaitSecondsWithoutPool: metrics are declared before the pool exists,
// and a CounterFunc that panics on a nil pool would crash every scrape.
func TestPoolWaitSecondsWithoutPool(t *testing.T) {
	saved := poolStatsSource
	poolStatsSource = nil
	defer func() { poolStatsSource = saved }()

	if got := poolWaitSeconds(); got != 0 {
		t.Errorf("poolWaitSeconds() = %v with no pool, want 0", got)
	}
}

// TestSetPoolStatsSourceIgnoresNilPool: a nil pool must not start a sampler
// goroutine that dereferences it every five seconds.
func TestSetPoolStatsSourceIgnoresNilPool(t *testing.T) {
	saved := poolStatsSource
	defer func() { poolStatsSource = saved }()

	SetPoolStatsSource(t.Context(), nil)
	if poolStatsSource != nil {
		t.Error("a nil pool was installed as the stats source")
	}
}

// TestHistogramsUseTheTunedBuckets closes the gap between "the right buckets
// are defined" and "the histograms use them". Asserting the variable alone
// passes even if every HistogramOpts is switched back to DefBuckets.
//
// Buckets are not readable from a HistogramOpts after construction, so this
// observes a value that the tuned buckets resolve and the defaults do not, then
// reads the exposed bucket boundaries back out of the gathered metric.
func TestHistogramsUseTheTunedBuckets(t *testing.T) {
	for name, h := range map[string]prometheus.Histogram{
		"context0_query_duration_seconds": QueryDuration,
		"context0_store_duration_seconds": StoreDuration,
	} {
		reg := prometheus.NewPedanticRegistry()
		if err := reg.Register(h); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		h.Observe(0.115)

		families, err := reg.Gather()
		if err != nil {
			t.Fatalf("gather %s: %v", name, err)
		}
		if len(families) == 0 {
			t.Fatalf("%s produced no metric families", name)
		}

		var bounds []float64
		for _, b := range families[0].GetMetric()[0].GetHistogram().GetBucket() {
			bounds = append(bounds, b.GetUpperBound())
		}

		// The defect: p50 115ms and p99 187ms sharing one bucket.
		bucketOf := func(v float64) float64 {
			for _, b := range bounds {
				if v <= b {
					return b
				}
			}
			return -1
		}
		if bucketOf(0.115) == bucketOf(0.187) {
			t.Errorf("%s puts the measured p50 and p99 in the same bucket (%v): "+
				"it is not using the tuned buckets", name, bucketOf(0.115))
		}
	}
}
