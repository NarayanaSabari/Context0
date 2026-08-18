# Context0 performance and Kubernetes-native audit

Measured 2026-08-18 against a local `docker compose` stack: PostgreSQL 18 +
Apache AGE 1.7.0 + pgvector, 50,053 `Memory` vertices across 50 projects, 201
edges. Every number below came from `EXPLAIN (ANALYZE)` on that instance, not
from estimation. Query shapes are the exact ones `internal/graph/age.go` emits.

Research inputs are in [`k8s-production-readiness-2026.md`](k8s-production-readiness-2026.md)
and were cross-checked against these measurements.

---

## 1. The headline finding: no property is indexed

AGE stores every vertex property inside a single `agtype` column on the label
table `context0."Memory"`. Creating a graph gives you btree indexes on the
internal `graphid` columns (`Memory_pkey`, `_ag_label_edge_start_id_idx`,
`_ag_label_edge_end_id_idx`) and nothing else, so **every filter on `id`,
`project_id`, or `created_at` is a sequential scan of the whole label**.

Measured at 50k vertices, before any change:

| Query | Plan | Time |
|---|---|---|
| `GetMemory` by id | `Seq Scan`, 50,052 rows discarded | 5.5 ms |
| `Query` project filter | `Seq Scan`, 49,053 rows discarded | 17.2 ms |
| `GetSubgraph` 1-hop | `Append` over every label table | 223 ms |
| `getEdgesAround` | `Append` over every label table | 234 ms |

This grows linearly with corpus size forever. `GetMemory` is the one that hurts
most, because `SearchByVector` calls it once per hit.

### Fix: expression indexes

The canonical form is AGE's own regression test
(`regress/sql/index.sql` @ `release/PG17/1.7.0`) - fully qualified, two-argument,
with the key as a quoted agtype string:

```sql
CREATE INDEX memory_id_idx ON context0."Memory"
  (ag_catalog.agtype_access_operator(properties, '"id"'::agtype));
CREATE INDEX memory_project_id_idx ON context0."Memory"
  (ag_catalog.agtype_access_operator(properties, '"project_id"'::agtype));
ANALYZE context0."Memory";
```

`ANALYZE` matters: expression indexes only get statistics after it runs.

Measured after adding them:

| Query | Before | After | Speedup |
|---|---|---|---|
| `GetMemory` by id | 5.54 ms | 0.19 ms | **29x** |
| `Query` project filter | 17.2 ms | 3.56 ms | **4.8x** |

---

## 2. Unlabeled MATCH patterns scan every vertex table

`GetSubgraph` and `getEdgesAround` both matched untyped nodes:

```cypher
MATCH (center {id: $center_id})-[e]-(neighbor)      -- original
```

With no label, AGE must `Append` over `_ag_label_vertex` **and every vertex
label table**, and it cannot use an index that exists only on `"Memory"`.

| Variant | Time |
|---|---|
| both ends unlabeled | 223 ms |
| center labeled only | 205 ms |
| neighbour labeled | 72 ms |

Labeling the *neighbour* is what matters, not the center. The center must in
fact stay unlabeled: callers pass Session ids as well as Memory ids, so
constraining it to `:Memory` silently returns nothing for a session subgraph.
The existing `TestSessionLifecycle` caught exactly that mistake.

Labeling alone still leaves ~72ms, because the pattern is undirected. §6 is the
rest of the story and takes this to 0.086ms.

---

## 3. `WHERE` and map-literal forms hit different indexes

These two are not interchangeable, which is easy to miss:

| Cypher form | Compiles to | Needs |
|---|---|---|
| `WHERE m.id = $id` | `agtype_access_operator(...) = ...` | expression btree |
| `MATCH (m:Memory {id: $id})` | `properties @> '{"id": ...}'` | GIN on `properties` |

Both are indexable, but the btree path is meaningfully faster. Measured through
pgx with real bind parameters, exactly as the server sends them:

| Form | Plan | Time |
|---|---|---|
| `MATCH (m:Memory {id: $id})` | Bitmap Index Scan on GIN | 0.49 ms |
| `WHERE m.id = $id` | Index Scan on btree | **0.10 ms** |

`GetMemory` currently uses the slower map form.

> Note: this had to be measured through Go. AGE rejects a psql literal as the
> third `cypher()` argument (`third argument of cypher function must be a
> parameter`), so `EXPLAIN` in psql cannot reproduce the parameterized plans the
> server actually runs. All four parameterized shapes were confirmed to use
> indexes once they exist.

---

## 4. Keyword search can never be indexed inside Cypher

The `Query` hot path does:

```cypher
WHERE toLower(m.content) CONTAINS $kw0
```

This is unindexable for two independent reasons:

1. `agtype_string_match_contains` is declared in `sql/agtype_string.sql` with
   **no operator and no operator class**. PostgreSQL can only convert a qual into
   an `Index Cond` through an operator bound to an opclass, so there is no
   mechanism for `CONTAINS` to reach an index at all.
2. `toLower(...)` wraps the property, producing an expression that cannot match
   an index on the bare property.

The saving grace today is that `project_id` narrows the set first, so `CONTAINS`
only filters the survivors. That holds as long as no single project is huge.

The real fix is to move substring search out of Cypher, either to `pg_trgm` over
the label table or onto the pgvector side. Both are a larger change than the
rest of this document and are not attempted here.

---

## 5. N+1 patterns, and a counter-intuitive result

Three round-trip amplifiers, all in the `Query` path:

- **`SearchByVector`** calls `GetMemory` once per hit, so top-K costs K+1 queries.
- **`Query`** called `IncrementAccessCount` once per result (~1.4ms each at 50k).
- **`GetProfile`** hardcodes `TopK: 200`.

Batching the increments looked obvious, and the first attempt made the endpoint
**slower** - 18ms to 37ms. The reason turned out to matter far more than the
batching itself:

> **A parameterized `WHERE m.id IN $ids` defeats the property index entirely.**
> AGE plans a sequential scan over the whole label. `UNWIND $ids AS wanted ...
> WHERE m.id = wanted` over the exact same parameter drives an index scan per
> element.

Measured at 50k vertices:

| Form | Plan | Time |
|---|---|---|
| `WHERE m.id IN $ids` (parameterized) | Seq Scan | 30.9 ms |
| `UNWIND $ids ... WHERE m.id = wanted` | Index Scan | 2.3 ms |
| context edges, `IN $ids` | Seq Scan | 12.4 ms |
| context edges, `UNWIND` | Index Scan | **0.098 ms** |

The literal `IN [...]` form *does* use the index, which is why this is easy to
miss when testing in psql - but building literals is exactly the injection hole
this repository removed, so it is not an option.

`m.type IN $types` keeps the IN form deliberately: it is a secondary filter
applied after `project_id` has already driven the index, and it measured 2.0ms
against 1.5ms for project alone.

The vector-search N+1 is left alone. Batching it needs the same UNWIND treatment
plus a rework of how `SearchByVector` hydrates results, which is a larger change
than this pass.

---

## 6. Undirected MATCH cannot use the edge indexes

The same class of problem, in the traversal queries. An undirected `-[e]-`
pattern makes AGE scan the whole label; the two directed halves each drive
`_ag_label_edge_start_id_idx` / `_ag_label_edge_end_id_idx`, and their union is
exactly the undirected set.

| Query | Undirected | Directed pair |
|---|---|---|
| `getEdgesAround` | 71.5 ms | **0.045 ms** |
| `GetSubgraph` neighbours | 209 ms | **0.086 ms** |
| `GetContextEdges` | 19.2 ms | **0.074 ms** |

---

## 7. The liveness probe runs two full graph scans

`charts/context0/templates/api.yaml` points **both** liveness and readiness at
`/v1/health`, and `internal/service/health.go` implements that as:

```cypher
MATCH (n) RETURN count(n)        -- 14.3 ms at 50k
MATCH ()-[e]->() RETURN count(e) --  2.4 ms
```

So every pod runs ~17 ms of full-graph counting every 10 seconds, forever, and
the cost grows with the corpus. Worse, it is wired to **liveness**, which means a
brief Postgres blip fails the probe on every replica simultaneously and
Kubernetes restarts the entire fleet. Restarting a Go process cannot fix a remote
database, so this converts a short database hiccup into a cluster-wide
`CrashLoopBackOff` with a cold connection pool.

Correct split:

- **liveness**: process-local only, no database, no graph counts.
- **readiness**: bounded `pool.Ping` with a ~1s timeout, plus a `draining` flag.
- **startup**: covers `InitSchema`, which runs AGE graph creation before serving.

The node/edge counts belong on `/metrics` or a separate stats endpoint, not on a
probe that runs every 10 seconds per pod.

---

## 8. Two shutdown bugs in `cmd/server/main.go`

```go
grpcServer.GracefulStop()          // (1) gateway still dials this
if err := httpServer.Shutdown(ctx) // (2) ctx is the root context
```

1. **Ordering is backwards.** grpc-gateway serves REST by dialing the local gRPC
   server, so stopping gRPC first breaks in-flight REST requests during the
   drain. HTTP must shut down first.
2. **The drain is unbounded.** `Shutdown` reuses the long-lived root context
   instead of a deadline, so a stuck connection can hang past
   `terminationGracePeriodSeconds` and be SIGKILLed - skipping `repo.Close()`.

Also missing: no `preStop` hook, so the pod can stop accepting connections before
kube-proxy has removed it from Service endpoints, which drops requests on every
rollout.

---

## 9. Configuration left entirely at defaults

| Setting | Current | Note |
|---|---|---|
| `shared_buffers` | 128 MB | default, against a 1 Gi pod limit |
| `work_mem` | 4 MB | default |
| `pgxpool.MaxConns` | unset | defaults to **node** core count, not the pod limit |
| `GOMEMLIMIT` | unset | not auto-derived from `limits.memory` |

`pgxpool.MaxConns` is the dangerous one on Kubernetes: it keys off the machine's
core count rather than the container's CPU limit, so a few replicas on a large
node can exhaust `max_connections` (100).

Note on `GOMAXPROCS`: Go 1.25+ is already cgroup-aware and adjusts dynamically.
Adding `automaxprocs` would actively *disable* that, so it should not be added.
`GOMEMLIMIT` is different and does need wiring from the pod limit.

---

## 10. Results

Measured through the public REST API against the same 50k corpus, comparing a
binary built at `c2b2888` (before any of this work) against `bbd6eee`, both
pointed at the same database:

| Endpoint | Before (`c2b2888`) | After (`bbd6eee`) | Speedup |
|---|---|---|---|
| `GET /v1/memories/query` (project filter) | 135.0 ms | **2.9 ms** | **47x** |
| `GET /v1/memories/query` (keyword) | 139.8 ms | **4.7 ms** | **30x** |
| `GET /v1/profiles/{id}` | 39.4 ms | **3.8 ms** | **10x** |
| liveness probe | 50.4 ms (`/v1/health`) | **0.5 ms** (`/livez`) | **95x**, and no DB |

> An earlier draft of this table reported 62.2 ms as the "before" figure. That
> was measured mid-work, after the property indexes had already landed, so it
> compared two partially-optimized builds rather than before against after. The
> numbers above are a true A/B: two binaries, one database, one corpus.

What shipped:

1. Expression indexes on `id` and `project_id`, created and `ANALYZE`d at
   startup. `create_vlabel` runs first so they exist from the very first boot
   rather than after the first write.
2. Every traversal split into two directed matches.
3. `UNWIND` instead of parameterized `IN` for id lists.
4. Batched `IncrementAccessCounts` in place of a serial loop.
5. Probes split into `/livez`, `/readyz`, `/startupz`; graph counts stay on
   `/v1/health`, which is no longer a probe.
6. Shutdown reordered (HTTP before gRPC), bounded at 15s, with a `preStop` hook
   and `terminationGracePeriodSeconds`.
7. `pgxpool` sized explicitly instead of by node core count.
8. Postgres memory settings, PDB, topology spread, `GOMEMLIMIT`, ServiceMonitor,
   and a non-root read-only-rootfs security context in the chart.

`TestQueryPlansUseIndexes` pins items 1-3: it asserts on the plan, so a future
edit that reintroduces a sequential scan fails the suite instead of quietly
regressing latency.

## 11. Kubernetes verification

The chart claims were checked against a running kind cluster, not against
rendered YAML. `scripts/verify_k8s.sh` maps each requirement to a check that
observes the live cluster, and reports 35/35 on a default `helm install` with no
`--set` overrides:

| Area | What is checked |
|---|---|
| Probes | all three paths wired, each returns 200, none still points at `/v1/health` |
| Security | readOnly rootfs *enforced at runtime*, not just declared; caps dropped; non-root |
| Runtime config | `GOMEMLIMIT` resolved from the pod limit, `pool_max_conns` in the DSN |
| Lifecycle | preStop present, grace period exceeds the 15s drain, `maxUnavailable: 0` |
| Postgres | all four tuned settings served by the running server |
| Performance | property indexes created automatically on first in-cluster boot |
| Public API | store and query round-trip; `/metrics` scrapeable without auth |
| Web | upstream resolves; pod Running rather than CrashLoopBackOff |
| Failure mode | database killed: liveness holds, readiness fails, **no restart**, self-recovers |

That last row is the one that matters most. Killing Postgres and watching the
API pod stay up, drop out of Service endpoints, and rejoin on its own with the
restart count unchanged is direct evidence for the probe split. Under the
previous configuration the same outage would have failed liveness on every
replica simultaneously.

Also verified, because each is a distinct failure mode:

- **Default install path** (`helm install` with no overrides) - the path a new
  user actually takes, and the one I had never run.
- **Conditional resources**: no PDB at one replica (a `minAvailable` PDB there
  blocks node drains), PDB and topology spread appear at two.
- **ServiceMonitor gating**: absent by default, and enabling it on a cluster
  without the Prometheus Operator CRD fails loudly with an actionable message
  rather than silently doing nothing.
- **Zero-downtime rollout**: 13,323 requests through the NodePort Service across
  a full rolling update, zero failures.

> A first rollout attempt reported 9,779 failures. That was `kubectl
> port-forward`, which binds a single pod and dies when it is replaced - a
> property of the test, not the Service. Re-run through the NodePort, which is a
> real Service path, it is clean.

---

## 12. Not attempted

- **Keyword search** is still `toLower(...) CONTAINS` inside Cypher, which can
  never use an index (§4). It survives only because `project_id` narrows the set
  first. Moving it to `pg_trgm` or the vector side is the next real win.
- **The `SearchByVector` N+1** still issues one `GetMemory` per hit.
- **CloudNativePG** remains the right long-term answer for the database, but AGE
  needs `shared_preload_libraries` and a custom operand image, so it is a
  project in itself.
