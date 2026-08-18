# Enterprise observability for Context0 (Go gRPC + REST on Kubernetes)

Research report, 2026-08-18. **No code was modified.** Everything below is either a
[VERIFIED] claim backed by a primary source I fetched, or [INFERENCE] where I am reasoning
from those sources to Context0's specific situation.

Current state confirmed by reading the repo:
`internal/metrics/metrics.go` has 6 metrics, `QueryDuration`/`StoreDuration` both use
`prometheus.DefBuckets`, logging is stdlib `log` in `cmd/server`, `cmd/consolidate`,
`internal/graph/age.go`, `internal/service/consolidate.go`. No OTel dependency anywhere.

---

## TL;DR - top 5 changes, in order

1. **Fix the histogram buckets and add per-endpoint RED labels.** `DefBuckets` is
   `{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}` seconds [VERIFIED, read from
   client_golang v1.23.2 source, `prometheus/histogram.go:271`]. With p50≈3ms and p95≈10ms,
   every normal request lands in the first two buckets, so `histogram_quantile` has almost
   no resolution where you actually live. This is the single highest-value, lowest-risk fix.
   Details in §3.
2. **Structured logging via `log/slog` with a JSON handler, plus request-scoped trace/request
   IDs.** Turns "consolidation: starting merge phase..." into something greppable and joinable
   to a trace. §1.
3. **OpenTelemetry tracing** with `otelgrpc.NewServerHandler` + `otelhttp.NewMiddleware` +
   `otelpgx.NewTracer`. You get end-to-end spans through gateway → gRPC → Postgres/AGE for
   roughly 30 lines of wiring. This is the only way to answer "why was *that* request slow".
   §2.
4. **pgxpool saturation gauges via a custom `prometheus.Collector`.** Pool exhaustion is the
   most likely production failure mode for a pgxpool-backed service, and right now it is
   completely invisible. §4.
5. **SLO-based multi-window multi-burn-rate alerts** on one availability SLI and one latency
   SLI, replacing (not supplementing) naive threshold alerts. §6.

---

## 1. Structured logging in Go

### Is `log/slog` the settled answer?

[VERIFIED] Yes. `log/slog` is in the Go standard library and documented at
<https://pkg.go.dev/log/slog>. It ships `JSONHandler` (line-delimited JSON), `TextHandler`,
levels, groups, context-aware methods, and a `LogValuer` interface for redaction. As of the
docs I fetched (go1.26.6) it has 77,158 importers. It is the default choice for new Go
services; zap/zerolog remain fine but no longer offer a decisive advantage, and both now have
`slog.Handler` adapters, so `slog` is the safe API surface to code against even if you later
swap the backend. [INFERENCE on the "safe API surface" point.]

### Idiomatic JSON setup with a dynamically adjustable level

Straight from the package overview [VERIFIED, <https://pkg.go.dev/log/slog>]:

```go
var programLevel = new(slog.LevelVar) // Info by default

h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: programLevel})
slog.SetDefault(slog.New(h))

// later, e.g. from a debug endpoint or SIGHUP:
programLevel.Set(slog.LevelDebug)
```

Two important properties the docs call out explicitly:

- `slog.SetDefault` **also redirects the old `log` package** to the new handler. So
  `log.Printf("consolidation: ...")` in `internal/service/consolidate.go` keeps working and
  starts emitting JSON immediately, without rewriting every call site. That makes migration
  incremental. [VERIFIED]
- `HandlerOptions.Level` set to a `Level` fixes the level for the handler's lifetime; set it
  to a `*LevelVar` to vary it at runtime, safely across goroutines. [VERIFIED]

For the hot path, `logger.LogAttrs(ctx, slog.LevelInfo, "msg", slog.Int("count", 3))` is the
allocation-free form; `slog.Info("msg", "count", 3)` allocates. [VERIFIED - "For the most
efficient log output, use Logger.LogAttrs ... this allows it, too, to avoid allocation".]
At Context0's request rates this matters mostly for per-request logs, not for startup logs.
[INFERENCE]

### Attaching request-scoped context (trace IDs)

The docs address this directly under "Contexts": *"Some handlers may wish to include
information from the context.Context that is available at the call site. One example of such
information is the identifier for the current span when tracing is enabled."* [VERIFIED]

Two mechanisms, and you want both:

**(a) `Logger.With` for static per-request fields.** *"Rather than repeat the attribute with
every log call, you can use Logger.With to construct a new Logger containing the
attributes"* [VERIFIED]. Stash that logger in the request context in an interceptor.

**(b) A wrapping `Handler` that pulls the span context out of `ctx` automatically.** This is
the better design because it cannot be forgotten at a call site:

```go
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
    if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
        r.AddAttrs(
            slog.String("trace_id", sc.TraceID().String()),
            slog.String("span_id", sc.SpanID().String()),
        )
    }
    return h.Handler.Handle(ctx, r)
}
```

[INFERENCE - this is the standard composition of two verified APIs: `slog.Handler`
(<https://pkg.go.dev/log/slog>) and `trace.SpanContextFromContext`. The `slog.Handler`
interface with `Handle(ctx, Record)` and the `Record.AddAttrs` method are verified.]

This only works if you use the `...Context` methods: `slog.InfoContext(ctx, ...)`,
`logger.LogAttrs(ctx, ...)`. The bare `slog.Info` has no context and will silently drop the
trace ID. The docs say *"It is recommended to pass a context to an output method if one is
available."* [VERIFIED] For Context0 that means a lint rule (`sloglint` has a
`context-only` mode) is worth adding alongside the migration. [INFERENCE]

There is also an official bridge, `go.opentelemetry.io/contrib/bridges/otelslog` v0.20.0,
which makes an `slog.Logger` emit into the OTel logs pipeline
[VERIFIED, <https://pkg.go.dev/go.opentelemetry.io/contrib/bridges/otelslog>]. Note OTel Go
**logs are Beta**, not Stable (see §2), so I would keep stdout JSON as the primary sink and
treat the bridge as optional.

### Library vs server default level

- **Library**: a library should not configure logging at all. It should accept an
  `*slog.Logger` (or read `slog.Default()`) and emit at `Debug` for routine internal detail,
  `Info` only for genuinely notable state changes. The `slog` default is `LevelInfo`
  [VERIFIED: *"The default value is LevelInfo"*], and a library that logs at Info on a hot
  path will spam every consumer.
- **Server**: `Info` in production, `Debug` behind an env var or a runtime-adjustable
  `LevelVar` endpoint. *"One common configuration is to log messages at Info or higher
  levels, suppressing debug logging until it is needed. The built-in handlers can be
  configured with the minimum level to output ... The program's `main` function typically
  does this."* [VERIFIED]

[INFERENCE for Context0 specifically] Per-request success logs at Info are wasteful once you
have RED metrics - the metric already tells you the rate. Log at Info for lifecycle events
(startup, config, migrations, consolidation phase transitions) and at Warn/Error for
failures, and let Debug carry per-request detail.

Related, and worth doing: Prometheus's instrumentation guide says *"for every line of logging
code you should also have a counter that is incremented ... It is also generally useful to
export the total number of info/error/warning lines that were logged by the application as a
whole"* [VERIFIED, <https://prometheus.io/docs/practices/instrumentation/>]. A `slog.Handler`
wrapper that increments `context0_log_messages_total{level=...}` gives you that for free, and
`level` is a bounded 4-value label. [INFERENCE on the implementation.]

### Avoiding accidental user-data logging

The primary mechanism is `LogValuer` [VERIFIED, <https://pkg.go.dev/log/slog>]: *"If a type
implements the LogValuer interface, the Value returned from its LogValue method is used for
logging. You can use this to control how values of the type appear in logs. For example, you
can redact secret information like passwords, or gather a struct's fields in a Group."*

For Context0 that means:

```go
type MemoryContent string
func (c MemoryContent) LogValue() slog.Value {
    return slog.StringValue(fmt.Sprintf("<redacted %d bytes>", len(c)))
}
```

Defence in depth, all [INFERENCE] but standard practice:

1. **Type-level redaction via `LogValuer`** on any type holding memory content, embeddings,
   or project identifiers. This is the strongest control because it follows the value
   everywhere.
2. **`HandlerOptions.ReplaceAttr`** as a backstop: a denylist of keys (`content`, `text`,
   `embedding`, `prompt`, `email`) that get replaced with `"[REDACTED]"` regardless of who
   logged them. The docs list ReplaceAttr under "modifying attributes before they are
   logged" [VERIFIED].
3. **Ban `%v` on request protos.** The single biggest accidental-leak vector in a gRPC service
   is `log.Printf("req: %v", req)`. `slog`'s structured API makes this awkward by design,
   which is part of the point. Enforce with a lint rule.
4. **Log identifiers, not payloads.** `memory_id`, `session_id` - never the content. Note
   this also helps §7: high-cardinality values are fine in *logs* (they are not indexed the
   way Prometheus labels are) but are still a compliance surface.
5. `slog.WithGroup` per subsystem to prevent key collisions between, say, the graph layer and
   the service layer both using `id` [VERIFIED - the docs give exactly this rationale].

---

## 2. OpenTelemetry for Go

### Stability matrix [VERIFIED]

From <https://opentelemetry.io/docs/languages/go/>, "Status and Releases":

| Traces | Metrics | Logs |
|--------|---------|------|
| Stable | Stable  | Beta |

That is the primary source you asked for. Traces **and** metrics are Stable in Go; logs are
Beta.

### gRPC tracing

[VERIFIED, <https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc>,
v0.70.0, published 2026-08-04]

**Important correction to the premise of the question:** the interceptor API is *deprecated*.
The package overview now says: *"Use NewClientHandler with grpc.WithStatsHandler to instrument
a gRPC client. Use NewServerHandler with grpc.StatsHandler to instrument a gRPC server."*
The `InterceptorFilter` type and `WithInterceptorFilter` option are both explicitly marked
deprecated in the current docs. The stats-handler approach is the current one, and it is
strictly better: it sees stream events the interceptor cannot.

```go
srv := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
```

That one line is the verified canonical example from the package docs.

Options worth knowing: `WithFilter` (skip health checks and `/metrics`-adjacent RPCs from
tracing), `WithPropagators`, `WithMetricAttributesFn` (dynamic attributes - and the docs
themselves carry a cardinality warning on the example: *"Caution: example only. This must be a
controlled, bounded and very limited set of numbers so that you don't end up with very high
cardinality."*). [VERIFIED] That warning is directly relevant to §7.

### HTTP (grpc-gateway) tracing

[VERIFIED, <https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp>,
v0.70.0]

`otelhttp.NewHandler(handler, "operation", opts...)` or `otelhttp.NewMiddleware("operation")`.
*"NewMiddleware returns a tracing and metrics instrumentation middleware."* Wrap the
grpc-gateway mux with it.

Two features that matter for Context0:

- `WithSpanNameFormatter(func(operation string, r *http.Request) string)` - **use this**. The
  default naming can produce per-path span names; since Context0's REST routes are
  gateway-generated they should already be templated, but confirm. [VERIFIED that the option
  exists; INFERENCE on the recommendation.]
- `otelhttp.Labeler` / `LabelerFromContext` - lets a handler add bounded attributes to the
  emitted metrics from inside the handler. [VERIFIED]
- `WithFilter(func(*http.Request) bool)` - exclude `/livez`, `/readyz`, `/startupz`, and
  `/metrics` from tracing. [VERIFIED that the option exists.] Without this, kubelet probes at
  1-second intervals will dominate your trace volume. [INFERENCE, but this is a well-known
  operational gotcha.]

### pgx / database spans

[VERIFIED, <https://pkg.go.dev/github.com/exaring/otelpgx>, v0.11.1, published 2026-05-21,
110 importers]

This is the mature option for pgx v5. The README gives exactly the wiring Context0 needs:

```go
cfg, err := pgxpool.ParseConfig(connString)
cfg.ConnConfig.Tracer = otelpgx.NewTracer()
pool, err := pgxpool.NewWithConfig(ctx, cfg)
if err := otelpgx.RecordStats(pool); err != nil { ... }
```

Note `otelpgx.RecordStats` **also solves §4** if you go the OTel-metrics route: *"RecordStats
records database statistics for provided pgxpool.Pool at a default 1 second interval unless
otherwise specified by the WithMinimumReadDBStatsInterval StatsOption."* [VERIFIED]

Critical options for Context0, all [VERIFIED from the option list]:

- **`WithDisableSQLStatementInAttributes()`** or `WithTrimSQLInSpanName()`. By default *"the
  whole SQL statement is used as a span name"*. For Context0's AGE Cypher queries and pgvector
  similarity queries, raw SQL in the span name is both a cardinality problem in the tracing
  backend and a potential data-leak (embedded literals). `WithTrimSQLInSpanName` uses only the
  first word; `WithSpanNameFunc` gives full control. **This is the single most important
  configuration decision when adopting otelpgx.** [Option existence VERIFIED; the
  recommendation is INFERENCE.]
- **Do NOT use `WithIncludeQueryParameters()`** - it puts query parameters into span
  attributes. For a memory engine those parameters are user content. [VERIFIED that the option
  does this; the recommendation is INFERENCE.]
- `WithDisableConnectionDetailsInAttributes()` if connection strings could carry credentials.
- It exposes `SQLStateKey` (`pgx.sql_state`) mapping to PostgreSQL error codes, which is a
  genuinely useful bounded attribute for error classification. [VERIFIED]
- pgxpool gained first-class `AcquireTracer` support in v5.6.0, and otelpgx implements
  `TraceAcquireStart`/`TraceAcquireEnd` [VERIFIED from both doc pages], so **you get pool-wait
  time as an actual span**. That is how you distinguish "the query was slow" from "we waited
  40ms for a connection" - which is exactly the question §4 is about.

### Should OTel metrics replace client_golang?

**[INFERENCE, but well-supported] No - keep `prometheus/client_golang` for now, and adopt OTel
for tracing only. Reconsider metrics later.**

The reasoning, with the verified facts it rests on:

- OTel Go metrics *are* Stable [VERIFIED, the matrix above], so stability is not the blocker.
- But the **Prometheus exporter is `v0.67.0`** - a v0.x module, i.e. not yet stable
  [VERIFIED, <https://pkg.go.dev/go.opentelemetry.io/otel/exporters/prometheus>, published
  2026-08-03]. The bridge between the two ecosystems is the immature part, not the API.
- The exporter's own docs show naming translation churn: `WithTranslationStrategy`,
  `WithoutCounterSuffixes` (deprecated), `WithoutUnits` (deprecated), and a `NoTranslation`
  example that produces quoted metric names like `{"my.metric",...}` requiring
  `model.NameEscapingScheme = model.NoEscaping`. [VERIFIED from the three package examples.]
  Metric names are your dashboards and alerts; churn there is expensive.
- The exporter adds `otel_scope_name`, `otel_scope_version`, `otel_scope_schema_url` labels to
  every series [VERIFIED - visible in all three example outputs]. That is 3 extra labels on
  every time series, suppressible via `WithoutScopeInfo()`.
- There is a real gotcha: *"The Prometheus exporter ignores metrics from the Prometheus
  bridge. To export these metrics, simply register them directly with the Prometheus Handler."*
  [VERIFIED] Mixed-mode setups have sharp edges.

**Pragmatic route [INFERENCE]:** keep `client_golang` as the metrics implementation and the
`/metrics` endpoint. Add OTel purely for traces. If you later want otelgrpc's automatic RPC
metrics, `otelprom.New(otelprom.WithRegisterer(reg))` lets the OTel MeterProvider write into
the *same* `client_golang` registry Context0 already serves [VERIFIED - the CustomRegistry
example does exactly this], so it is an additive migration rather than a rewrite. That is the
escape hatch that makes "Prometheus now, OTel later" a low-risk sequence.

### Semantic conventions for RPC metrics [VERIFIED]

<https://opentelemetry.io/docs/specs/semconv/rpc/rpc-metrics/> - status **Release Candidate**.

- `rpc.server.call.duration`, Histogram, unit `s`.
- Recommended `ExplicitBucketBoundaries`:
  `[0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10]`
- Attributes: `rpc.system.name` (Required), `error.type` (Conditionally Required, iff the
  operation failed), `rpc.method` (Conditionally Required), `rpc.response.status_code`
  (Conditionally Required), `server.address`/`server.port` (Opt-In).

Two things worth quoting because they bear directly on §7:

> *"`rpc.method`: The method name MAY have unbounded cardinality in edge or error cases ...
> When the method is not recognized ... the attribute MUST be set to `_OTHER`."*

> *"The `error.type` value SHOULD be predictable and SHOULD have low cardinality."*

Even the spec authors treat cardinality control as a MUST-level concern. Also note that spec's
recommended buckets are *still wrong for Context0* - the first bucket is 5ms, above your p50.
See §3.

---

## 3. RED and USE for Context0

### The methods

- **RED** (Rate, Errors, Duration) - request-scoped, per endpoint. Prometheus's own
  instrumentation guide states the equivalent for online-serving systems: *"The key metrics in
  such a system are the number of performed queries, errors, and latency. The number of
  in-progress requests can also be useful."* [VERIFIED,
  <https://prometheus.io/docs/practices/instrumentation/>]
- **USE** (Utilization, Saturation, Errors) - resource-scoped. *"For every resource, check
  utilization, saturation, and errors."* [VERIFIED,
  <https://www.brendangregg.com/usemethod.html>]

The guide also gives two directly applicable rules:

> *"Be consistent in whether you count queries when they start or when they end. When they end
> is suggested, as it will line up with the error and latency stats, and tends to be easier to
> code."* [VERIFIED]

> *"When reporting failures, you should generally have some other metric representing the total
> number of attempts. This makes the failure ratio easy to calculate."* [VERIFIED]

And for the pgxpool specifically, USE maps cleanly because Gregg notes: *"It can be useful to
consider some software resources as well, or software imposed limits (resource controls)"* -
a connection pool is exactly such a resource. [VERIFIED]

### Concrete metric set for Context0

[INFERENCE for the specific names/labels; the shape follows the verified guidance above.]

**RED, request path.** One histogram carries Rate and Duration; `_count` doubles as the
request counter, which Prometheus docs confirm: *"the `http_request_duration_seconds_count`
series behaves exactly like a counter for the HTTP requests (which you would call
`http_requests_total` if you did not already have the histogram)"* [VERIFIED].

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `context0_rpc_duration_seconds` | Histogram | `service`, `method`, `code` | Replaces `QueryDuration`/`StoreDuration`. `_count` gives Rate; `code!="OK"` gives Errors. |
| `context0_rpc_requests_in_flight` | Gauge | `service` | Saturation of the request path. |
| `context0_rpc_request_bytes` / `_response_bytes` | Histogram | `method` | Optional; catches "slow because huge". |
| `context0_errors_total` | Counter | `component`, `code` | Errors that never reach an RPC boundary (consolidation, background jobs). |
| `context0_log_messages_total` | Counter | `level` | Per Prometheus's logging guidance, quoted in §1. |

Label cardinality: `service` ~3, `method` ~15, `code` ~17 (gRPC canonical codes). Worst case
3×15×17 = 765 series per bucket set - acceptable but not trivial. **Drop `service`** (it is
derivable from `method` if `method` is the fully-qualified name) and you are at ~255.
[INFERENCE]

Do **not** split gRPC and REST into different metric names. The gateway forwards to the same
gRPC handlers, so use one histogram with a `transport="grpc"|"http"` label, or instrument only
at the gRPC layer and accept that gateway-added latency is invisible. The Prometheus docs
recommend the single-metric-with-labels shape explicitly: *"rather than
`http_responses_500_total` and `http_responses_403_total`, create a single metric called
`http_responses_total` with a `code` label"* [VERIFIED].

**USE, resources.**

| Resource | U | S | E |
|---|---|---|---|
| pgxpool | `acquired_conns / max_conns` | `empty_acquire_total` rate, `acquire_duration` | `canceled_acquire_total` |
| Postgres | query duration histogram | connection wait | `pgx.sql_state`-classified errors |
| Goroutines/CPU | `go_goroutines`, `process_cpu_seconds_total` | scheduler latency (Go runtime metrics) | - |
| Memory | `go_memstats_heap_inuse_bytes` vs k8s limit | GC CPU fraction | OOMKills (kube-state-metrics) |
| Consolidation worker | in-progress items | queue depth, last-success timestamp | failures counter |

For the consolidation path, the Prometheus guide's offline-processing advice applies directly:
*"For each stage, track the items coming in, how many are in progress, the last time you
processed something, and how many items were sent out."* [VERIFIED] Context0's
`internal/service/consolidate.go` logs phase transitions but exports nothing - a
`context0_consolidation_last_success_timestamp_seconds` gauge would let you alert on a stalled
consolidator, which is currently undetectable.

Also note Go runtime metrics are **already free**: `client_golang`'s `DefaultRegisterer` comes
pre-registered with the Go and process collectors *"Also note that the DefaultRegisterer comes
registered with a Collector for Go runtime metrics (via NewGoCollector) and a Collector for
process metrics (via NewProcessCollector)"* [VERIFIED,
<https://pkg.go.dev/github.com/prometheus/client_golang/prometheus>]. Context0 uses
`prometheus.MustRegister` (the default registry), so `go_goroutines`, heap stats, GC, fds and
CPU are already on `/metrics`. Half of USE is already there and probably unused.

### Histogram buckets - why `DefBuckets` is wrong here

[VERIFIED] `DefBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}`
(client_golang v1.23.2, `prometheus/histogram.go:271`, read from the local module cache).

With p50 ≈ 3ms and p95 ≈ 10ms:

- Bucket `le=0.005` captures roughly everything below 5ms - so **more than half of all
  requests fall in a single bucket**, and every quantile below p50 is estimated by linear
  interpolation across `[0, 5ms]`.
- p95 ≈ 10ms sits *exactly on* a bucket boundary, which is the least informative place for it
  to be: you can say "95% are under 10ms" and nothing more precise.
- Seven of the eleven buckets (`0.25` through `10`) will be essentially empty in steady state.
  You are paying for 11 time series per label combination and getting usable resolution from
  about 2 of them.

The mechanism matters: Prometheus quantile estimation *"is calculated by assuming a linear
distribution within a bucket"* - if the true p50 is 3ms and the only information is "n
observations landed somewhere in [0, 5ms]", the estimate is an artifact of the bucket layout,
not the data. [Interpolation behaviour is VERIFIED from
<https://prometheus.io/docs/practices/histograms/>; the arithmetic applied to Context0 is
INFERENCE.]

**Better buckets** [INFERENCE, derived from the verified principle that boundaries should
straddle the SLO threshold and the bulk of the distribution]:

```go
// p50 ~3ms, p95 ~10ms. Roughly 2x spacing, dense in 1-20ms, with a tail for
// pool-exhaustion and timeout pathologies. 14 buckets.
Buckets: []float64{
    0.0005, 0.001, 0.002, 0.003, 0.005, 0.0075,
    0.010, 0.015, 0.025, 0.050, 0.100, 0.250, 1.0, 5.0,
}
```

Design rules behind that list:

1. **Put a boundary exactly at your SLO threshold.** If the latency SLO is "99% under 25ms",
   `0.025` must be a boundary, otherwise the SLI itself is an interpolation. This is the
   highest-priority constraint.
2. **Roughly 2× spacing through the body** of the distribution, so each bucket has meaningful
   population.
3. **Keep a long tail** (`1.0`, `5.0`) - the interesting failures (pool exhaustion, a bad
   pgvector scan, AGE query blowup) live out there and you need to distinguish "slow" from
   "catastrophically slow".
4. **Do not exceed ~15 buckets** per label combination. Cost is `buckets × label
   combinations`, and Context0 will have a few hundred label combinations.

**Better still: native histograms.** The Prometheus docs are unusually blunt: *"The most
important lesson to learn from this document is simple: If you can, use native histograms and
prefer them over both classic histograms and summaries."* [VERIFIED,
<https://prometheus.io/docs/practices/histograms/>] Native histograms use dynamic exponential
buckets, so the bucket-tuning problem disappears entirely - which for a service whose latency
profile will shift as the corpus grows is a real advantage. Caveats, all [VERIFIED from the
same page]: *"native histogram support is still rare. Currently, the latter requires exposition
via the protobuf format, limiting the support to protobuf-enabled libraries, like the Java and
the Go library."* Go is on the supported list. And there is a middle path: *"Even if your
instrumented program only exposes classic histogram, you can configure Prometheus to ingest
them as native histograms anyway ... in the form of Native Histograms with Custom Bucket
boundaries (NHCB)."*

[INFERENCE] Recommendation: ship the explicit buckets above now (works with every Prometheus
and every Grafana), and set `NativeHistogramBucketFactor` on the same `HistogramOpts` so that a
protobuf-negotiating scrape gets native histograms for free. `client_golang` supports emitting
both from one instrument.

One more verified caution on aggregation, which is the reason to prefer histograms over
summaries here: with summaries *"you cannot aggregate quantiles (e.g. to calculate the total
90th percentile latency for a service backed by multiple replicated workers)"* [VERIFIED]. On
Kubernetes with multiple Context0 replicas, that rules summaries out.

---

## 4. pgxpool observability

### What `pgxpool.Stat()` exposes [VERIFIED]

From <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool> (v5.10.0), the `Stat` type's full
method set:

| Method | Return | Meaning |
|---|---|---|
| `AcquireCount()` | `int64` | Cumulative acquires |
| `AcquireDuration()` | `time.Duration` | Cumulative total time spent acquiring |
| `AcquiredConns()` | `int32` | Currently checked out |
| `CanceledAcquireCount()` | `int64` | Acquires canceled by context |
| `ConstructingConns()` | `int32` | Being established right now |
| `EmptyAcquireCount()` | `int64` | Acquires that had to wait for a new/idle conn |
| `EmptyAcquireWaitTime()` | `time.Duration` | Cumulative time waiting on an empty pool |
| `IdleConns()` | `int32` | Idle |
| `MaxConns()` | `int32` | Configured max |
| `MaxIdleDestroyCount()` | `int64` | Destroyed for exceeding `MaxConnIdleTime` |
| `MaxLifetimeDestroyCount()` | `int64` | Destroyed for exceeding `MaxConnLifetime` |
| `NewConnsCount()` | `int64` | Total new connections created |
| `TotalConns()` | `int32` | Total in pool |

Config knobs that these interact with, also verified from that page: `MaxConns` (*"The default
is the greater of 4 or runtime.NumCPU()"* - worth checking Context0 sets this explicitly; the
default is far too small for a k8s pod with a low CPU limit), `MinConns`, `MaxConnLifetime`,
`MaxConnLifetimeJitter` (*"helps prevent all connections from being closed at the exact same
time, starving the pool"*), `MaxConnIdleTime`, `PingTimeout`.

### Which ones indicate real problems

[INFERENCE, reasoning from the verified semantics above.]

**Tier 1 - page someone:**

- **`CanceledAcquireCount`** rising. A caller's context expired *while waiting for a
  connection*. This is unambiguous: requests are failing because of the pool, not the
  database. Any sustained non-zero rate is a genuine incident.
  `rate(...[5m]) > 0` is a defensible alert condition.
- **`EmptyAcquireWaitTime`** rate. `rate(empty_acquire_wait_time_seconds_total[5m])` gives you
  *seconds of waiting per second* - i.e. the average number of goroutines blocked on the pool
  at any instant. Above ~1 means, on average, at least one request is always stuck waiting.
  This is the best single saturation signal because it is a rate of a cumulative duration and
  needs no percentile.

**Tier 2 - investigate:**

- **`EmptyAcquireCount`** rate relative to `AcquireCount`. A nonzero *ratio* is not by itself
  bad - a pool sized below peak concurrency will legitimately construct connections. It
  matters when combined with wait time. Watch the ratio, not the raw count.
- **`AcquiredConns / MaxConns`** - the classic utilization number. Gregg's warning applies
  precisely here: *"A burst of high utilization can cause saturation and performance issues,
  even though utilization is low when averaged over a long interval ... The monitoring tool was
  reporting five minute averages, during which CPU utilization hit 100% for seconds at a
  time."* [VERIFIED] A 15s-scrape gauge of `AcquiredConns` will systematically under-report
  bursty exhaustion. **This is why the wait-time rate is the better primary signal** - it is
  cumulative and cannot be missed between scrapes.
- **`AcquireDuration / AcquireCount`** - mean acquire time. Cheap, but a mean; use it for
  trending only.

**Tier 3 - capacity/config signals:**

- `MaxLifetimeDestroyCount` climbing fast means churn; combined with `NewConnsCount`, tells you
  whether you are paying TCP+TLS+auth setup on the request path.
- `ConstructingConns` persistently > 0 means the pool is always growing, i.e. undersized.

**On `AcquireDuration` specifically:** it is a *cumulative total*, not a distribution. You
cannot get a p99 acquire time from it. If you need that - and for diagnosing tail latency you
do - use the `pgxpool.AcquireTracer` interface (added v5.6.0) [VERIFIED] to observe each
acquire into a real histogram, or let `otelpgx` do it, since it implements
`TraceAcquireStart`/`TraceAcquireEnd` [VERIFIED]. **This is the correct answer to "which pool
stat indicates a real problem" - the most important one is not in `Stat()` at all.**

### Collector pattern vs periodic goroutine

**[INFERENCE, strongly supported] Use the custom `prometheus.Collector` pattern.** The
`client_golang` docs describe precisely this use case:

> *"If you already have metrics available, created outside of the Prometheus context, you don't
> need the interface of the various Metric types. You essentially want to mirror the existing
> numbers into Prometheus Metrics during collection. An own implementation of the Collector
> interface is perfect for that. You can create Metric instances 'on the fly' using
> NewConstMetric ... Creation of the Metric instance happens in the Collect method."* [VERIFIED]

That is a textbook description of wrapping `pgxpool.Stat()`.

Why it beats a periodic goroutine [INFERENCE]:

1. **No staleness skew.** Values are read at scrape time, so the timestamp Prometheus assigns
   matches when the value was true. A goroutine ticking at 10s scraped at 15s gives you values
   up to 10s stale, with a phase relationship that drifts.
2. **No goroutine, no shutdown handling, no leak.** One less lifecycle to get wrong.
3. **`Stat()` is cheap** - it reads counters under a lock. Calling it once per scrape is
   strictly less work than every second.
4. **Correct counter semantics.** `NewConstMetric(desc, prometheus.CounterValue, v)` preserves
   the cumulative nature of `AcquireCount` etc. A goroutine setting a Gauge to a cumulative
   value is a common and subtle bug - `rate()` on a gauge silently misbehaves across restarts.

Sketch:

```go
type poolCollector struct {
    pool *pgxpool.Pool
    acquireCount, emptyAcquire, canceledAcquire *prometheus.Desc
    acquiredConns, idleConns, maxConns          *prometheus.Desc
    acquireDuration, emptyAcquireWait           *prometheus.Desc
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) { /* send all descs */ }

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
    s := c.pool.Stat()
    ch <- prometheus.MustNewConstMetric(c.acquireCount,    prometheus.CounterValue, float64(s.AcquireCount()))
    ch <- prometheus.MustNewConstMetric(c.emptyAcquire,    prometheus.CounterValue, float64(s.EmptyAcquireCount()))
    ch <- prometheus.MustNewConstMetric(c.canceledAcquire, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
    ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, s.AcquireDuration().Seconds())
    ch <- prometheus.MustNewConstMetric(c.emptyAcquireWait,prometheus.CounterValue, s.EmptyAcquireWaitTime().Seconds())
    ch <- prometheus.MustNewConstMetric(c.acquiredConns,   prometheus.GaugeValue,   float64(s.AcquiredConns()))
    ch <- prometheus.MustNewConstMetric(c.idleConns,       prometheus.GaugeValue,   float64(s.IdleConns()))
    ch <- prometheus.MustNewConstMetric(c.maxConns,        prometheus.GaugeValue,   float64(s.MaxConns()))
}
```

[The API shapes - `Describe`/`Collect`/`MustNewConstMetric`/`CounterValue` - are VERIFIED from
the client_golang docs; this specific composition is INFERENCE.]

Naming: cumulative ones end in `_total` (`context0_pgxpool_acquires_total`,
`..._empty_acquires_total`, `..._canceled_acquires_total`,
`..._acquire_duration_seconds_total`, `..._empty_acquire_wait_seconds_total`); gauges do not
(`context0_pgxpool_acquired_conns`, `..._idle_conns`, `..._max_conns`). If Context0 ever runs
more than one pool, add a single bounded `pool` label - the Prometheus guide anticipates this:
*"a database connection pool should distinguish the databases it is talking to"* [VERIFIED].

The one thing to be careful about: `Collect` runs on the scrape path, so it must not block. It
does not here. And the docs note that if you return no `Desc` from `Describe` the collector
becomes "unchecked" with consistency only validated at scrape time [VERIFIED] - so do
implement `Describe` properly.

If you adopt otelpgx anyway, `otelpgx.RecordStats(pool)` covers this with OTel-native metric
names [VERIFIED] - but that reintroduces the periodic-read model (*"at a default 1 second
interval"*) and the naming-translation question from §2.

---

## 5. Exemplars and trace-metric correlation

### How it works

[VERIFIED] Prometheus supports exemplars behind a feature flag:
`--enable-feature=exemplar-storage`. From
<https://prometheus.io/docs/prometheus/latest/feature_flags/>:

> *"OpenMetrics introduces the ability for scrape targets to add exemplars to certain metrics.
> Exemplars are references to data outside of the MetricSet. A common use case are IDs of
> program traces. Exemplar storage is implemented as a fixed size circular buffer that stores
> exemplars in memory for all series ... An exemplar with just a `trace_id=<jaeger-trace-id>`
> uses roughly 100 bytes of memory via the in-memory exemplar storage. If the exemplar storage
> is enabled, we will also append the exemplars to WAL for local persistence (for WAL
> duration)."*

That paragraph answers the cost question directly: ~100 bytes per exemplar, fixed-size circular
buffer, configurable via `storage/exemplars` in the config file. The cost is bounded by
construction - it cannot blow up your TSDB the way a bad label can.

On the Go side, `client_golang` provides `ExemplarObserver` [VERIFIED - confirmed present in
`prometheus/observer.go:54-62` of v1.23.2 in the local module cache]:

```go
if obs, ok := hist.(prometheus.ExemplarObserver); ok {
    if sc := trace.SpanContextFromContext(ctx); sc.IsSampled() {
        obs.ObserveWithExemplar(elapsed, prometheus.Labels{"trace_id": sc.TraceID().String()})
        return
    }
}
hist.Observe(elapsed)
```

[The `ExemplarObserver` interface is VERIFIED; this composition is INFERENCE.]

Three requirements that trip people up [VERIFIED except where noted]:

1. **Exposition must be OpenMetrics.** Exemplars are an OpenMetrics feature; the classic
   Prometheus text format cannot carry them. `promhttp.HandlerOpts` has
   `EnableOpenMetrics`-family options (I confirmed `EnableOpenMetricsTextCreatedSamples` exists
   at `promhttp/http.go:423` in v1.23.2). Serve with OpenMetrics negotiation enabled.
2. **Prometheus must run with `--enable-feature=exemplar-storage`.** [VERIFIED] Off by default.
3. **Only attach exemplars for sampled spans.** An exemplar pointing at a trace ID that was
   never exported is a dead link. [INFERENCE]

Only histograms and counters carry exemplars; gauges do not. [INFERENCE]

### Is it worth doing?

**[INFERENCE] Yes for Context0, but only after tracing exists, and it is the 6th priority not
the 1st.**

The argument for: exemplars solve the single hardest problem in latency debugging - the
*sampling mismatch*. Head-based trace sampling at, say, 1% will essentially never capture the
p99.9 request, because slow requests are rare by definition. Exemplars invert this: the
histogram observes 100% of requests, and the exemplar attached to the `le=1.0` bucket is
*guaranteed* to be a genuinely slow one. You click the outlier point in Grafana and land in the
trace for that exact request. Without exemplars, the workflow is "notice p99 is bad, then go
hunting in the trace UI hoping a slow trace was sampled".

Cost is genuinely low: ~100 bytes per exemplar [VERIFIED], no cardinality impact on the TSDB
(exemplars are stored out-of-band in a circular buffer, not as label values - this is the key
distinction from §7), a handful of lines of code.

The argument against, honestly stated: it requires an OpenMetrics-capable scrape path, a
Prometheus feature flag, and a Grafana datasource with `exemplarTraceIdDestinations`
configured. That is three pieces of infrastructure coordination. If Context0's Prometheus is
managed by someone else, the flag may be a negotiation.

**Practical alternative if exemplars are blocked** [INFERENCE]: tail-based sampling in an OTel
Collector, or a simple "always sample if the request exceeded N ms" rule in the application's
sampler. Cruder, but needs no cooperation from the metrics stack.

---

## 6. Dashboards and alerts

### Grafana dashboard

[INFERENCE for the layout; the RED/USE structure follows the verified methods in §3.]

**Row 1 - SLO / service health (the "is it broken" row):**

- Error budget remaining for the 30d window (stat panel, big number).
- Current burn rate, 1h and 6h (stat panels, colored by threshold).
- Availability SLI over 28d (time series with the SLO as a threshold line).

**Row 2 - RED, aggregate then per-method:**

- Request rate: `sum(rate(context0_rpc_duration_seconds_count[5m])) by (method)`
- Error ratio: `sum(rate(...{code!="OK"}[5m])) by (method) / sum(rate(...[5m])) by (method)`
- Latency p50/p95/p99:
  `histogram_quantile(0.99, sum(rate(context0_rpc_duration_seconds_bucket[5m])) by (le, method))`
  Note the aggregation order - `sum by (le)` *before* `histogram_quantile`. This is the
  aggregatability property that makes histograms usable across replicas and summaries not
  [VERIFIED, §3].
- A latency heatmap. With good buckets this is the panel that actually shows you bimodality
  (e.g. cache hit vs miss), which percentile lines hide. **With `DefBuckets` this panel is
  useless**, which is another argument for §3.

**Row 3 - dependencies (pgxpool + Postgres):**

- Pool utilization: `context0_pgxpool_acquired_conns / context0_pgxpool_max_conns`
- Pool saturation: `rate(context0_pgxpool_empty_acquire_wait_seconds_total[5m])` - the
  "average goroutines blocked" number from §4.
- `rate(context0_pgxpool_canceled_acquires_total[5m])` - should be flat zero.
- Query duration p95 split by operation (vector search vs AGE traversal vs write). These have
  wildly different profiles and averaging them together hides regressions.

**Row 4 - runtime (USE for the process):**

- `go_goroutines` (a monotonic climb is the goroutine-leak signature)
- Heap in use vs the k8s memory limit
- GC CPU fraction
- `process_open_fds` vs limit

**Row 5 - Kubernetes:**

- Ready replicas vs desired; restart count; probe failures on `/livez` `/readyz` `/startupz`;
  CPU throttling (`container_cpu_cfs_throttled_seconds_total`). CPU throttling is a classic
  invisible cause of latency regression on k8s and is not visible in any application metric.

**Row 6 - background work:**

- Consolidation: last-success timestamp, items in/out, duration, failures. Per the Prometheus
  offline-processing guidance quoted in §3.

Two cross-cutting rules [INFERENCE]: put a deploy/version annotation on the time axis so
regressions line up with releases, and configure `exemplarTraceIdDestinations` on the latency
panels once §5 is in place.

### Alerts: multi-window multi-burn-rate

[VERIFIED, <https://sre.google/workbook/alerting-on-slos/>, Chapter 5 by Steven Thurgood et al.]

The workbook frames alerting quality along four axes, quoted verbatim:

- **Precision**: *"The proportion of events detected that were significant. Precision is 100% if
  every alert corresponds to a significant event."*
- **Recall**: *"The proportion of significant events detected. Recall is 100% if every
  significant event results in an alert."*
- **Detection time**: *"How long it takes to send notifications in various conditions. Long
  detection times can negatively impact the error budget."*
- **Reset time**: *"How long alerts fire after an issue is resolved. Long reset times can lead
  to confusion or to issues being ignored."*

It then walks six approaches *"in order of increasing fidelity"*, and states plainly: *"The
first three nonviable attempts work toward the latter three viable alerting strategies, with
approach 6 being the most viable and most highly recommended option."*

Why the naive approaches fail, in the workbook's own words:

- **Approach 1 (error rate over 10m ≥ SLO threshold):** *"Precision is low: The alert fires on
  many events that do not threaten the SLO. A 0.1% error rate for 10 minutes would alert, while
  consuming only 0.02% of the monthly error budget."* And devastatingly: *"Taking this example
  to an extreme, you could receive up to 144 alerts per day every day, not act upon any alerts,
  and still meet the SLO."* [VERIFIED]
- **Approach 2 (longer, 36h window):** *"Very poor reset time: In the case of 100% outage, an
  alert will fire shortly after 2 minutes, and continue to fire for the next 36 hours."* Plus
  *"Calculating rates over longer windows can be expensive in terms of memory or I/O
  operations, due to the large number of data points."* [VERIFIED]
- **Approach 3 (`for: 1h` duration clause):** *"Poor recall and poor detection time: Because
  the duration parameter [requires the rate to stay above threshold]..."* - a 100% outage that
  dips momentarily resets the timer. [VERIFIED]

This is the direct, citable answer to "multi-window multi-burn-rate vs naive thresholds": the
naive threshold alert is not merely less elegant, it is *quantifiably* capable of firing 144
times a day on a service that is meeting its SLO.

The workbook's own detection-time formula for approach 1 is
`alerting window size / reporting period` of budget spend, and for approach 2:
`(1 − SLO) / error ratio × alerting window size`. [VERIFIED]

**Concrete rules for Context0** [INFERENCE - the structure is the workbook's canonical
multiwindow/multi-burn-rate pattern; the specific SLO numbers are proposals]:

Assume a 99.9% availability SLO over 30 days.

```yaml
# Fast burn: 14.4x rate consumes 2% of a 30d budget in 1h. Page.
- alert: Context0ErrorBudgetFastBurn
  expr: |
    (context0:slo_errors:ratio_rate1h  > (14.4 * 0.001))
    and
    (context0:slo_errors:ratio_rate5m  > (14.4 * 0.001))
  for: 2m
  labels: {severity: page}

# Slow burn: 6x rate consumes 5% of budget in 6h. Page.
- alert: Context0ErrorBudgetSlowBurn
  expr: |
    (context0:slo_errors:ratio_rate6h  > (6 * 0.001))
    and
    (context0:slo_errors:ratio_rate30m > (6 * 0.001))
  for: 15m
  labels: {severity: page}

# 3x over 24h / 3x over 3d. Ticket, not page.
- alert: Context0ErrorBudgetBurnTicket
  expr: |
    (context0:slo_errors:ratio_rate3d  > (1 * 0.001))
    and
    (context0:slo_errors:ratio_rate6h  > (1 * 0.001))
  labels: {severity: ticket}
```

The short second window is the part that matters and the part people omit: it is what gives
**good reset time**, since the alert stops firing minutes after recovery instead of hours later.
That directly addresses the approach-2 flaw quoted above.

Precompute the ratios as recording rules (`context0:slo_errors:ratio_rate1h` etc.) - the
workbook's examples use exactly this `job:slo_errors_per_request:ratio_rate10m` naming
convention [VERIFIED], and it also mitigates the "expensive over long windows" concern.

**Latency SLI.** Same structure, different numerator. With a bucket boundary at your threshold
(§3), "good events" is a clean bucket read:

```
1 - (
  sum(rate(context0_rpc_duration_seconds_bucket{le="0.025"}[1h]))
  /
  sum(rate(context0_rpc_duration_seconds_count[1h]))
)
```

This is precisely why the SLO threshold must be a bucket boundary - otherwise your SLI is an
interpolated estimate and the alert inherits that error.

**Alerts that should stay as simple thresholds** [INFERENCE] - the symptom-based SLO alerts
above should be the only *pages*, but a few cause-based **tickets** are worth having because
they are leading indicators:

- `rate(context0_pgxpool_canceled_acquires_total[5m]) > 0` for 10m - pool exhaustion.
- `time() - context0_consolidation_last_success_timestamp_seconds > 3600` - stalled background
  worker. The Prometheus guide notes *"Knowing the last time that a system processed something
  is useful for detecting if it has stalled"* [VERIFIED].
- `go_goroutines` growth over 6h - leak.
- Certificate expiry, disk fill, replica count - the usual.

**Low-traffic caveat** [VERIFIED that the workbook flags this]: the chapter explicitly notes
*"alerting can become particularly sensitive to nonsignificant events during low-traffic
periods (discussed in Low-Traffic Services and Error Budget Alerting)"*. If Context0 serves a
handful of requests per minute in some environments, a single error becomes a 100% error rate
over a short window. Mitigations: longer short-windows, a minimum-traffic guard
(`and sum(rate(...[5m])) > 0.1`), or synthetic probe traffic.

---

## 7. Cardinality traps

### `project_id` - confirmed cardinality bomb

**[VERIFIED as a violation of documented Prometheus guidance.]** From
<https://prometheus.io/docs/practices/instrumentation/>:

> *"Each labelset is an additional time series that has RAM, CPU, disk, and network costs."*

> *"As a general guideline, try to keep the cardinality of your metrics below 10, and for
> metrics that exceed that, aim to limit them to a handful across your whole system. The vast
> majority of your metrics should have no labels."*

> *"If you have a metric that has a cardinality over 100 or the potential to grow that large,
> investigate alternate solutions such as reducing the number of dimensions or moving the
> analysis away from monitoring and to a general-purpose processing system."*

`project_id` is user-controlled and unbounded, so it has, in the docs' exact phrasing, *"the
potential to grow that large"*. It is a textbook violation. Confirmed: **do not use it as a
label.**

The docs' own worked example makes the scale concrete [VERIFIED]: node_exporter exposes
`node_filesystem_avail` per mounted filesystem, tens of series per node, ~100,000 series at
10,000 nodes - *"which is fine for Prometheus to handle"*. But: *"If you were to now add quota
per user, you would quickly reach a double digit number of millions with 10,000 users on 10,000
nodes. This is too much for the current implementation of Prometheus."*

Apply that arithmetic to Context0 [INFERENCE]: `context0_rpc_duration_seconds` with 14 buckets
× 15 methods × 17 status codes is ~3,570 series - fine. Multiply by 10,000 projects and you
have **35.7 million series** from one metric. Each series carries memory in the head block, an
index entry, and disk. This will OOM Prometheus. And critically, **the damage persists**:
Prometheus retains those series for the full retention period, so even after you fix the code,
the affected Prometheus stays degraded until the blocks age out. There is no fast rollback.

The docs also warn about the *opportunity* cost, not just the failure: *"Even with smaller
numbers, there's an opportunity cost as you can't have other, potentially more useful metrics
on this machine any more."* [VERIFIED]

Note that OTel's own semantic conventions independently arrive at the same rule - `rpc.method`
MUST become `_OTHER` when unrecognized, and `error.type` *"SHOULD be predictable and SHOULD
have low cardinality"* [VERIFIED, §2]. Two independent standards bodies both treating this as a
MUST/SHOULD is strong signal.

### Full list of labels to avoid in Context0

[INFERENCE, applying the verified rules above to this codebase.]

**Never:** `project_id`, `session_id`, `memory_id`, `user_id`, `agent_id`, `trace_id`,
`request_id`, `query` / query text, embedding vectors or hashes, full URL paths with IDs
embedded, raw error strings (`err.Error()` - unbounded, often contains IDs or SQL fragments),
timestamps, IP addresses, `pod_name` as an *application-set* label (the k8s service discovery
already adds `pod`, and setting it yourself guarantees full series churn on every deploy).

**Note on `pod`/`instance`**: these are unavoidable and legitimate, but they mean every deploy
creates a fresh set of series. That is a real, recurring cost that multiplies against every
other label you add - which is another reason to keep application labels tight.

**Safe (bounded by code, not by input):** `method` (~15, enumerated in the .proto),
`code` (17 gRPC canonical codes), `type` (semantic/episodic/procedural - already used correctly
by `MemoriesTotal`), `relationship` (relates_to/supersedes/caused_by - already correct in
`EdgesTotal`), `level` (4), `transport` (2), `operation` (a small enum you define), `component`.

Context0's existing `MemoriesTotal{type}` and `EdgesTotal{relationship}` are **already the right
pattern** - the label domain is fixed by the schema, not by user input. Preserve that
discipline.

### The correct approach for per-project visibility

You will eventually want "which project is slow / erroring". Four legitimate routes
[INFERENCE, with the mechanisms verified where noted]:

1. **Traces.** Span attributes are *not* indexed like Prometheus labels - each span is an
   independent record. `project_id` as a span attribute is correct and cheap, and it is
   filterable in Tempo/Jaeger. **This is the primary answer**, and it is another argument for
   adopting tracing (§2) rather than trying to squeeze per-tenant detail into metrics.
2. **Structured logs.** `slog.String("project_id", id)` costs nothing in the metrics system.
   Aggregate in Loki/Elasticsearch. Note the §1 privacy point - an ID is fine, content is not.
3. **Exemplars.** Trace IDs attached to slow buckets get you from "the p99 is bad" to "here is
   a slow request, and its trace has the project_id". Bounded ~100 bytes each [VERIFIED, §5].
   This is the elegant bridge between the aggregate and the specific.
4. **A bounded tier label.** If the business need is really "are paying customers affected",
   `tenant_tier="free"|"pro"|"enterprise"` has cardinality 3 and answers it. otelgrpc's
   `WithMetricAttributesFn` example demonstrates exactly this pattern with a
   `tenant.tier` baggage member [VERIFIED - the example uses `tenant.tier` with value
   `premium`], and it is the *only* dynamic-attribute example in those docs that is not
   accompanied by a cardinality warning. That is a deliberate signal from the OTel authors.

**Defence in depth** [INFERENCE]:

- **Code review rule**: any new label must have an enumerable domain documented in a comment.
  Since `internal/metrics/metrics.go` is a single small file, this is easy to enforce.
- **Server-side guard**: `sample_limit` in the scrape config caps damage from a bad deploy, and
  `metric_relabel_configs` can drop an offending label without a code deploy - important
  because, as noted, you cannot un-ingest series.
- **Watch `prometheus_tsdb_head_series`** and alert on sudden growth. This catches the bomb in
  minutes rather than when Prometheus OOMs.
- The Prometheus docs' closing advice is the right default: *"If you are unsure, start with no
  labels and add more labels over time as concrete use cases arise."* [VERIFIED]

---

## Sources

All fetched 2026-08-18.

- <https://pkg.go.dev/log/slog> (go1.26.6)
- <https://opentelemetry.io/docs/languages/go/> - stability matrix
- <https://opentelemetry.io/docs/specs/semconv/rpc/rpc-metrics/> - RC
- <https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc> v0.70.0
- <https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp> v0.70.0
- <https://pkg.go.dev/go.opentelemetry.io/contrib/bridges/otelslog> v0.20.0
- <https://pkg.go.dev/go.opentelemetry.io/otel/exporters/prometheus> v0.67.0
- <https://pkg.go.dev/github.com/exaring/otelpgx> v0.11.1
- <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool> v5.10.0
- <https://pkg.go.dev/github.com/prometheus/client_golang/prometheus> v1.24.1
- <https://prometheus.io/docs/practices/histograms/>
- <https://prometheus.io/docs/practices/instrumentation/>
- <https://prometheus.io/docs/prometheus/latest/feature_flags/> - exemplar-storage
- <https://sre.google/workbook/alerting-on-slos/> - SRE Workbook ch. 5
- <https://www.brendangregg.com/usemethod.html> - USE method

Local verification (read, not modified): `internal/metrics/metrics.go`;
`prometheus/histogram.go:271` and `prometheus/observer.go:54` in
`client_golang@v1.23.2` from the module cache.
