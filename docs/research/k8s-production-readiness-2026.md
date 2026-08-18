# Kubernetes-native production readiness for Context0 (Go + PostgreSQL/AGE + pgvector)

Research report, 2026-08-18. No code was modified.
Legend: **[V]** = verified against a cited primary source. **[I]** = my inference / engineering judgment.
Version-sensitive claims are flagged **[VER]**.

---

## 0. Current-state facts about this repo (read from source, not inferred)

- `charts/context0/templates/api.yaml`: liveness and readiness are the *same* HTTP probe on `/v1/health`,
  no `startupProbe`, no `preStop`, no `terminationGracePeriodSeconds`, no PDB, no HPA, no topologySpread,
  no ServiceMonitor. Service is a plain ClusterIP/NodePort fronting both `grpc` and `http` ports.
- `internal/auth/apikey.go:125`: `/v1/health` and `/metrics` bypass auth. Good - probes work; but it also means
  `/metrics` is publicly reachable wherever the Service is exposed.
- `cmd/server/main.go`: shutdown is `grpcServer.GracefulStop()` **then** `httpServer.Shutdown(ctx)`.
  This ordering is backwards for a grpc-gateway topology (see §2.4) and `Shutdown(ctx)` reuses the long-lived
  root `ctx`, so there is no bounded drain deadline.
- `Dockerfile`: `alpine:3.20` runtime with `wget` installed solely for healthcheck; static `CGO_ENABLED=0` build.
  No `automaxprocs`, no `GOMEMLIMIT`.
- Postgres is a hand-rolled single-replica StatefulSet (`templates/postgres.yaml`).

---

## 1. Health probe design (gRPC + HTTP Go service)

### 1.1 The three probes are three different questions **[V]**
Kubernetes defines liveness (restart the container), readiness (remove from Service endpoints), and startup
(suppress the other two until the app has booted).
<https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/>

The startup probe exists specifically so you can keep liveness aggressive without penalizing slow boot:
"set up a startup probe with the same command, HTTP or TCP check, with a `failureThreshold * periodSeconds`
long enough to cover the worst case startup time." **[V]** (same page)

Context0's boot does `repo.InitSchema(ctx)` (AGE graph creation) before serving. That is exactly the
slow-first-start case a startupProbe is for. **[I]**

### 1.2 Native gRPC probe: GA, and what it costs you **[V] [VER]**
The built-in `grpc:` probe is `Kubernetes v1.27 [stable]`. Details from the same doc:

- probes run against the **Pod IP**, so the gRPC listener must not bind `127.0.0.1`;
- **no auth params** (`-tls` etc. unsupported);
- **no named ports** for gRPC probes (HTTP/TCP probes can use named ports);
- all errors are probe failures, no error-code granularity.

Practical consequence: since Kubernetes ships this natively from 1.27, shipping `grpc-health-probe` as an
`exec` probe is now legacy. The doc explicitly notes the exec-based `grpc-health-probe` does *not* respect
`timeoutSeconds` when `ExecProbeTimeout=false`, while the built-in probe does. **[V]**
If the chart must support clusters older than 1.27, keep an HTTP fallback rather than reintroducing
grpc-health-probe. **[I]**

The probe speaks the gRPC Health Checking Protocol (`grpc.health.v1.Health/Check`, empty service name = overall
server status, `SERVING` / `NOT_SERVING`). <https://github.com/grpc/grpc/blob/master/doc/health-checking.md> **[V]**

Go implementation is one import: `google.golang.org/grpc/health` gives `health.NewServer()` with
`SetServingStatus`, plus `Shutdown()` (flip everything to NOT_SERVING and freeze) and `Resume()`.
<https://pkg.go.dev/google.golang.org/grpc/health> **[V]**

> Note for this repo: Context0 registers its *own* `HealthService` proto, not `grpc.health.v1.Health`.
> The native k8s `grpc:` probe will not work until the standard service is also registered. **[I]**

### 1.3 What readiness should check, and why liveness must NOT touch the DB
Readiness for a DB-backed query service should be a **cheap, bounded** check that the pod can serve *right now*:
pool acquirable within a short timeout (e.g. `pgxpool.Ping` with a ~1s context), schema initialized, and a
process-local `draining` flag that flips to false on SIGTERM. **[I]**

The argument against DB connectivity in **liveness** is a correlated-failure argument, and it is strong:

- Liveness failure means *kill and restart the container*
  (<https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/>) **[V]**.
- If Postgres is briefly unavailable (failover, WAL replay, network blip), a DB-checking liveness probe fails on
  **every replica simultaneously**, so Kubernetes restarts the whole fleet at once. Restarting a Go process
  cannot fix a remote database, and repeated restarts push pods into `CrashLoopBackOff` with exponential backoff
  (<https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/>) **[V]**, so recovery is now *slower* than
  the original outage. The restart also drops the warm connection pool, adding a reconnect storm on the database
  right as it comes back. **[I]**
- Correct split: **liveness = "is this process wedged?"** (a static handler served by the same mux, proving the
  HTTP server goroutine and scheduler are alive), **readiness = "can this pod serve a request end to end?"**
  (includes the DB). Readiness failure removes the pod from endpoints without destroying it, which is the
  reversible action you want for a dependency outage. **[I]**
- CloudNativePG applies exactly this pattern internally: it is the *readiness* probe on the primary that fails
  and triggers failover, not a restart loop.
  <https://cloudnative-pg.io/docs/1.25/failover> **[V]**

### 1.4 Concrete recommendation for Context0 **[I]**

| Probe | Endpoint | Semantics | Suggested timings |
|---|---|---|---|
| startup | `/livez` (HTTP) or `grpc:` | covers `InitSchema` + first pool connect | `periodSeconds: 5`, `failureThreshold: 30` (=150s budget) |
| liveness | `/livez` - static 200, no DB | process wedged only | `periodSeconds: 10`, `failureThreshold: 3`, `timeoutSeconds: 2` |
| readiness | `/readyz` - pool ping + draining flag | serve/don't-serve | `periodSeconds: 5`, `failureThreshold: 2`, `timeoutSeconds: 2` |

Keep `/livez` and `/readyz` in the unauthenticated allowlist alongside the existing `/v1/health`; consider moving
`/metrics` off the public Service onto a separate container port so a ServiceMonitor can scrape it without
exposing it. **[I]**

---

## 2. Graceful shutdown and zero-downtime rollout

### 2.1 The endpoint-removal race **[V] for mechanism, [I] for framing**
Pod deletion runs two things **concurrently**: the kubelet begins termination (preStop, then SIGTERM), and the
endpoints controller removes the pod from Service endpoints. Nothing sequences them. So a pod can receive SIGTERM
and stop accepting connections while kube-proxy / ingress / a service mesh on some node still lists it as a valid
backend, producing connection-refused errors on a rollout that "should" be zero downtime.

Kubernetes documents that `preStop` runs **before** the TERM signal and that the TERM signal cannot be sent until
the hook completes: "A call to the `PreStop` hook ... must complete before the TERM signal to stop the container
can be sent" and "the Pod's termination grace period countdown begins before the `PreStop` hook is executed."
<https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/> **[V]**

That is the whole remedy: use preStop to buy wall-clock time for endpoint propagation before your process starts
refusing work.

### 2.2 The sleep remedy, modern form **[V] [VER]**
Kubernetes now has a **native `sleep` hook handler**, executed by the kubelet, no shell needed:
"There are three types of hook handlers ... Exec ... HTTP ... **Sleep - Pauses the container for a specified
duration**" and "`httpGet`, `tcpSocket` (deprecated) and `sleep` are executed by the kubelet process."
<https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/> **[V]**

```yaml
lifecycle:
  preStop:
    sleep:
      seconds: 10          # >= endpoint propagation time in your cluster
terminationGracePeriodSeconds: 60   # >= preStop + longest in-flight RPC + margin
```

This matters a lot for a **distroless/scratch** image: the old `exec: ["/bin/sh","-c","sleep 10"]` idiom requires
a shell in the image, and it is the single most common reason teams keep alpine. The `sleep` handler removes that
dependency, so §7's distroless recommendation and this one compose. **[I]**

Budget arithmetic is documented and unforgiving: grace period covers preStop **plus** normal stop. The doc's own
example - `terminationGracePeriodSeconds: 60`, a 55s hook and a 10s stop - results in the container being killed
before it finishes. **[V]** So always set `terminationGracePeriodSeconds > preStop + max RPC duration`.

### 2.3 Correct Go shutdown shape **[I], built on [V] API semantics**
API facts: `grpc.Server.GracefulStop` stops accepting new connections/RPCs and blocks until all pending RPCs
finish; `http.Server.Shutdown(ctx)` gracefully closes listeners then idle connections, and returns
`ErrServerClosed` from `ListenAndServe`
(<https://pkg.go.dev/net/http#Server.Shutdown>). **[V]**

```go
// 1. signal handling: use NotifyContext, not a bare channel.
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

// ... start servers ...

<-ctx.Done()
stop() // second signal now kills hard - operator escape hatch

// 2. Flip readiness FIRST, and keep serving. The preStop sleep is still running,
//    so we are still in endpoints; we want to keep answering during that window.
healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING) // grpc.health.v1
ready.Store(false)                                                       // /readyz -> 503

// 3. Bounded drain deadline, derived from terminationGracePeriodSeconds.
drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

// 4. HTTP (grpc-gateway) FIRST: the gateway is a *client* of the local gRPC server.
if err := httpServer.Shutdown(drainCtx); err != nil { ... }

// 5. Then gRPC, with a hard backstop so a hung stream cannot eat the grace period.
done := make(chan struct{})
go func() { grpcServer.GracefulStop(); close(done) }()
select {
case <-done:
case <-drainCtx.Done():
    grpcServer.Stop() // force
}

// 6. Only now release the pool.
pool.Close()
```

**Two real bugs in `cmd/server/main.go` today** (both [I], derived from the code above):
1. **Ordering is inverted.** `GracefulStop()` runs before `httpServer.Shutdown()`. The grpc-gateway mux dials the
   local gRPC server (`RegisterContext0HandlerFromEndpoint(ctx, gwMux, cfg.GRPCAddr(), ...)`), so killing gRPC
   first makes every in-flight *REST* request fail with `Unavailable` during the drain. HTTP must drain first.
2. **Unbounded drain.** `httpServer.Shutdown(ctx)` passes the long-lived root context, so there is no deadline;
   a slow client can hold shutdown until the kubelet SIGKILLs at the grace period, and `pool.Close()` /
   `repo.Close()` never runs. There is also no `grpcServer.Stop()` backstop.

Additionally, `grpc.NewServer` is constructed with no `keepalive.ServerParameters` (see §3.3) and no
`MaxConnectionAge`, and the gateway's internal dial uses `insecure` credentials to itself - fine for loopback,
worth a comment. **[I]**

### 2.4 Rollout knobs **[I]**
`maxUnavailable: 0` + `maxSurge: 1` on the Deployment strategy, so capacity never dips during a rollout; combined
with a PDB (`maxUnavailable: 1`, recommended over `minAvailable` because it "automatically responds to changes in
the number of replicas" per <https://kubernetes.io/docs/tasks/run-application/configure-pdb/> **[V]**), and
`topologySpreadConstraints` on `kubernetes.io/hostname` so a single node drain cannot take the whole API tier.
Note the doc's warning: a PDB that permits zero voluntary evictions will make `kubectl drain` hang forever. **[V]**

---

## 3. Autoscaling

### 3.1 CPU HPA: the default, and its specific failure mode here **[V]**
The HPA algorithm is `desiredReplicas = ceil(currentReplicas * currentMetric / desiredMetric)`, evaluated every
15s (`--horizontal-pod-autoscaler-sync-period`), with a 0.1 tolerance and a 5-minute downscale stabilization
window. Crucially: "if some of the Pod's containers do not have the relevant **resource request** set, CPU
utilization for the Pod will not be defined and the autoscaler will not take any action."
<https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/> **[V]**

So step zero for Context0 is setting real CPU/memory **requests** in `values.yaml`. Without requests, a CPU HPA is
silently inert. **[V] + [I]**

The same doc's "Pod readiness and autoscaling metrics" section is directly relevant to a service that runs
`InitSchema` and warms an embedding model at boot: CPU samples are ignored for
`--horizontal-pod-autoscaler-cpu-initialization-period` (default 5 min) unless the Pod is stably Ready, and the
documented good practice is "Configure a `startupProbe` that doesn't pass until the high CPU usage has passed."
**[V]** This ties §1 and §3 together: without a startupProbe, boot CPU spikes can drive spurious scale-ups.

### 3.2 What metric actually makes sense for a query-serving API **[I]**
CPU is a *proxy* for saturation and it is a bad proxy for Context0 specifically: a vector-similarity + AGE graph
query spends most of its wall time **blocked on Postgres**, so p99 latency rises long before pod CPU does. Ranked:

1. **Concurrency / in-flight requests per pod** (Little's Law: `concurrency = RPS x latency`). This is the single
   best autoscaling signal for a latency-sensitive request-response service, because it captures both traffic
   volume and downstream slowness in one number, and it is the quantity your pool size actually bounds. Export as
   a gauge; target ~70% of `pgxpool.MaxConns`.
2. **`pgxpool` acquire wait time / `EmptyAcquireCount`** - directly says "this pod is pool-starved."
   Exposed by `pgxpool.Stat` (`EmptyAcquireWaitTime`, `EmptyAcquireCount`, `AcquireDuration`)
   <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool> **[V]**.
3. **RPS per pod** - simple, works, but blind to query-mix changes.
4. **p99 latency as a scaling target** - tempting and usually wrong: latency-driven autoscaling is a positive
   feedback loop when the bottleneck is the shared database. More API pods = more pool connections = *worse*
   Postgres latency. Use latency as an **alert**, not as a scaling metric, until Postgres is horizontally read-scaled.

Caution that applies to all of these **[V]**: HPA takes the **max** desired-replica count across metrics, and
skips scale-down entirely if any metric errors. Multi-metric HPAs bias upward by design.

### 3.3 Prometheus Adapter vs KEDA **[V] for capability, [I] for the pick]**
Both feed the same `custom.metrics.k8s.io` / `external.metrics.k8s.io` aggregated APIs the HPA reads
(<https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/>) **[V]**.

- **prometheus-adapter**: cluster-scoped config, rules written in its own DSL, one more control-plane component
  to own. Right when you already run kube-prometheus-stack and want per-pod custom metrics with no new CRDs. **[I]**
- **KEDA**: `ScaledObject` CRD wrapping an arbitrary PromQL query, with `threshold`, `activationThreshold`
  (the scale-from-zero gate), auth via `TriggerAuthentication`, and `ignoreNullValues` to control what happens
  when the Prometheus target disappears. <https://keda.sh/docs/2.17/scalers/prometheus/> **[V]**

**Recommendation [I]:** ship a plain CPU HPA in the chart (`autoscaling.enabled`, default false) as the
zero-dependency baseline, and ship an *optional* KEDA `ScaledObject` template gated on
`autoscaling.keda.enabled`. KEDA is the better fit for an OSS chart because the scaling policy lives in one
readable namespaced CR that a user can copy, rather than in a cluster-scoped adapter config only an admin can
edit. Set `ignoreNullValues: false` so a Prometheus outage surfaces loudly instead of silently pinning replicas.

### 3.4 HPA + long-lived gRPC connections: the load-imbalance problem **[V]**
This is the biggest autoscaling gotcha for Context0 and it is well documented:

> "gRPC breaks the standard connection-level load balancing, including what's provided by Kubernetes. This is
> because gRPC is built on HTTP/2, and HTTP/2 is designed to have a single long-lived TCP connection, across which
> all requests are multiplexed ... Once the connection is established, there's no more balancing to be done. All
> requests will get pinned to a single destination pod."
> <https://kubernetes.io/blog/2018/11/07/grpc-load-balancing-on-kubernetes-without-tears/> **[V]**

Consequence: **scaling up does nothing.** New pods join the Service, receive zero traffic because existing
clients never re-dial, average CPU stays high, HPA scales up again - a runaway that adds cost and no capacity. **[I]**

Remedies, in ascending order of infrastructure cost:

1. **`MaxConnectionAge` on the server** - cheapest, purely in-process, no cluster dependency.
   `keepalive.ServerParameters.MaxConnectionAge` is "the maximum amount of time a connection may exist before it
   will be closed by sending a GoAway. A random jitter of +/-10% will be added to MaxConnectionAge to spread out
   connection storms", with `MaxConnectionAgeGrace` as "an additive period after MaxConnectionAge after which the
   connection will be forcibly closed."
   <https://pkg.go.dev/google.golang.org/grpc/keepalive> **[V]**
   Forcing clients to re-dial periodically (e.g. 30m age + 5m grace) restores approximate balance, and the
   built-in jitter is exactly the thundering-herd protection you would otherwise hand-roll. **[I]**
   Also set `Time`/`Timeout` (server defaults: 2h ping interval, 20s timeout **[V]**) to detect dead peers faster,
   and an `EnforcementPolicy` (`MinTime` default 5 minutes **[V]**) so aggressive clients are not GOAWAY'd
   unexpectedly.
2. **Client-side round-robin over a headless Service** - "Kubernetes will create multiple A records in the DNS
   entry for the service. If our gRPC client is sufficiently advanced, it can automatically maintain the load
   balancing pool from those DNS entries. But this approach restricts us to certain gRPC clients." **[V]**
   Viable because Context0 ships its own SDK, but it forces every consumer onto that SDK. **[I]**
3. **L7 proxy / service mesh** (Linkerd, Envoy, Istio, or a gRPC-aware ingress) - balances per *request* rather
   than per connection. **[V]** Correct and general, but a heavy dependency to assume in an OSS chart. **[I]**

**Recommendation [I]:** implement (1) in `grpc.NewServer` unconditionally with chart-tunable values - it is a
~5-line change that makes HPA meaningful without asking users to install a mesh - and document (3) as the
production path. Do not ship an HPA without doing (1) first, or the HPA is worse than no HPA.

---

## 4. PostgreSQL on Kubernetes: CloudNativePG

### 4.1 Is CNPG the consensus? **[I], with [V] supporting evidence**
I could not run comparative web searches (the search backend returned anti-bot challenges), so I will not assert
a market-share claim. What is verifiable: CNPG is a **CNCF project** under LF Projects governance
(<https://cloudnative-pg.io/docs/1.30/image_catalog>, footer) **[V]**, ships a documented release train through
**1.30** with 1.25 already marked "no longer actively maintained" **[V] [VER]**, and covers the full operational
surface below. My judgment: for a *new* Go+Postgres service in 2026 with no existing operator investment, CNPG is
the default choice, and this project's own docs already name it as target state. **[I]**

### 4.2 What CNPG gives you over a hand-rolled StatefulSet **[V]**

| Capability | Hand-rolled StatefulSet (today) | CNPG |
|---|---|---|
| Failover | none - single replica, manual | Automated two-phase failover on readiness-probe failure, with leader election, `failoverDelay` to ride out blips, `switchoverDelay` for the fast-shutdown/RPO tradeoff. <https://cloudnative-pg.io/docs/1.25/failover> |
| Backups | none | `ScheduledBackup` CRD (six-field cron incl. seconds), on-demand `Backup` CRD, object store (Barman Cloud) or CSI VolumeSnapshots, backup-from-standby by default. <https://cloudnative-pg.io/docs/1.25/backup> |
| PITR | none | Continuous WAL archiving; "CloudNativePG provides out-of-the-box an **RPO <= 5 minutes** for disaster recovery, even across regions." Same page. |
| Pooling | none | `Pooler` CRD = an HA PgBouncer Deployment with TLS passthrough, `auth_query` via a dedicated `cnpg_pooler_pgbouncer` role, and a tunable parameter allowlist. <https://cloudnative-pg.io/docs/1.25/connection_pooling> |
| Minor upgrades | manual image edit, no orchestration | `ImageCatalog`/`ClusterImageCatalog` - "when a catalog entry is updated, all associated clusters automatically roll out the new image", images pinned by **SHA256 digest**. <https://cloudnative-pg.io/docs/1.30/image_catalog> |
| Extensions/DB objects | ad-hoc SQL in initContainers | `Database` CRD with declarative `extensions`, `schemas`, `fdws`. <https://cloudnative-pg.io/docs/1.30/declarative_database_management> |

Two important CNPG caveats **[V]**:
- **No logical backup.** "logical backups are not suitable for business continuity use cases and as such are not
  covered by CloudNativePG"; `pg_dump` is out of scope.
- **WAL archive must be on an object store** - VolumeSnapshots alone do not give you PITR.
- Backups **do not include** the superuser/app secrets; those are your problem.

### 4.3 Running AGE + pgvector under CNPG - three routes

**Route A - custom operand image (works today, all PG versions) [V]**
CNPG works with "any compatible container image of PostgreSQL" provided `initdb`, `postgres`, `pg_ctl`,
`pg_controldata`, `pg_basebackup` **and** the seven `barman-cloud-*` binaries are on `PATH`, plus appropriate
locales. No ENTRYPOINT/CMD needed - "CloudNativePG overrides it with its instance manager." The image tag must
start with the PostgreSQL major version (`16`, `16.4`, `17.6-1`); **`latest` is rejected**.
<https://cloudnative-pg.io/docs/1.25/container_images> **[V]**

So: `FROM ghcr.io/cloudnative-pg/postgresql:<major>` and build AGE + pgvector on top. This is the only route that
works for AGE today. **[I]**

**Route B - ImageVolume extensions (PG 18+, the modern route) [V] [VER]**
CNPG can mount an extension as an OCI image at `/extensions/<name>`, auto-wiring `extension_control_path` and
`dynamic_library_path`. Requirements are strict: **PostgreSQL 18+** (needs `extension_control_path`),
**Kubernetes 1.35+** (default-on; 1.33/1.34 need the `ImageVolume` feature gate), and containerd >= 2.1.0 or
CRI-O >= 1.31. Community images exist for **pgvector** and PostGIS; catalogs with an `extensions` section need
CNPG **>= 1.29**. <https://cloudnative-pg.io/docs/1.30/imagevolume_extensions> **[V]**

I found **no official CNPG extension image for Apache AGE** in the documented set (pgvector and PostGIS are named;
AGE is not). **[V - absence in cited doc]** Building an AGE extension image to the documented spec (`/share/extension/*.control`
+ `/lib/*.so`) looks feasible, and multi-extension images are explicitly supported (the PostGIS+pgRouting example),
but AGE needs `shared_preload_libraries` handling - the docs note modules loaded that way "must be loaded via
`shared_preload_libraries` at server start" and that this must be added **manually** to the `Cluster` spec even
when the binaries arrive via image volume. **[V]** Whether AGE's `.so` loads cleanly this way is **unverified and
must be tested**. **[I - flag as open risk]**

**Route C - drop AGE.** Out of scope for this report, but worth noting: the ImageVolume path is materially easier
if the graph layer were expressible in plain SQL + pgvector, because pgvector is a first-class community image.
**[I]**

**Extension activation** in all routes: use the `Database` CRD rather than init SQL -
`extensions: [{name: vector, version: "..."}]` makes CNPG run `CREATE EXTENSION IF NOT EXISTS` and keeps it
reconciled, without granting the app superuser. **[V]**

**Migration risk [I]:** Route A gives failover/backup/PITR immediately with a Dockerfile change and no PG major
upgrade. Route B is cleaner long-term but gates you on PG 18 **and** Kubernetes 1.35 - too new to require of OSS
users in 2026. Recommend Route A now, Route B as a documented future path.

---

## 5. Connection pooling

### 5.1 pgxpool defaults and the multiplication problem **[V] for defaults, [I] for sizing]**
`pgxpool.Config` defaults: `MaxConns` = "the greater of 4 or `runtime.NumCPU()`".
<https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool> **[V]**

That default is a landmine under Kubernetes. `runtime.NumCPU()` historically reports the **node's** core count,
not the pod's CPU limit, so on a 64-core node a pod limited to 500m still opens up to 64 connections. Multiply by
HPA replicas and you exhaust Postgres `max_connections` (default 100) at 2 replicas. **[I]** Go 1.25's
container-aware GOMAXPROCS (§7) changes `runtime.GOMAXPROCS` but `NumCPU()` still reports logical CPUs - so
**always set `MaxConns` explicitly**, never rely on the default. **[I]**

Sizing rule **[I]**: `replicas_max * MaxConns + reserved <= max_connections`.
With `max_connections = 200`, `maxReplicas = 10`, reserve ~20 for admin/CNPG: `MaxConns <= 18`. Start at 10-15.
Also set `MinIdleConns` (docs: "superior to MinConns for this purpose ... avoid the latency of establishing a new
connection while handling requests" **[V]**) to cut cold-start tail latency, and `MaxConnLifetime` +
`MaxConnLifetimeJitter` ("helps prevent all connections from being closed at the exact same time, starving the
pool" **[V]**) so connections rebalance after a CNPG failover instead of pinning to a demoted primary.

Recommended starting config **[I]**:
```
MaxConns = 15, MinIdleConns = 2, MaxConnLifetime = 30m, MaxConnLifetimeJitter = 5m,
MaxConnIdleTime = 5m, HealthCheckPeriod = 1m, PingTimeout = 2s
```
All of these must be surfaced in `values.yaml`, since correct values depend on `maxReplicas`. **[I]**

### 5.2 Is PgBouncer warranted? **[I]**
Not initially. pgxpool is already a real pool with sane lifetime management; PgBouncer in front of it adds a
network hop, a failure domain, and prepared-statement constraints. It becomes warranted when:

- replica count x MaxConns approaches `max_connections` and you cannot lower `MaxConns` without starving pods;
- you add short-lived clients (the consolidation CronJob, CLI, serverless callers) that each pay full connect cost;
- you want connection multiplexing across a fleet larger than ~20 pods.

If you get there, CNPG's `Pooler` is the low-effort answer (`instances: 3`, podAntiAffinity across hosts, TLS and
`auth_query` wired automatically). **[V]** One mode caveat: the CNPG quickstart uses `poolMode: session`; only
`transaction` mode gives real multiplexing, and transaction mode breaks session-scoped state - session-level
`SET`, advisory locks, and (unless `max_prepared_statements` is set) prepared statements. pgx uses the extended
query protocol with statement caching by default, so moving to transaction pooling requires
`default_query_exec_mode=exec` / `simple_protocol` on the pgx side or PgBouncer >= 1.21 prepared-statement support.
**[I - verify against PgBouncer docs before adopting]**
Also heed CNPG's own topology warning about cross-AZ hops app -> pooler -> primary. **[V]**

---

## 6. Observability

### 6.1 Tracing gRPC + pgx **[V]**
- gRPC: `otelgrpc` stats handlers (`otelgrpc.NewServerHandler()` / `NewClientHandler()`) passed to
  `grpc.NewServer`. Note the interceptor-based API is deprecated in favor of stats handlers.
  <https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc>
- pgx: `otelpgx` is the maintained option - `cfg.ConnConfig.Tracer = otelpgx.NewTracer()` plus
  `otelpgx.RecordStats(pool)` which also emits pool metrics. Requires **go 1.25+** and pgx v5+.
  Useful options: `WithTrimSQLInSpanName()` (avoid unbounded span-name cardinality),
  `WithDisableSQLStatementInAttributes()` (do **not** ship raw SQL + embeddings to your trace backend),
  and `WithIncludeQueryParameters()` which you should leave off for a memory engine handling user content.
  <https://pkg.go.dev/github.com/exaring/otelpgx> **[V]**

Together these give a single trace spanning REST -> gateway -> gRPC handler -> pgx query, which is exactly the
span you need to answer "is p99 our code or the database?" **[I]**

Privacy note **[I]**: Context0 stores user memories. Default the pgx tracer to
`WithDisableSQLStatementInAttributes()` unless the operator opts in.

### 6.2 ServiceMonitor vs PodMonitor **[V]**
Both are `monitoring.coreos.com/v1`. ServiceMonitor selects Services and scrapes their endpoints; PodMonitor
"can bypass the service and find targets based on Pod labels". The Prometheus CR chooses which to pick up via
`serviceMonitorSelector` / `podMonitorSelector` - so the chart must let users set the label kube-prometheus-stack
expects (commonly `release: <name>`).
<https://prometheus-operator.dev/docs/developer/getting-started/> **[V]**

**Recommendation [I]:** ship both templates gated on `metrics.serviceMonitor.enabled` /
`metrics.podMonitor.enabled` (default false so the chart does not fail on clusters without the CRDs), with
configurable `additionalLabels`, `interval`, and `scrapeTimeout`. Prefer **PodMonitor** here: it lets you scrape a
metrics-only port that is not published on the public Service, which fixes the current "`/metrics` is
unauthenticated on the exposed Service" issue. Add a `PrometheusRule` with starter alerts.

### 6.3 Which RED/USE metrics matter **[I]**

**RED, per endpoint/RPC method** (the SLO surface):
- Rate: `grpc_server_handled_total` / `http_requests_total` by method+code
- Errors: same, ratio of non-OK; split client (`InvalidArgument`) from server (`Internal`, `Unavailable`)
- Duration: **native histograms** or well-chosen buckets. Pick buckets around your actual SLO
  (e.g. 5ms..2.5s for vector search); default `prometheus.DefBuckets` tops out at 10s and is too coarse at the
  low end for a query API.

**USE, for the resources this service actually saturates** - and for Context0 the scarce resource is *pool
connections*, not CPU:
- `pgxpool.Stat`: `AcquiredConns`/`MaxConns` (utilization), `EmptyAcquireCount` + `EmptyAcquireWaitTime`
  (saturation), `CanceledAcquireCount` (errors), `NewConnsCount`, `MaxLifetimeDestroyCount`. All available on
  `pgxpool.Stat` **[V]** and auto-exported by `otelpgx.RecordStats` **[V]**.
- In-flight request gauge (also your best HPA signal, §3.2).
- Go runtime: `go_goroutines`, `go_memstats_*`, GC CPU fraction; plus **cgroup throttling**
  (`container_cpu_cfs_throttled_seconds_total`) which is the observable that proves §7's GOMAXPROCS point.
- Domain-specific: embedding-generation latency, AGE query latency, consolidation CronJob success/duration
  (a silently failing CronJob is a classic invisible outage).

---

## 7. Image and startup efficiency

### 7.1 distroless vs scratch vs alpine **[V] for facts, [I] for pick]**
`gcr.io/distroless/static-debian13` is ~2 MiB, "about 50% of the size of alpine (~5 MiB), and less than 2% of the
size of debian (124 MiB)", contains no package manager or shell, ships CA certificates, offers a `nonroot`
variant, and is cosign-signed keyless. A `:debug` variant with a busybox shell exists for incident response.
Kubernetes itself has used distroless since v1.15.
<https://github.com/GoogleContainerTools/distroless/blob/main/README.md> **[V]**

For a `CGO_ENABLED=0` Go binary - which Context0 already builds **[V, from Dockerfile]** - `static-debian13` is
the right target: you get CA certs, `/etc/passwd` with a nonroot user, and tzdata handling without a package
manager. Pure `scratch` is ~0 MiB smaller in practice but you must hand-copy CA certs and fabricate `/etc/passwd`,
and you lose the maintained CVE stream (distroless auto-PRs Debian updates **[V]**). **[I]**

The current Dockerfile's stated reason for alpine is *"alpine for healthcheck support"* + `wget` **[V, comment in
Dockerfile]**. That justification is obsolete: Kubernetes `httpGet`/`grpc` probes are executed by the **kubelet**,
not inside the container **[V, probes doc]**, and the `sleep` preStop handler is likewise kubelet-executed **[V,
lifecycle doc]**. Nothing in the k8s path needs a shell or `wget`. Only the docker-compose `HEALTHCHECK` does -
and that can use the Go binary itself with a `-healthcheck` flag. **[I]**

Note distroless has no shell, so `ENTRYPOINT` must be **vector form** - `ENTRYPOINT ["/context0-server"]` **[V]**.

Also add: `-ldflags="-s -w"` to strip, `-trimpath` for reproducibility, `USER nonroot:nonroot` (or the `:nonroot`
tag), and a `securityContext` with `runAsNonRoot: true`, `readOnlyRootFilesystem: true`,
`allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault` - none of which
the chart sets today **[V, absence in api.yaml]**. **[I]**

### 7.2 GOMAXPROCS and cgroup CPU limits **[V] [VER - this is the single most version-sensitive item]**
**Go 1.25 changed the default.** From the release notes:

> "On Linux, the runtime considers the CPU bandwidth limit of the cgroup containing the process, if any. If the
> CPU bandwidth limit is lower than the number of logical CPUs available, `GOMAXPROCS` will default to the lower
> limit. In container runtime systems like Kubernetes, cgroup CPU bandwidth limits generally correspond to the
> 'CPU limit' option. The Go runtime does **not** consider the 'CPU requests' option."
> "On all OSes, the runtime periodically updates `GOMAXPROCS` if the number of logical CPUs available or the
> cgroup CPU bandwidth limit change."
> <https://go.dev/doc/go1.25> **[V]**

Disabled automatically if `GOMAXPROCS` is set via env or `runtime.GOMAXPROCS`, or explicitly via GODEBUG
`containermaxprocs=0` / `updatemaxprocs=0`. **[V]**

**Implication for this repo:** the Dockerfile builds with `golang:1.26-alpine` **[V, from Dockerfile]**, i.e.
Go >= 1.25, so **`automaxprocs` is no longer needed** and adding it would be a regression - it would set
`GOMAXPROCS` explicitly and thereby *disable* the runtime's periodic re-reading of cgroup limits, which matters
when a VPA or a limit edit changes the quota at runtime. **[I, directly implied by the [V] quote]**
Keep automaxprocs only if you must support Go < 1.25 builds. **[I]**

Two conditions still bite **[I]**:
- The runtime honors **limits**, not **requests**. A pod with `requests.cpu: 500m` and *no* limit gets
  `GOMAXPROCS` = full node core count. For a Go service this is usually acceptable (better burst) but makes
  latency non-reproducible across node shapes. Set limits, or set `GOMAXPROCS` explicitly, and be deliberate.
- Why it matters at all: uber-go's measured data shows the cost of over-provisioned GOMAXPROCS under a 2-core
  quota - at the default (24) throughput fell from 44,715 to 22,191 RPS and p99.9 rose from 26ms to 76ms, with
  cgroup `nr_throttled` climbing; matching GOMAXPROCS to quota eliminated the throttling.
  <https://pkg.go.dev/go.uber.org/automaxprocs> **[V]** The mechanism is generic: more OS threads than quota
  allows means the CFS period ends mid-work and every runnable goroutine stalls for the rest of the period, which
  shows up precisely as tail latency. **[I]**

### 7.3 GOMEMLIMIT **[V]**
`debug.SetMemoryLimit` / `GOMEMLIMIT` is a **soft** limit: "the runtime undertakes several processes to try to
respect this memory limit, including adjustments to the frequency of garbage collections and returning memory to
the underlying system more aggressively." Initial setting is `math.MaxInt64` unless `GOMEMLIMIT` is set; suffixes
are powers of two (`MiB`, `GiB`). Critically, "it does not account for space used by the Go binary and memory
external to Go" - so it is not a substitute for a container memory limit.
<https://pkg.go.dev/runtime/debug#SetMemoryLimit> **[V]**

Unlike GOMAXPROCS, **Go does not auto-derive GOMEMLIMIT from cgroups** - no such change appears in the Go 1.25
notes **[V - absence]**. So this must be wired manually. The Kubernetes-native way needs no new dependency:

```yaml
env:
  - name: GOMEMLIMIT
    valueFrom:
      resourceFieldRef:
        resource: limits.memory
        divisor: 1
```
then apply a headroom factor in code (`debug.SetMemoryLimit(limit * 9 / 10)`), because the soft limit excludes
non-heap memory. Without this, a Go service under a container memory limit gets **OOMKilled** rather than
GC-pressured, since the runtime happily grows the heap past the cgroup limit. **[I]**

---

## 8. Prioritized recommendations

**P0 - correctness bugs and zero-cost wins (no new cluster dependencies)**
1. Fix shutdown ordering in `cmd/server/main.go`: readiness-off -> `httpServer.Shutdown(bounded ctx)` ->
   `grpcServer.GracefulStop()` with a `Stop()` backstop -> `pool.Close()`. Currently inverted and unbounded. (§2.3)
2. Add `preStop: sleep: 10` + `terminationGracePeriodSeconds: 60` to the API Deployment. Without it, every
   rollout drops connections. (§2.1-2.2)
3. Split probes: static `/livez` for liveness, DB-checking `/readyz` for readiness, add a `startupProbe`. Remove
   the DB dependency from liveness. (§1.3)
4. Set explicit `pgxpool.MaxConns` - do not ship the `NumCPU()` default. (§5.1)
5. Set `keepalive.ServerParameters{MaxConnectionAge, MaxConnectionAgeGrace}` on `grpc.NewServer`. Prerequisite
   for any meaningful HPA. (§3.4)
6. Real CPU/memory requests+limits in `values.yaml`. Without requests, HPA is inert. (§3.1)

**P1 - chart completeness**
7. PodDisruptionBudget (`maxUnavailable: 1`), `maxUnavailable: 0`/`maxSurge: 1` rollout strategy,
   topologySpreadConstraints, `securityContext`. (§2.4, §7.1)
8. Optional ServiceMonitor/PodMonitor + PrometheusRule, gated, with configurable labels; move `/metrics` off the
   public Service. (§6.2)
9. Optional CPU HPA, default off, documented as requiring item 5. (§3)
10. Register the standard `grpc.health.v1.Health` service so the native `grpc:` probe works, and wire it to the
    same drain flag. (§1.2)

**P2 - larger, higher-value**
11. Distroless runtime image; drop alpine+wget; `GOMEMLIMIT` from `limits.memory` via `resourceFieldRef`;
    do **not** add automaxprocs (Go 1.25+ already does this, and adding it would disable dynamic updates). (§7)
12. otelgrpc + otelpgx tracing, with SQL statement attributes off by default. (§6.1)
13. CNPG migration behind a chart flag: keep the StatefulSet as the default dev path, add a `Cluster` CR path with
    a custom operand image carrying AGE + pgvector (Route A), `ScheduledBackup`, and a documented restore drill.
    This is what buys failover + PITR, and it is the single largest production-readiness gap. (§4)
14. Optional KEDA `ScaledObject` on in-flight-requests or pool-saturation once metrics exist. (§3.3)

**Open questions requiring hands-on verification (do not treat as settled)**
- Whether Apache AGE can be packaged as a CNPG ImageVolume extension given its `shared_preload_libraries`
  requirement. No official AGE extension image is listed. (§4.3)
- PgBouncer transaction-mode compatibility with pgx's default extended-query/statement-caching behavior. (§5.2)
- Actual endpoint-propagation latency in target clusters, which sets the correct preStop sleep duration. (§2.2)

---

## Source list
Kubernetes: probes <https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/> |
pod lifecycle <https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/> |
lifecycle hooks <https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/> |
HPA <https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/> |
PDB <https://kubernetes.io/docs/tasks/run-application/configure-pdb/> |
gRPC LB <https://kubernetes.io/blog/2018/11/07/grpc-load-balancing-on-kubernetes-without-tears/>
CloudNativePG: failover <https://cloudnative-pg.io/docs/1.25/failover> |
backup <https://cloudnative-pg.io/docs/1.25/backup> |
pooling <https://cloudnative-pg.io/docs/1.25/connection_pooling> |
container images <https://cloudnative-pg.io/docs/1.25/container_images> |
image catalog <https://cloudnative-pg.io/docs/1.30/image_catalog> |
imagevolume extensions <https://cloudnative-pg.io/docs/1.30/imagevolume_extensions> |
declarative databases <https://cloudnative-pg.io/docs/1.30/declarative_database_management>
Go/gRPC: Go 1.25 notes <https://go.dev/doc/go1.25> |
SetMemoryLimit <https://pkg.go.dev/runtime/debug#SetMemoryLimit> |
keepalive <https://pkg.go.dev/google.golang.org/grpc/keepalive> |
health <https://pkg.go.dev/google.golang.org/grpc/health> |
health protocol <https://github.com/grpc/grpc/blob/master/doc/health-checking.md> |
pgxpool <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool> |
otelpgx <https://pkg.go.dev/github.com/exaring/otelpgx> |
automaxprocs <https://pkg.go.dev/go.uber.org/automaxprocs>
Other: KEDA Prometheus scaler <https://keda.sh/docs/2.17/scalers/prometheus/> |
prometheus-operator <https://prometheus-operator.dev/docs/developer/getting-started/> |
distroless <https://github.com/GoogleContainerTools/distroless/blob/main/README.md>
