// Package metrics defines and registers Prometheus metrics for the Context0
// engine. All metric names are prefixed with "context0_" to avoid collisions.
// Metrics are exposed at the /metrics HTTP endpoint via promhttp.Handler.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// MemoriesTotal counts the total number of memories stored, partitioned
	// by memory type (semantic, episodic, procedural).
	MemoriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "context0_memories_total",
			Help: "Total number of memories stored, by type.",
		},
		[]string{"type"},
	)

	// EdgesTotal counts the total number of graph edges created, partitioned
	// by relationship type (relates_to, supersedes, caused_by).
	EdgesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "context0_edges_total",
			Help: "Total number of edges created, by relationship.",
		},
		[]string{"relationship"},
	)

	// QueryDuration observes the latency of query requests in seconds,
	// using the default Prometheus histogram buckets.
	QueryDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "context0_query_duration_seconds",
			Help:    "Duration of query requests.",
			Buckets: prometheus.DefBuckets,
		},
	)

	// StoreDuration observes the latency of store requests in seconds,
	// using the default Prometheus histogram buckets.
	StoreDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "context0_store_duration_seconds",
			Help:    "Duration of store requests.",
			Buckets: prometheus.DefBuckets,
		},
	)

	// QueryResultsCount observes how many results each query returns.
	// Custom buckets are tuned for the typical result set sizes (0-20).
	QueryResultsCount = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "context0_query_results_count",
			Help:    "Number of results returned per query.",
			Buckets: []float64{0, 1, 2, 3, 5, 10, 20},
		},
	)

	// ActiveSessions tracks the number of currently open agent sessions.
	ActiveSessions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "context0_active_sessions",
			Help: "Number of currently active sessions.",
		},
	)
)

// Register registers all Context0 metrics with the default Prometheus
// registry. It must be called exactly once during server startup; calling
// it more than once will panic (prometheus.MustRegister behavior).
func Register() {
	prometheus.MustRegister(
		MemoriesTotal,
		EdgesTotal,
		QueryDuration,
		StoreDuration,
		QueryResultsCount,
		ActiveSessions,
	)
}
