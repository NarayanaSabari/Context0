# Kubernetes and Go runtime settings

This page explains every setting the Helm chart at `charts/kora` sets
specifically because of how Kubernetes 1.37 (the current release, with
1.34-1.37 supported) and Go 1.26 (this repo's `go.mod`) behave, and where each
number came from.
Nothing here is a general Helm chart tour; read `charts/kora/values.yaml` and
its templates for that.
Every number below is either a fact about Go or Kubernetes from the sources
listed at the bottom, or a measurement from `docs/WORKLOG.md`'s Track B
profiling run, or a value derived from another setting already in this chart.
None are invented.

## GOMAXPROCS: nothing to set

Go 1.25 and later set `GOMAXPROCS` from the container's cgroup CPU limit
automatically on Linux: it reads the limit, re-checks it up to once a second,
ignores CPU requests entirely, and rounds a fractional limit up with a floor
of 2 (500m and 1500m both become 2; 2500m becomes 3).
This is gated on the `go` directive in `go.mod` being 1.25 or later; this
repo's is 1.26.1, so it applies without any code change.
Setting the `GOMAXPROCS` environment variable disables this and pins the
value, which this chart does not do.

`uber-go/automaxprocs` is deliberately not vendored.
It solves the same problem but worse on a Go 1.25+ toolchain: it rounds a
fractional limit down instead of up, floors at 1 instead of 2, computes the
value once at process start instead of re-checking the cgroup limit, and
running it disables the stdlib behaviour above.
It predates the stdlib doing this itself and has nothing left to add here.

## api.resources.limits.cpu: 2000m

CPU is sized from the read-path profile in `docs/WORKLOG.md` (Track B), not a
round number.
A query is about 22ms of wall-clock time, of which about 21ms is waiting on a
PostgreSQL round trip and about 5ms is this process's own CPU; the API is
CPU-idle 77% of the time even under a loaded single-threaded query stream.
The write path (`extract`) is bound on the embedding/LLM network call, not
CPU, for the same reason.
PostgreSQL is the actual bottleneck, which is why `postgres.resources` in this
chart runs with no CPU limit at all (see the comment there).

The API keeps a CPU limit anyway, unlike Postgres, because of the GOMAXPROCS
behaviour above: with no limit, Go would set GOMAXPROCS from the node's full
core count, and on a large node that means GC worker goroutines and the
runtime scheduler spreading across every core the node has, for a process
that is idle most of the time.
A limit keeps that bounded.

2000m specifically is not "4x the old 500m limit" chosen by feel.
Because GOMAXPROCS rounds a fractional CPU limit up with a floor of 2, every
limit from 1 to 2000m resolves to the same GOMAXPROCS value of 2; only above
2000m does it become 3.
2000m is therefore the largest limit that still produces exactly the same
GOMAXPROCS the chart already had at 500m.
Raising the limit to that point buys throttling headroom for concurrent
bursts (CFS throttling is what actually enforces a CPU limit) without
changing the runtime's own parallelism at all.

## api.resources.requests.cpu: 200m

A scheduling guarantee, not a cap.
It only needs to clear the roughly 23%-busy baseline measured under one query
stream, with margin for more than one request in flight at a time; the limit
above is what actually bounds bursts.

## api.resources.limits.memory: 512Mi (unchanged)

Left at 512Mi.
Track B's memory profile, taken with 5.9k memories loaded, showed roughly
60-85MB of heap in use, so the limit already carries 6-8x headroom over what
was measured.
That headroom is deliberate: it covers corpora larger than what was profiled,
and covers the parts of the process's memory footprint that `GOMEMLIMIT`
(below) does not bound -- goroutine stacks and off-heap allocations.

## GOMEMLIMIT: computed, not the raw limit

`GOMEMLIMIT`'s grammar accepts only `^[0-9]+(([KMGT]i)?B)?$` or the literal
`off`.
A Kubernetes memory quantity like `512Mi` does not match that grammar and
crashes the process at startup if passed through unchanged.
A bare integer byte count (no suffix) does match, and is what this chart
emits.

The value is not the raw limit converted to bytes, either.
The Go garbage collector guide recommends leaving 5-10% headroom under the
container's memory limit rather than setting `GOMEMLIMIT` equal to it, so a
GC pause that briefly needs more than the target still has somewhere to go
before the kernel OOM-kills the container.
This chart uses 90% of `api.resources.limits.memory`, the more conservative
end of that range.

The obvious Kubernetes-native way to get a byte count -- a Downward API
`resourceFieldRef` pointed at `limits.memory` -- was rejected because it can
only emit the limit itself.
There is no way to have it apply a percentage.
So the chart computes 90% of the limit at template render time instead, via
the `kora.gomemlimit` helper in `_helpers.tpl`, reusing the existing
`kora.bytes` quantity parser.
If no memory limit is set, no `GOMEMLIMIT` is emitted, matching the existing
behaviour for that case.

## PodDisruptionBudget: unhealthyPodEvictionPolicy: AlwaysAllow

`policy/v1`'s `unhealthyPodEvictionPolicy` field has been GA since Kubernetes
1.31.
Without it, a crash-looping pod -- unhealthy, and therefore contributing
nothing to availability regardless of what the PDB decides -- still counts as
"unavailable" against the budget and can block a voluntary eviction, such as
a node drain, indefinitely.
`AlwaysAllow` makes unhealthy pods evictable unconditionally, so a node drain
is never blocked by a pod that was never going to serve traffic again anyway.
The existing `maxUnavailable: 1` and the replicas-greater-than-1 gate on the
whole PDB object are unchanged.

## HorizontalPodAutoscaler (api.hpa, disabled by default)

An HPA is added to the chart, but off by default, because scaling the API on
either CPU or request count is close to meaningless for this workload: the
CPU comment above already establishes that a query spends 77% of its time
idle, waiting on Postgres, and adding API replicas does nothing to make
Postgres answer faster.
It exists for the case where the bottleneck genuinely moves to the API side
of the system -- heavier extraction load, or a self-hosted embedding
provider colocated with the API pod, which this chart does not currently do.

When enabled, it scales on a Pods metric, `kora_requests_per_second`, rather
than CPU.
That metric name does not exist as something the API exports directly.
It is a `prometheus-adapter` external/custom metrics rule computed from the
engine's real Prometheus counter, `kora_requests_total`
(`internal/metrics/metrics.go`), which is already incremented per RPC with
`method` and `code` labels by the gRPC interceptor in
`internal/logging/interceptor.go`.
A rule roughly like the following has to be installed alongside
`prometheus-adapter` for the HPA to have anything to read; without it the HPA
sits at `minReplicas` because the metrics API returns nothing for the name it
asks for:

```yaml
rules:
  - seriesQuery: 'kora_requests_total{namespace!="",pod!=""}'
    resources:
      overrides:
        namespace: {resource: "namespace"}
        pod: {resource: "pod"}
    name:
      matches: "kora_requests_total"
      as: "kora_requests_per_second"
    metricsQuery: 'sum(rate(<<.Series>>{<<.LabelMatchers>>}[2m])) by (<<.GroupBy>>)'
```

`api.hpa.targetRequestsPerSecond` (default 50) is a starting point for
operators to tune, not a measurement: Track B profiled the cost of a single
query in isolation, not sustained per-pod throughput at varying replica
counts under realistic traffic, and this repo has no soak test that produced
a per-pod throughput ceiling to target instead.

`api.hpa.targetCPUUtilizationPercentage` (default 0, meaning off) is a CPU
fallback metric, added only if the operator sets it explicitly, for the case
described above where the bottleneck really has moved to the API.
An HPA with more than one metric type scales to whichever one asks for the
most replicas, so turning this on can only ever push the replica count up
relative to the requests metric alone, never down.

`minReplicas: 2`, `maxReplicas: 6`.
The scale-up behavior is throttled to one pod per 60 seconds rather than the
HPA's own default (which can double the fleet every 15 seconds), because each
new API replica claims up to `api.pool.maxConns` (10) connections against
Postgres's `max_connections` (100); at `maxReplicas: 6` the fleet claims at
most 60 total, leaving headroom, but only if replicas arrive slowly enough
for Postgres's own connection accounting and this process's pool ramp-up to
keep pace.
A burst of several new pods at once risks a thundering herd of connection
attempts against the database that this chart has already established is the
bottleneck.
Scale-down stabilization is left at the HPA's own default of 300 seconds,
written explicitly in the manifest rather than left implicit, because nothing
about this workload argues for scaling down faster and a database-bound
service flapping its replica count adds pool churn for no throughput benefit.

When the HPA is enabled, the Deployment's `spec.replicas` field is omitted
entirely rather than set to `api.replicas`.
A Deployment's `replicas` field and an HPA both want to be the sole owner of
the replica count; leaving Helm in charge of it too means every
`helm upgrade` resets the count back to `api.replicas`, fighting whatever the
autoscaler had already decided.
Omitting the field is the pattern the Kubernetes HPA documentation itself
recommends.

## topologySpreadConstraints: unchanged

The existing hostname-based spread (`maxSkew: 1`, `topologyKey:
kubernetes.io/hostname`, `whenUnsatisfiable: ScheduleAnyway`) is kept as is.
`matchLabelKeys: [pod-template-hash]` was considered and deliberately not
added.
It is beta and default-on since Kubernetes 1.27, so it is available, but its
purpose is to spread each rolling update's new ReplicaSet independently of
the pods still terminating from the old one, which matters on fleets doing
frequent rolling updates at a large scale.
At this chart's replica counts (`minReplicas`/`maxReplicas` of 2-6) it adds a
beta-gated field for no benefit that would be observable in practice.

## preStop: exec vs the native sleep action

The chart's `lifecycle.preStop` sleep -- which exists to give Kubernetes'
endpoint-removal propagation a head start over container termination, so a
proxy does not keep routing to a socket that has already closed -- can now be
expressed two ways.
The native `sleep` lifecycle action reached GA in Kubernetes 1.34, having been
beta and default-on since 1.30; it needs no `sleep` binary in the container
image, unlike the `exec: command: ["sleep", "N"]` form the chart used before.

`api.preStopSleepAction` (default `false`) selects between them.
Default `false` keeps the exec form, which is unconditionally correct on
every Kubernetes version this chart supports (1.34-1.37) and on anything
older too.
The native action's guarantee only starts at 1.34's GA; on a 1.30-1.33
control plane with the beta feature gate turned off, it would silently
produce no preStop delay at all rather than failing loudly, which is a worse
failure mode than the extra process fork the exec form costs on every pod
termination.
Set `api.preStopSleepAction: true` once the target cluster is known to be
1.34 or newer.

## terminationGracePeriodSeconds: enforced invariant

The process's own drain is a fixed 15 seconds.
Whatever `preStopSleepSeconds` adds on top of that has to fit inside
`terminationGracePeriodSeconds`, or the kubelet SIGKILLs the process
mid-drain and the connection pool is never closed.
This was already true before this change; what is new is that the chart now
fails the template render if the invariant is violated, at the top of
`templates/api.yaml`, the same way `templates/postgres.yaml` already refuses
to render a `shmSize` that would exceed `postgres.resources.limits.memory`.
The three probes (`startupProbe`, `livenessProbe`, `readinessProbe`) were
reviewed against `internal/server/probes.go` and left unchanged: their
semantics already match what that file documents (startup covers schema
initialization, liveness never touches the database, readiness does a
bounded database ping plus a draining flag), and no change to Kubernetes 1.37
probe behaviour affects them.

## CronJob: startingDeadlineSeconds, singleton pattern, idempotency

`consolidation.startingDeadlineSeconds` (default 600) is new.
If the CronJob controller is down or backlogged past this many seconds past
the scheduled time, the run is skipped rather than started late; 600 seconds
is comfortably above a typical control-plane hiccup while still bounding how
stale the skipped run's decay/prune math could get before the next scheduled
attempt.
Values much under 10 seconds risk the run never firing at all, since that is
close to the CronJob controller's typical reconciliation latency.

`concurrencyPolicy: Forbid` was already set and is unchanged; it is what
makes this job a singleton, since Kubernetes itself refuses to start a second
run while one is still in progress.
Because of that, `coordination.k8s.io/v1` `Lease`-based leader election
(`client-go`'s `leaderelection` package) is not needed: there is no
always-on replica doing this job's work between scheduled runs for a lease to
arbitrate between.
`Forbid` only prevents two runs overlapping; it does not protect against a
run crashing partway through and leaving partial decay/prune state, so each
run still has to be safe to retry from scratch, which is an existing
property of the consolidation job rather than something this change adds.

## Why the vector index and graph do not need a warm-start story

The pgvector HNSW index and the AGE graph both live inside PostgreSQL, which
this chart already runs as a `StatefulSet` backed by a `PersistentVolumeClaim`
(`templates/postgres.yaml`).
The API deployment itself is stateless; it holds no index or graph state of
its own to rebuild on start.

A Postgres pod restart is cheap for exactly that reason: the HNSW index and
the graph are already on disk in the PVC, so a restart replays WAL and comes
back with the same index rather than rebuilding it.
Rebuild-from-scratch cost only applies to the case `postgres.shmSize`'s
comment already documents -- restoring a backup, where `pg_restore` skips the
index and it is rebuilt in one pass, which is why `/dev/shm` is sized for that
in this chart rather than for steady-state operation.

The API's own "warm start" is just its `startupProbe` covering
`InitSchema`, which creates the AGE graph and its property indexes on a cold
database and is slow specifically on that first run.
There is no cache in the API process that a restart empties and needs to
refill; the state that matters is Postgres's, and Postgres's own probes
(`startupProbe` using `pg_isready`, generous `failureThreshold` for a cold
`initdb` or WAL replay after an unclean shutdown) already cover that.

## What we deliberately did not do

- **`uber-go/automaxprocs`.** Redundant with, and slightly worse than, the Go
  1.25+ stdlib behaviour this chart already relies on. See "GOMAXPROCS"
  above.
- **CPU-based HorizontalPodAutoscaler by default.** A query spends 77% of its
  time waiting on Postgres; scaling API replica count on CPU utilization
  scales the wrong resource. The optional `api.hpa.targetCPUUtilizationPercentage`
  fallback exists for operators who have evidence otherwise.
- **`matchLabelKeys: [pod-template-hash]` on `topologySpreadConstraints`.**
  Available (beta, default-on since 1.27) but solves a large-fleet,
  frequent-rollout problem this chart's replica counts do not have.
- **`coordination.k8s.io/v1` `Lease` leader election for the consolidation
  CronJob.** `concurrencyPolicy: Forbid` already makes the job a singleton;
  there is no always-on replica for a lease to arbitrate between.

## Sources

- https://go.dev/doc/go1.25
- https://go.dev/doc/gc-guide
- https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/
- https://kubernetes.io/docs/tasks/run-application/configure-pdb/
- https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/
- https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/
