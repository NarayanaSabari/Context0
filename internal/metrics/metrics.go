package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	MemoriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "context0_memories_total",
			Help: "Total number of memories stored, by type.",
		},
		[]string{"type"},
	)

	EdgesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "context0_edges_total",
			Help: "Total number of edges created, by relationship.",
		},
		[]string{"relationship"},
	)

	QueryDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "context0_query_duration_seconds",
			Help:    "Duration of query requests.",
			Buckets: prometheus.DefBuckets,
		},
	)

	StoreDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "context0_store_duration_seconds",
			Help:    "Duration of store requests.",
			Buckets: prometheus.DefBuckets,
		},
	)

	QueryResultsCount = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "context0_query_results_count",
			Help:    "Number of results returned per query.",
			Buckets: []float64{0, 1, 2, 3, 5, 10, 20},
		},
	)

	ActiveSessions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "context0_active_sessions",
			Help: "Number of currently active sessions.",
		},
	)
)

// Register registers all Context0 metrics with the default Prometheus registry.
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
