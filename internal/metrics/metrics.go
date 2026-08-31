// Package metrics defines and registers Prometheus metrics for the Kora
// engine. All metric names are prefixed with "kora_" to avoid collisions.
// Metrics are exposed at the /metrics HTTP endpoint via promhttp.Handler.
package metrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// latencyBuckets covers the range this engine actually operates in.
//
// prometheus.DefBuckets starts at 5ms and jumps 0.1 -> 0.25 -> 0.5 -> 1s, which
// is designed for services measured in hundreds of milliseconds. Measured
// against this deployment under load, 79% of queries landed in the single
// [0.1, 0.25] bucket and p50 (114ms), p95 (145ms) and p99 (187ms) all fell
// inside it -- histogram_quantile interpolates linearly within a bucket, so
// those three numbers were indistinguishable from each other and from any
// regression that stayed inside 150ms of headroom.
//
// These buckets span 1ms to 10s. Dense where the work actually is (a scoped
// query is ~3ms unloaded, ~115ms under contention), sparse in the tail where
// only the existence of an outlier matters, not its precise value.
var latencyBuckets = []float64{
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.075,
	0.1, 0.125, 0.15, 0.175, 0.2, 0.25, 0.3, 0.4, 0.5, 0.75,
	1, 2.5, 5, 10,
}

var (
	// MemoriesTotal counts the total number of memories stored, partitioned
	// by memory type (semantic, episodic, procedural).
	MemoriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kora_memories_total",
			Help: "Total number of memories stored, by type.",
		},
		[]string{"type"},
	)

	// EdgesTotal counts the total number of graph edges created, partitioned
	// by relationship type (relates_to, supersedes, caused_by).
	EdgesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kora_edges_total",
			Help: "Total number of edges created, by relationship.",
		},
		[]string{"relationship"},
	)

	// MemoriesConsolidated counts extracted memories that were folded into an
	// existing memory instead of being stored as a new row, by how they
	// related to it.
	//
	// Paired with MemoriesTotal this is the ratio that says whether write-time
	// consolidation is working. It went in alongside the fix for a corpus
	// holding 6,010 memories for 573 distinct facts, and without a metric the
	// only way to notice that ratio drifting back is to count rows by hand.
	//
	// The verdict label separates the cases operationally: a store that is
	// almost all Equivalent is ingesting the same transcript repeatedly, while
	// one that is mostly NewSubsumesOld is being fed progressively more
	// detailed restatements of the same facts.
	MemoriesConsolidated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kora_memories_consolidated_total",
			Help: "Extracted memories folded into an existing memory rather than stored, by subsumption verdict.",
		},
		[]string{"verdict"},
	)

	// SupersededDemotions counts retrieval candidates demoted because a live
	// memory replaces them.
	//
	// The rate is the useful part: demotions per query says how often stale
	// facts are still competing for the top of results, which is a corpus
	// health signal -- a store that is mostly demotions is being fed the same
	// facts in new phrasings faster than consolidation folds them.
	SupersededDemotions = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "kora_superseded_demotions_total",
			Help: "Retrieval candidates demoted because a live memory supersedes them.",
		},
	)

	// ExtractionFallbacks counts conversations the configured LLM extractor
	// could not handle, so the rule-based scanner answered instead.
	//
	// The fallback is deliberate -- losing a conversation because a provider
	// is down is worse than storing a cruder version of it -- but it is a
	// quality change the caller is never told about: Extract returns success
	// either way. Without this, a deployment whose provider has been failing
	// for a week looks identical to one whose provider works, and the only
	// symptom is memories that read like transcript lines.
	//
	// "error" means the call failed. "empty" means it succeeded, returned
	// nothing, and the rule-based pass then found memories in the same text:
	// the disagreement, not the empty result, is the signal. A conversation
	// both extractors find empty is not counted, because it is the normal
	// outcome for a conversation of greetings, and a metric that fires on
	// healthy traffic cannot be alerted on.
	ExtractionFallbacks = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kora_extraction_fallbacks_total",
			Help: "Conversations the rule-based extractor answered because the LLM extractor failed or missed content, by reason.",
		},
		[]string{"reason"},
	)

	// QueryDuration observes the latency of query requests in seconds.
	QueryDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kora_query_duration_seconds",
			Help:    "Duration of query requests.",
			Buckets: latencyBuckets,
		},
	)

	// StoreDuration observes the latency of store requests in seconds.
	StoreDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kora_store_duration_seconds",
			Help:    "Duration of store requests.",
			Buckets: latencyBuckets,
		},
	)

	// RequestsTotal is the R and E of RED: rate and errors, per RPC, by
	// outcome. The duration histograms above cover only Query and Store, so
	// every other endpoint was invisible -- a failing Extract or Profile
	// produced no metric at all.
	//
	// The gRPC code is the label rather than a boolean "error" because the
	// distinction that matters operationally is whose fault it is: a rise in
	// InvalidArgument is a client integration breaking, a rise in Internal is
	// this service breaking, and alerting on their sum catches neither well.
	RequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kora_requests_total",
			Help: "Total RPCs, by method and gRPC status code.",
		},
		[]string{"method", "code"},
	)

	// RequestDuration is the D of RED, for every method rather than the two
	// that happened to be instrumented by hand.
	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kora_request_duration_seconds",
			Help:    "Duration of RPCs, by method.",
			Buckets: latencyBuckets,
		},
		[]string{"method"},
	)

	// QueryResultsCount observes how many results each query returns.
	// Custom buckets are tuned for the typical result set sizes (0-20).
	QueryResultsCount = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kora_query_results_count",
			Help:    "Number of results returned per query.",
			Buckets: []float64{0, 1, 2, 3, 5, 10, 20},
		},
	)

	// ActiveSessions tracks the number of currently open agent sessions.
	ActiveSessions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "kora_active_sessions",
			Help: "Number of currently active sessions.",
		},
	)

	// PoolConnections reports connection pool occupancy by state.
	//
	// Pool exhaustion is this service's most likely saturation point and was
	// entirely unobservable: an earlier deadlock in this codebase, where a
	// transaction held one connection while acquiring a second, presented as
	// uniformly slow requests with no metric that would have named the cause.
	// Every request waits on the pool, so "acquired == max" is the difference
	// between a busy service and a stuck one.
	PoolConnections = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kora_pool_connections",
			Help: "Database connection pool connections, by state.",
		},
		[]string{"state"},
	)

	// PoolAcquireWait is the time spent waiting for a connection, cumulative.
	//
	// A counter of total wait rather than a gauge of current wait: the useful
	// signal is rate(), which shows saturation building before it becomes an
	// outage, and a gauge sampled every 15s would miss it entirely.
	PoolAcquireWait = prometheus.NewCounterFunc(
		prometheus.CounterOpts{
			Name: "kora_pool_acquire_wait_seconds_total",
			Help: "Cumulative time spent waiting to acquire a pooled connection.",
		},
		func() float64 { return poolWaitSeconds() },
	)
)

// poolStatsSource is set by SetPoolStatsSource. Reading pool statistics needs
// the live pool, which is created after the metrics are declared.
var poolStatsSource func() *pgxpool.Stat

// poolWaitSeconds reports cumulative acquire wait, or zero before the pool
// exists. Zero is honest here: no pool means no waiting has happened.
func poolWaitSeconds() float64 {
	if poolStatsSource == nil {
		return 0
	}
	return poolStatsSource().EmptyAcquireWaitTime().Seconds()
}

// SetPoolStatsSource connects the pool to the pool metrics and starts a
// sampler for the gauges.
//
// Sampled on a ticker rather than computed in a CounterFunc per gauge, so one
// scrape does not take four separate snapshots of a moving pool and report a
// state that never existed.
func SetPoolStatsSource(ctx context.Context, pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	poolStatsSource = pool.Stat

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				st := pool.Stat()
				PoolConnections.WithLabelValues("acquired").Set(float64(st.AcquiredConns()))
				PoolConnections.WithLabelValues("idle").Set(float64(st.IdleConns()))
				PoolConnections.WithLabelValues("total").Set(float64(st.TotalConns()))
				PoolConnections.WithLabelValues("max").Set(float64(st.MaxConns()))
				PoolConnections.WithLabelValues("constructing").Set(float64(st.ConstructingConns()))
			}
		}
	}()
}

// Register registers all Kora metrics with the default Prometheus
// registry. It must be called exactly once during server startup; calling
// it more than once will panic (prometheus.MustRegister behavior).
func Register() {
	prometheus.MustRegister(
		MemoriesTotal,
		EdgesTotal,
		MemoriesConsolidated,
		ExtractionFallbacks,
		SupersededDemotions,
		QueryDuration,
		StoreDuration,
		QueryResultsCount,
		ActiveSessions,
		RequestsTotal,
		RequestDuration,
		PoolConnections,
		PoolAcquireWait,
	)
}
