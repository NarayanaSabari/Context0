package metrics

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"os"
	"runtime"
	"testing"
	"time"

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

// TestSetPoolStatsSourcePopulatesGauges covers the sampler against a real
// pool.
//
// The existing tests only cover the nil-pool guards, so nothing verified that
// a live pool actually produces gauge values. Pool exhaustion is this
// service's most likely saturation point, and these gauges are the only thing
// that names it -- a sampler that silently never ran would leave the same
// blind spot the metrics were added to remove, while /metrics still listed
// the series.
func TestSetPoolStatsSourcePopulatesGauges(t *testing.T) {
	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set")
	}

	saved := poolStatsSource
	t.Cleanup(func() { poolStatsSource = saved })
	PoolConnections.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Retry the initial connection. This test asserts that the sampler
	// populates gauges from a live pool, not that the database is reachable at
	// this instant: a rolling deployment or a restarted port-forward makes the
	// first ping fail transiently, and failing here would report that as a
	// metrics defect.
	var pingErr error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		if pingErr = pool.Ping(ctx); pingErr == nil {
			break
		}
	}
	if pingErr != nil {
		t.Skipf("database unreachable after retries, so the sampler cannot be "+
			"observed: %v", pingErr)
	}

	SetPoolStatsSource(ctx, pool)

	if poolStatsSource == nil {
		t.Fatal("a live pool was not installed as the stats source")
	}

	// poolWaitSeconds must read through the installed source rather than
	// returning the no-pool zero.
	if got := poolWaitSeconds(); got < 0 {
		t.Errorf("poolWaitSeconds() = %v, want a non-negative duration", got)
	}

	// The sampler ticks every 5s; wait for the gauges to be populated rather
	// than sleeping for a fixed period.
	deadline := time.Now().Add(20 * time.Second)
	var maxConns float64
	for time.Now().Before(deadline) {
		maxConns = testutil.ToFloat64(PoolConnections.WithLabelValues("max"))
		if maxConns > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if maxConns <= 0 {
		t.Fatalf("the pool sampler never populated the max gauge (got %v); "+
			"pool saturation would be invisible", maxConns)
	}

	// Every documented state must be reported, or a partially populated set
	// makes "acquired == max" impossible to evaluate.
	for _, state := range []string{"acquired", "idle", "total", "max", "constructing"} {
		g := PoolConnections.WithLabelValues(state)
		if v := testutil.ToFloat64(g); v < 0 {
			t.Errorf("gauge %q = %v, want a non-negative count", state, v)
		}
	}

	total := testutil.ToFloat64(PoolConnections.WithLabelValues("total"))
	if total > maxConns {
		t.Errorf("total connections %v exceeds max %v", total, maxConns)
	}
}

// TestSetPoolStatsSourceStopsWithContext: the sampler is a goroutine for the
// life of the process, and one that outlived its context would keep touching
// a closed pool.
func TestSetPoolStatsSourceStopsWithContext(t *testing.T) {
	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set")
	}

	saved := poolStatsSource
	t.Cleanup(func() { poolStatsSource = saved })

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	SetPoolStatsSource(ctx, pool)

	cancel()
	pool.Close()

	// The sampler selects on ctx.Done(), so it should exit promptly.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("goroutine count stayed at %d (started from %d) after the context "+
		"was cancelled; the pool sampler may not be stopping",
		runtime.NumGoroutine(), before)
}

// TestPoolWaitSecondsReadsTheInstalledSource: the nil guard exists so the
// metric works before the pool is built, but forcing it always-true makes the
// metric permanently zero -- which reads as "no request has ever waited on the
// pool", the exact opposite of what a saturated pool looks like.
//
// TestPoolWaitSecondsWithoutPool covers the nil case; this covers the case
// where a source is installed and must actually be consulted. It uses a real
// pool because pgxpool.Stat wraps an internal struct that cannot be
// constructed from outside the package.
func TestPoolWaitSecondsReadsTheInstalledSource(t *testing.T) {
	dsn := os.Getenv("CONTEXT0_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CONTEXT0_TEST_DATABASE_URL not set")
	}

	saved := poolStatsSource
	t.Cleanup(func() { poolStatsSource = saved })

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var called bool
	poolStatsSource = func() *pgxpool.Stat {
		called = true
		return pool.Stat()
	}

	got := poolWaitSeconds()
	if !called {
		t.Fatal("poolWaitSeconds did not consult the installed stats source; " +
			"the metric would report zero wait no matter how saturated the pool is")
	}
	if got < 0 {
		t.Errorf("poolWaitSeconds() = %v, want a non-negative duration", got)
	}
}
