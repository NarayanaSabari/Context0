# Resilience and Data Durability Research (report only, no code changed)

Scope: Context0 - Go API (gRPC + HTTP) on Kubernetes, hand-rolled single-replica PostgreSQL
StatefulSet with a custom image (Apache AGE + pgvector), consolidation CronJob, pgxpool.

Legend: **[F]** = verified against a cited primary source. **[I]** = inference/judgment by me.

---

## 0. What the repo actually does today (read from source)

- `charts/context0/templates/postgres.yaml`: single-replica StatefulSet, `volumeClaimTemplates` 5Gi
  RWO, args include `-c shared_preload_libraries=age`, probes are `pg_isready`. No archive_mode, no
  backup sidecar, no `ScheduledBackup`, no replica. **[F, repo]**
- `charts/context0/templates/consolidation.yaml`: `concurrencyPolicy: Forbid`,
  `successfulJobsHistoryLimit: 3`, `failedJobsHistoryLimit: 3`, `restartPolicy: OnFailure`.
  No `startingDeadlineSeconds`, no `activeDeadlineSeconds`, no `backoffLimit`. **[F, repo]**
- `internal/service/memory.go:172`: `vectorResults, _ = s.repo.SearchByVector(...)` - the vector leg
  of hybrid retrieval **already degrades silently** to graph-only on error. Section 5 evaluates
  whether that is right. **[F, repo]**
- `internal/graph/age.go:42-74`: MaxConns 10 / MinConns 2, set explicitly because pgxpool defaults
  MaxConns to `max(4, runtime.NumCPU())` of the *node*. That default is confirmed by upstream docs:
  "MaxConns is the maximum size of the pool. The default is the greater of 4 or runtime.NumCPU()."
  <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool> **[F]**
- `values.yaml` already documents the per-process rate limiter caveat. **[F, repo]**

---

## 1. CloudNativePG with a custom AGE + pgvector image

### Can CNPG run a custom operand image? Yes.

CNPG's image contract is deliberately small. Verbatim from the docs, the operator "is designed to
work with any compatible PostgreSQL container image that meets the following requirements":

- `initdb`, `postgres`, `pg_ctl`, `pg_controldata`, `pg_basebackup` on `PATH`
- proper locale settings
- optional: PGAudit (only if audit logging), `du` (only for `kubectl cnpg status`)
- "No entry point or command is required in the image definition. CloudNativePG automatically
  overrides it with its instance manager."
- only PGDG-supported PostgreSQL major versions
- only the "Primary with multiple/optional Hot Standby Servers" architecture is supported

<https://cloudnative-pg.io/docs/devel/container_images> **[F]**

Notably, Barman Cloud binaries are **no longer required in the image** - backup moved to the
CNPG-I Barman Cloud Plugin. Same page. **[F]** This matters: an older blog post telling you to bake
`barman-cloud-wal-archive` into your custom image is now stale.

**Image tag rule.** The operator must determine the PostgreSQL major version. Either set
`imageCatalogRef.major`, or make the tag *begin* with a valid major version (`16`, `16.4`,
`16.4-age1.5`). `latest` is explicitly not a valid tag. **[F, same page]**

Practical consequence for Context0: your image is currently `context0/postgres-age-vector:dev`.
`dev` is not a parseable tag, so under CNPG you would have to retag to something like
`16.4-age1.5.0-pgvector0.8.0`, or declare an `ImageCatalog` entry with `major: 16`
(<https://cloudnative-pg.io/docs/devel/image_catalog>). **[F for the rule, I for the naming]**

Best practice is to build **FROM the CNPG community operand image** rather than `postgres:16`, i.e.
`FROM ghcr.io/cloudnative-pg/postgresql:16.x-standard-trixie` and then add AGE + pgvector. That
inherits the locale setup, the PGDG packaging layout, and the extension paths the operator expects.
**[I, but grounded in the requirement list above; the community images are stated to be "fully
compatible"]**

### shared_preload_libraries for AGE

CNPG defaults `shared_preload_libraries = ''` (it is in the documented global default parameter
block) and exposes `.spec.postgresql.shared_preload_libraries` as a **list of strings**, which the
operator "will merge with the ones that it automatically manages". Managed ones are `auto_explain`,
`pg_stat_statements`, `pgaudit`, `pg_failover_slots` - AGE is not managed, so it must be declared
explicitly. <https://cloudnative-pg.io/docs/devel/postgresql_conf> **[F]**

Critical warning from the same page, which applies directly to you: "In case a specified library is
not found, the server fails to start, **preventing CloudNativePG from any self-healing attempt and
requiring manual intervention**." So an image/`shared_preload_libraries` mismatch turns an
auto-healing cluster into a manual incident. Test the image before rollout. **[F]**

Sketch (not applied):

```yaml
spec:
  imageName: ghcr.io/you/postgres-age-vector:16.4-age1.5
  postgresql:
    shared_preload_libraries:
      - age
    parameters:
      shared_buffers: "256MB"
      # wal_level: replica  # CNPG defaults to logical; replica is cheaper if you don't need logical
```

CNPG defaults `wal_level=logical` (upstream default is `replica`) and recommends dropping to
`replica` if logical replication is not needed, to reduce WAL volume. **[F, postgresql_conf]** For a
memory engine with write-heavy consolidation, that is a real saving. **[I]**

Also note CNPG **fixes** `archive_command = '/controller/manager wal-archive %p'`, `ssl=on`,
`restart_after_crash=false`, `listen_addresses`, `port` - you cannot override them. **[F]** Your
current chart's `args: -c ...` style of tuning goes away entirely; everything moves to
`spec.postgresql.parameters`. **[I]**

### What you get

| Capability | Mechanism | Source |
|---|---|---|
| Automated failover / self-healing | operator-managed primary + hot standbys; only that topology is supported | container_images **[F]** |
| PITR to object storage | WAL archive + physical base backup; "CloudNativePG provides out-of-the-box an **RPO ≤ 5 minutes**" with a WAL archive configured | <https://cloudnative-pg.io/docs/devel/backup> **[F]** |
| Scheduled backups | `ScheduledBackup` CRD, 6-field cron *including seconds* (differs from K8s CronJob) | backup **[F]** |
| Volume-snapshot backups | native, requires CSI support; supports cold + incremental, but **no retention policies** | backup **[F]** |
| Rolling minor upgrades | image change / `ImageCatalog` entry update rolls all associated clusters | image_catalog **[F]** |
| PgBouncer pooling | `Pooler` CRD, needs PgBouncer **≥ 1.19** (`auth_dbname`); creates `cnpg_pooler_pgbouncer` role + `user_search` SECURITY DEFINER function automatically | <https://cloudnative-pg.io/docs/devel/connection_pooling> **[F]** |

Two lifecycle gotchas worth writing into your docs: `Pooler.spec.cluster` is **immutable**, and
poolers are **not** garbage-collected with the cluster. **[F, connection_pooling]**

### Migration path (concrete)

1. Rebuild the image `FROM ghcr.io/cloudnative-pg/postgresql:<major>-standard-*`, adding AGE +
   pgvector; tag it major-version-first.
2. Verify locally: `postgres -c shared_preload_libraries=age` starts, `CREATE EXTENSION age;` and
   `CREATE EXTENSION vector;` succeed, and `initdb/pg_ctl/pg_controldata/pg_basebackup` are on PATH.
3. Install the operator and (optionally) the Barman Cloud Plugin; declare an `ObjectStore`.
4. Create the `Cluster` with `bootstrap.initdb` and run your migrations, then move data with
   `pg_dump | psql` (small DB) - or use CNPG's `import` / `pg_basebackup` bootstrap for larger ones.
   Note CNPG explicitly says `pg_dump` is "**not suitable for business continuity**" as an ongoing
   strategy, but as a one-shot cutover it is fine. **[F for the quote, I for the cutover advice]**
5. Point `CONTEXT0_DATABASE_URL` at `<cluster>-rw` (or the `Pooler` service).
6. Add `ScheduledBackup` + test a PITR restore into a throwaway cluster before declaring done.

### Alternatives

- **Zalando postgres-operator** - mature, Patroni-based, widely deployed. Weaker native
  object-storage PITR ergonomics compared to CNPG's plugin model; the project has been in low-
  maintenance mode relative to CNPG's release cadence. **[I]**
- **CrunchyData PGO** - excellent, pgBackRest-native (full/diff/incr, S3/Azure/GCS, encryption,
  ransomware protection via versioned buckets - all documented at <https://pgbackrest.org/>
  **[F for pgBackRest features]**). Cost: Crunchy's own images/licensing story is less friendly for
  a permissively-licensed OSS project telling users to self-host. **[I]**
- **StackGres** - very batteries-included (Envoy, connection pooling, dashboards), but the most
  opinionated and heaviest to embed in someone else's cluster. **[I]**

**Pick CNPG.** Reasons: CNCF-hosted with a fast release cadence, a documented "any compatible image"
contract that is unusually friendly to custom extension builds like AGE, first-class
`shared_preload_libraries` merging, and PgBouncer via a CRD you do not have to operate.
**[I, grounded in the cited docs]**

**But**: for an OSS chart, make it *optional*. Ship `postgres.mode: statefulset | cnpg | external`
and default to `external` guidance for production. Do not force every self-hoster to install a
cluster-scoped operator + CRDs to run a memory engine. **[I]**

---

## 2. Backups without an operator

### Does AGE data back up with standard tooling? Confirmed.

AGE is "a PostgreSQL extension that provides graph database functionality... The goal of the project
is to create **single storage** that can handle both relational and graph model data so that users
can use standard ANSI SQL along with openCypher."
<https://age.apache.org/age-manual/master/intro/overview.html> **[F]**

Graph data lives in ordinary heap tables inside a per-graph schema (labels become tables), so:

- **Physical** backup (base backup + WAL, pgBackRest, volume snapshot) captures it trivially - it is
  just files in `PGDATA`. **Confirmed. [F by construction]**
- **Logical** `pg_dump` also works, with one real caveat: the dump must restore into a cluster where
  `age` is in `shared_preload_libraries` **before** restore, and the `ag_catalog` schema /
  `search_path` handling must be right. `pg_dump` restore of extension-owned catalog rows is the
  classic failure mode here. **[I - verify with an actual dump/restore test before trusting it]**
- pgvector columns are a normal type; `pg_dump` handles them, and HNSW indexes are rebuilt on
  restore (slow for large tables). **[I]**

### Options ranked

| Option | RPO | RTO | Honest assessment |
|---|---|---|---|
| **Nothing (today)** | ∞ - total loss | ∞ | Current state. |
| **`pg_dump` CronJob to S3, daily** | up to 24h of lost memories | minutes-to-hours (restore + reindex HNSW) | Minimum credible. ~40 lines of chart. PG docs are explicit that dumps are *logical* and "cannot be used as part of a continuous-archiving solution" <https://www.postgresql.org/docs/current/continuous-archiving.html> **[F]** |
| **WAL archiving + periodic base backup, hand-rolled** | seconds-to-minutes (one WAL segment, bounded by `archive_timeout`) | hours; you own the restore runbook | PG supports it natively via `wal_level=replica`, `archive_mode=on`, `archive_command` **[F, same page]**. Hand-rolling `archive_command` is where people lose data: PG assumes success on exit 0 and **recycles the segment** - a silently-failing archive command destroys your recovery chain **[F, same page]**. Also: "archive commands should generally be designed to refuse to overwrite any pre-existing archive file" **[F]**. |
| **pgBackRest sidecar/host** | seconds-to-minutes | best of the three: parallel + delta restore | Full/diff/incr at file *and block* level, checksums verified on restore, S3/Azure/GCS repos, encryption, and point-in-time reads of versioned buckets for ransomware recovery <https://pgbackrest.org/> **[F]**. It solves precisely the correctness traps of hand-rolled WAL archiving. |

**Recommendation for a self-hosted OSS chart:** ship the `pg_dump`-to-object-store CronJob as the
default (`backup.enabled`, off by default but loudly documented), and document pgBackRest or CNPG as
the production answer. A dump CronJob that people actually enable beats a PITR design nobody
installs. **[I]**

Two rules to write into the docs: (a) an untested backup is not a backup - the runbook must include a
restore drill; (b) if you archive WAL by hand, monitor `pg_stat_archiver` for `last_failed_wal`,
because PG will keep retrying but your RPO silently degrades meanwhile. **[F that the view exists and
that failures are retried; I for the monitoring advice]**

---

## 3. HPA for a latency-sensitive API

### The mechanics you must respect

HPA's core ratio: `desiredReplicas = ceil(currentReplicas * currentMetric / desiredMetric)`, skipped
when within a tolerance (0.1 default). The control loop runs every 15s by default
(`--horizontal-pod-autoscaler-sync-period`).
<https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/> **[F]**

Stabilization: "right before HPA scales the target, the scale recommendation is recorded. The
controller considers all recommendations within a configurable window choosing the **highest**
recommendation from within that window... `--horizontal-pod-autoscaler-downscale-stabilization`,
which defaults to **5 minutes**." **[F, same page]** In `autoscaling/v2` you set this per-HPA via
`behavior.scaleDown.stabilizationWindowSeconds` and shape the rate with `policies`. **[F]**

Startup traps that bite latency-sensitive services: CPU metrics from not-yet-ready pods are set
aside; `--horizontal-pod-autoscaler-cpu-initialization-period` defaults to **5 minutes** and
`--horizontal-pod-autoscaler-initial-readiness-delay` to **30 seconds**, both cluster-wide only. The
docs' own guidance is to gate readiness/startup probes so the warm-up CPU spike does not feed the
autoscaler. **[F, same page]** Context0's `/startupz` probe already gives you that hook. **[I]**

### Which metric

- **CPU**: correct-by-default only if latency correlates with CPU. Context0's hot path is dominated by
  Postgres round-trips and pgxpool waits, so a saturated replica can sit at modest CPU while p99
  climbs. CPU HPA will under-scale. **[I]**
- **The metric that actually matches the bottleneck: pool saturation / in-flight requests.**
  pgxpool exposes exactly this via `Stat()`: `AcquiredConns`, `EmptyAcquireCount`,
  `EmptyAcquireWaitTime`, `CanceledAcquireCount`
  <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool> **[F]**. Scaling on
  `AcquiredConns / MaxConns` (or concurrent in-flight RPCs per pod) tracks the real constraint. **[I]**
  Caveat: scaling out multiplies `MaxConns * replicas` against Postgres `max_connections` - so this
  metric must be paired with a pooler, or you scale yourself into connection exhaustion. **[I]**
- **Prometheus Adapter vs KEDA**: both end at the same place. KEDA "acts to monitor the event source
  and feed that data to Kubernetes and the HPA... to drive rapid scale"
  <https://keda.sh/docs/2.17/concepts/scaling-deployments/> **[F]** - i.e. KEDA *generates* an HPA,
  so all the stabilization semantics above still apply. KEDA is easier to configure (`ScaledObject`,
  one trigger block, `useCachedMetrics` to spare the scaler), supports scale-to-zero via its
  activation phase, and has a first-class `prometheus` trigger. **[F]** Prometheus Adapter is a
  thinner dependency but its rule config is notoriously fiddly. **[I]** For an OSS chart: **document
  a CPU HPA as the safe default, and a KEDA `ScaledObject` on pool saturation as the opt-in.** **[I]**

### The gRPC connection-imbalance problem

This is the real hazard and it is independent of the HPA. gRPC runs over HTTP/2 and "multiplexes many
requests on one connection". A Kubernetes `ClusterIP` Service is L4: it balances *connections*, not
*requests*. So a long-lived client connection pins all of its RPCs to one pod, and newly scaled-up
pods receive **zero** traffic - the HPA adds replicas and p99 does not move.
<https://grpc.io/blog/grpc-load-balancing/> **[F for the protocol/L4-vs-L7 framing]**

The blog's own guidance: "RPC load varies a lot among connections → use Application level LB", and
"L3/L4 LB by design does very little processing" - it just copies frames. **[F]**

Remedies, in increasing order of cost:

1. **Server-side `keepalive.ServerParameters.MaxConnectionAge`** - "a duration for the maximum amount
   of time a connection may exist before it will be closed by sending a GoAway. **A random jitter of
   +/-10% will be added to MaxConnectionAge to spread out connection storms.**" Pair with
   `MaxConnectionAgeGrace`, "an additive period after MaxConnectionAge after which the connection
   will be forcibly closed". <https://pkg.go.dev/google.golang.org/grpc/keepalive> **[F]** This is a
   ~5-line server change that forces periodic rebalancing and it is the single highest
   value/effort item in this section. Suggested: `MaxConnectionAge: 30m`,
   `MaxConnectionAgeGrace: 30s`. **[I for the values]**
2. **Client-side round-robin over a headless Service** - resolves all pod IPs, client balances per
   RPC. Requires cooperative clients; your Go SDK can do it, arbitrary users' clients may not. **[I]**
3. **L7 proxy / mesh** (Envoy, Istio, Linkerd, or an ingress with HTTP/2 backend support) - proper
   per-request balancing. The blog recommends exactly this for the "N clients, M servers" case.
   **[F]** Heaviest dependency. **[I]**

Also set `keepalive.EnforcementPolicy` deliberately: the server default `MinTime` is 5 minutes and a
client pinging faster gets GOAWAY'd. **[F, same page]** Misconfigured client keepalive vs server
enforcement is a classic self-inflicted disconnect loop. **[I]**

---

## 4. Retries, timeouts, circuit breaking

### The split

- **Application owns**: per-request deadlines/`context` propagation, idempotency, retry-safety
  classification, pool sizing, and *what a partial failure means semantically*.
- **Platform owns**: connection-level retry/eject (mesh outlier detection), rate limiting at the
  edge, TLS, and load balancing.

Retries on a *database* call are one of the few things the platform genuinely cannot do for you,
because only the app knows whether the statement was idempotent and whether it had already been sent
on the wire. **[I]**

### pgx specifics - the one API that matters

pgconn exposes `func SafeToRetry(err error) bool` and a `NotPreferredError` with a `SafeToRetry()`
method. <https://pkg.go.dev/github.com/jackc/pgx/v5/pgconn> **[F]** This is the correct predicate: it
tells you the query never reached the server, so re-executing cannot double-apply. Retrying anything
`SafeToRetry` returns false for risks duplicate writes. **[I]**

The pool already handles much of the failover case for you, and this is the important nuance:

- `Config.ShouldPing` - "If it returns true, the connection is pinged to check for liveness. If this
  func is not set, **the default behavior is to ping connections that have been idle for at least 1
  second.**" **[F, pgxpool]** So a stale connection left over from a failover is normally detected at
  acquire time, not at query time.
- `Config.PrepareConn` - and specifically: "**If it returns false and a nil error, the connection
  will be destroyed, and the instigating query will be retried on a new connection.**" **[F,
  pgxpool]** That is a built-in, correct, single transparent retry hook.
- `Pool.Reset()` exists to close all current connections - the right sledgehammer after a detected
  failover. **[F]**
- `MaxConnLifetime` + `MaxConnLifetimeJitter` ("helps prevent all connections from being closed at
  the exact same time, starving the pool") **[F]** - Context0 sets neither today. Setting
  `MaxConnLifetime: 30m, MaxConnLifetimeJitter: 5m` means a failover's stale connections age out
  instead of lingering. **[F for the fields, I for the values]**
- `PingTimeout` - "If zero, the default is **no timeout**." **[F]** Leaving this at zero means a
  liveness ping to a dead-but-not-closed primary can hang for the TCP timeout. Set it (e.g. 2s).
  **[I]**

Also relevant during failover: CNPG sets `tcp_user_timeout=5000` on standby connections by default
and explains it "defines how long transmitted data may remain unacknowledged before the TCP
connection is forcibly closed"
<https://cloudnative-pg.io/docs/devel/postgresql_conf> **[F]**. The same parameter is worth setting
on the *client* connection string so the API notices a vanished primary in seconds rather than
minutes. **[I]**

### Circuit breaker: probably not

`sony/gobreaker` and `failsafe-go` are the idiomatic Go options and both are fine libraries. **[I]**
But for a single-datastore service, a breaker mostly re-implements what you get free from:

1. **bounded contexts** - every request already carries a deadline, so a stalled DB cannot pile up
   goroutines indefinitely;
2. **a bounded pool** - `MaxConns=10` *is* a bulkhead: the 11th concurrent request blocks in
   `Acquire` and then fails on context deadline rather than hammering Postgres;
3. **readiness** - `/readyz` failing pulls the pod from Service endpoints, which is the
   platform-level equivalent of opening the circuit.

The failure mode a breaker uniquely prevents - a thundering herd re-dialing a downed primary - is
better handled here by (a) `Acquire` already queueing, and (b) `/readyz` shedding. **Recommendation:
skip the breaker; fix timeouts, `PingTimeout`, `MaxConnLifetime`, add one `SafeToRetry`-gated retry
with jittered backoff, and make `/readyz` reflect pool health.** **[I]**

One caution on `/readyz` reflecting DB health: if *all* replicas mark themselves unready during a DB
blip, you get a total outage instead of degraded service, and you also lose the ability to serve any
cache-only path. Prefer `/livez` never touching the DB, and `/readyz` touching it with a short
timeout and some hysteresis. **[I]**

---

## 5. Graceful degradation in the hybrid retrieval path

**Current behavior is already degrade-not-fail** (`vectorResults, _ = ...`, `memory.go:172`), and
that is the right *decision* with the wrong *ergonomics*. **[I]**

General principle for a composite read path: classify each leg as **essential** or **enriching**.

- Failing an essential leg must fail the request (a wrong answer is worse than no answer).
- Failing an enriching leg should degrade, but the degradation must be **explicit, observable, and
  bounded**.

For Context0: graph traversal is the source of truth for what memories exist and their relations;
vector similarity is a *ranking and recall enhancer*. So graph = essential, vector = enriching.
Vector-only with graph down would return memories with no relational context - arguably wrong.
Graph-only with vector down returns fewer semantically-similar hits but nothing incorrect.
**Degrade to graph-only. [I]**

What is missing today, in priority order:

1. **The error is discarded entirely** (`_`). It should be logged and counted
   (`context0_query_degraded_total{leg="vector"}`), so an operator whose embedder has been broken for
   three weeks finds out. **[I]**
2. **The response should say so.** A `degraded` flag / `partial_results` field in the query response
   lets a caller decide whether to trust recall. Silent degradation is how "search quality slowly got
   worse" bugs are born. **[I]**
3. **Bound the enriching leg with its own, shorter timeout** so a slow vector search cannot consume
   the whole request deadline and starve the essential leg. Run the two legs concurrently with
   `errgroup`, giving vector a sub-deadline. **[I]**
4. **Never degrade silently on writes.** Degradation is a read-path concept; a failed embedding write
   during `Remember` should either fail the call or be durably queued for backfill, not dropped.
   **[I]**

---

## 6. CronJob correctness for consolidation

Current: `Forbid` + history 3/3, no starting deadline, no active deadline. **[F, repo]**

- **`concurrencyPolicy: Forbid` is already correct.** "The CronJob does not allow concurrent runs; if
  it is time for a new Job run and the previous Job run hasn't finished yet, the CronJob skips the
  new Job run." <https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/> **[F]**
  `Replace` would kill a consolidation mid-merge; `Allow` would let two runs decay/prune the same
  graph concurrently. Both are wrong here. **[I]**

- **`startingDeadlineSeconds` is the sharp edge.** The docs are explicit about the interaction:
  "if `startingDeadlineSeconds` is set (not nil), the controller counts how many missed Jobs occurred
  from the value of `startingDeadlineSeconds` until now rather than from the last scheduled time
  until now". And with it **unset**, if more than 100 schedules are missed the controller gives up
  entirely and logs `too many missed start times. Set or decrease .spec.startingDeadlineSeconds or
  check clock skew`. **[F]** For a 6-hourly job, 100 misses is 25 days of controller downtime, so the
  unset case is not an urgent hazard - but setting it is still correct, because a consolidation that
  starts 5 hours late is nearly worthless and will collide with the next window. **[I]**
  Also: "If `startingDeadlineSeconds` is set to a value less than 10 seconds, the CronJob may not be
  scheduled. This is because the CronJob controller checks things every 10 seconds." **[F]**
  **Recommend `startingDeadlineSeconds: 600`.** **[I]**

- **Overlap risk with `Forbid` is a *silent skip*, not a collision.** The docs note that with
  `Forbid`, "long-running Jobs may cause scheduled times to be skipped" and a skipped run "would
  count as missed". **[F]** So the real failure mode is: consolidation gets slower as the graph grows,
  quietly starts taking >6h, and then simply stops running - with no alert. **Mitigations:**
  set `activeDeadlineSeconds` (~4h, well under the 6h period) so a runaway run is killed rather than
  blocking the next, set `backoffLimit` (default 6 retries is too many for a whole-graph job), emit a
  `last_successful_consolidation_timestamp` metric, and alert on its age. **[I]**

- **`failedJobsHistoryLimit: 3`** - fine; the default is 1, and 3 gives you three failures' logs to
  post-mortem. **[F that default is 1, I that 3 is right]**

- **`restartPolicy: OnFailure`** restarts the container in place; combined with an unbounded
  `backoffLimit` this can loop. Pair with the explicit `backoffLimit`. **[I]**

- **Idempotency is mandatory, not optional.** "A CronJob creates a Job object approximately once per
  execution time of its schedule. The scheduling is approximate because there are certain
  circumstances where two Jobs might be created, or no Job might be created... Therefore, the Jobs
  that you define should be **idempotent**." **[F]** Two overlapping decay passes must not
  double-decay. **[I - worth auditing `internal/service/consolidate.go` against this.]**

- **`timeZone`** is stable since 1.27 and schedules otherwise follow the kube-controller-manager's
  local zone. **[F]** For a chart shipped to strangers, set `timeZone: "Etc/UTC"` so behavior is
  reproducible. **[I]**

Suggested (not applied):

```yaml
spec:
  schedule: "0 */6 * * *"
  timeZone: "Etc/UTC"
  concurrencyPolicy: Forbid
  startingDeadlineSeconds: 600
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 2
      activeDeadlineSeconds: 14400
```

---

## 7. Multi-replica rate limiting

The problem is stated correctly in `values.yaml` already: N replicas × per-process token bucket = N ×
the intended limit. **[F, repo]**

Standard fixes, in order of how much of someone else's infrastructure they assume:

1. **Push it to the edge.** Ingress-NGINX (`limit-rps` / `limit-connections` annotations), Traefik's
   `RateLimit` middleware, Envoy's local or global rate limit filter, or Istio. This is where rate
   limiting *belongs*: it is a policy about untrusted callers, applied before the request costs you
   anything. **[I]**
2. **Redis-backed distributed bucket** (`go-redis/redis_rate`, or a Lua GCRA script). Correct and
   exact, but adds a mandatory stateful dependency to a project whose current selling point is
   "Postgres and a binary". It also becomes a new SPOF and a new latency term on the hot path unless
   you fail *open*. **[I]**
3. **Divide the limit by replica count.** Trivially wrong under uneven L4 gRPC connection balancing
   (section 3): with connection pinning, one pod may receive most traffic and will throttle at
   `limit/N`. Do not do this. **[I]**

**Is it worth it for an OSS self-hosted project? No - not as a Redis dependency.** **[I]** The honest
framing: this in-process limiter is a *self-protection* mechanism (stop one bad client saturating one
replica's pool), not a *billing/quota* mechanism. For self-protection, per-process is actually the
semantically correct scope - it protects the process it lives in. The right moves are:

- Rename/document it as per-replica self-protection, not a global quota (partially done already).
- Make the limit configurable and default it conservatively.
- Document the ingress/mesh recipe for anyone who needs a true global budget.
- Leave a clean interface (`Limiter`) so a Redis implementation can be dropped in by users who want
  it, without the chart depending on Redis.

---

## 8. Disaster scenarios

### What happens today if the PVC is lost

**Total, unrecoverable loss of all memories.** Concretely: **[I, from reading the chart - all of it
follows from the absence of any backup]**

- The StatefulSet's `volumeClaimTemplates` PVC is the only copy of the data. There is no replica, no
  WAL archive, no dump.
- Kubernetes will happily reschedule the pod and provision a **fresh empty PVC** if the old PVC is
  deleted - the pod goes `Running`, `pg_isready` passes, all three probes go green, and the API
  reports healthy while serving an empty graph. **This is the worst property of the current setup:
  the failure is silent and looks like success.**
- The API pods reconnect, migrations re-run on an empty DB, and clients see zero recall with no error.
- Related exposures: `reclaimPolicy: Delete` on many default StorageClasses means deleting the PVC
  destroys the underlying volume; `helm uninstall` does not delete `volumeClaimTemplates` PVCs (small
  mercy), but a namespace delete does.

Other single points of failure today: node loss with an RWO volume on local/topology-bound storage
strands the pod until the node returns; single replica means every Postgres restart, minor upgrade,
or OOM is a full outage; `sslmode=disable` in the consolidation job's connection string and a
password templated directly into the CronJob's env from `values.yaml` (not the Secret) are
adjacent hygiene issues worth noting. **[F, repo - see consolidation.yaml env block]**

### Minimum viable DR story to document

Four things, and it must be written down in `docs/`, not just implemented:

1. **A backup exists and is off-cluster** - daily `pg_dump --format=custom` to object storage,
   retained 7-30 days. State the RPO plainly: "up to 24 hours of memories may be lost".
2. **A tested restore runbook** - exact commands, including that `shared_preload_libraries=age` must
   be set on the target *before* restore, and that HNSW index rebuild time scales with row count.
   Include the expected RTO measured on a real dataset, not a guess.
3. **A restore drill cadence** - quarterly, into a scratch namespace, with the result recorded. An
   untested restore is a hypothesis.
4. **Monitoring that would have caught it** - alert on backup age (`time since last successful
   backup > 26h`), on `pg_stat_archiver.last_failed_wal` if you archive, and on a sudden collapse in
   row counts / query result counts, which is the signal that would have caught the silent-empty-PVC
   case above.

Plus two cheap hardening items: use a StorageClass with `reclaimPolicy: Retain` for the Postgres PVC,
and add a startup assertion that fails loudly if the database is unexpectedly empty when it should not
be (e.g. a persisted marker row), converting the silent failure into a crash-loop. **[I]**

---

## Priority: top 5 resilience changes

Weighted for an OSS project where an operator dependency is a real adoption cost.

### 1. Backups. Any backups. (`pg_dump` CronJob → object storage)

Nothing else on this list matters if the data can vanish. This is ~40 lines of chart, zero new
runtime dependencies, and it takes RPO from ∞ to 24h. Ship it as `backup.enabled` with a big README
section, plus the restore runbook and a `reclaimPolicy: Retain` recommendation. Everything in §8
collapses to "we lose a day, not everything".

### 2. pgx connection resilience + real timeouts (app-side, no infra)

`MaxConnLifetime` + `MaxConnLifetimeJitter`, a non-zero `PingTimeout`, `tcp_user_timeout` in the
connection string, one `pgconn.SafeToRetry`-gated retry with jittered backoff, and per-request
deadlines enforced end to end. Small, self-contained, no dependency, and it converts "DB blipped →
errors to callers" into "DB blipped → a few slow requests". Explicitly **not** a circuit breaker
(§4). Pair with `PrepareConn` returning `(false, nil)` to get pgxpool's built-in transparent retry on
a dead connection.

### 3. gRPC `MaxConnectionAge` + observable degradation

Two independent but tiny changes. `keepalive.ServerParameters{MaxConnectionAge: 30m,
MaxConnectionAgeGrace: 30s}` makes multi-replica scaling actually work (§3) - without it, adding
replicas or an HPA is theater. And in the query path (§5), replace the discarded error with a logged,
counted, response-flagged degradation plus a sub-deadline for the vector leg. Both are single-digit
line counts with outsized effect on operability.

### 4. CronJob hardening + a consolidation freshness metric

`startingDeadlineSeconds: 600`, `activeDeadlineSeconds: 14400`, `backoffLimit: 2`,
`timeZone: "Etc/UTC"`, an audit of `consolidate.go` for idempotency, and a
`last_successful_consolidation_timestamp` gauge with an alert on its age. This closes the silent
"consolidation quietly stopped running six weeks ago" failure, which is exactly the kind of bug a
memory engine cannot afford. Config-only, no code risk.

### 5. Optional CNPG path in the chart (`postgres.mode: statefulset | cnpg | external`)

Last, deliberately. CNPG genuinely solves failover, PITR at RPO ≤ 5min, rolling minor upgrades, and
PgBouncer - and its "any compatible image" contract makes the AGE custom image a non-blocker. But
forcing cluster-scoped CRDs and an operator on every self-hoster is a real adoption tax, and items
1-4 deliver most of the risk reduction at a fraction of that cost. Make it opt-in, publish the
correctly-tagged AGE + pgvector image built `FROM` the CNPG operand base, and document the migration.
Simultaneously document `external` (managed Postgres) as the low-effort production answer, since a
meaningful share of self-hosters already have an RDS/CloudSQL instance.

---

## Sources

**CloudNativePG**
- Container Image Requirements - <https://cloudnative-pg.io/docs/devel/container_images>
- PostgreSQL Configuration (`shared_preload_libraries`, fixed params, `wal_level`, `tcp_user_timeout`) - <https://cloudnative-pg.io/docs/devel/postgresql_conf>
- Image Catalog - <https://cloudnative-pg.io/docs/devel/image_catalog>
- Backup (RPO ≤ 5 min, object store vs volume snapshots, `ScheduledBackup`) - <https://cloudnative-pg.io/docs/devel/backup>
- Recovery / PITR - <https://cloudnative-pg.io/docs/devel/recovery>
- Connection Pooling (`Pooler`, PgBouncer ≥ 1.19) - <https://cloudnative-pg.io/docs/devel/connection_pooling>

**Kubernetes**
- Horizontal Pod Autoscaling (algorithm, stabilization, readiness periods) - <https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/>
- CronJob (concurrencyPolicy, startingDeadlineSeconds, history limits, idempotency, timeZone) - <https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/>

**PostgreSQL / backup**
- Continuous Archiving and PITR - <https://www.postgresql.org/docs/current/continuous-archiving.html>
- pgBackRest - <https://pgbackrest.org/>
- Apache AGE overview (single storage, standard SQL) - <https://age.apache.org/age-manual/master/intro/overview.html>

**Go**
- pgxpool (`MaxConns` default, `ShouldPing`, `PrepareConn`, `MaxConnLifetimeJitter`, `PingTimeout`, `Stat`) - <https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool>
- pgconn (`SafeToRetry`, `NotPreferredError`) - <https://pkg.go.dev/github.com/jackc/pgx/v5/pgconn>
- grpc/keepalive (`MaxConnectionAge` + jitter, `MaxConnectionAgeGrace`, `EnforcementPolicy.MinTime`) - <https://pkg.go.dev/google.golang.org/grpc/keepalive>
- gRPC Load Balancing (L4 vs L7, connection pinning) - <https://grpc.io/blog/grpc-load-balancing/>

**KEDA**
- Scaling Deployments (KEDA feeds the HPA; activation vs scaling phases; cached metrics) - <https://keda.sh/docs/2.17/concepts/scaling-deployments/>
