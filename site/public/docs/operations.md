# Operations

Running kora day to day: what it exposes, what maintains it, and what to watch.

## Consolidation

A memory graph that only grows eventually degrades: duplicate facts clutter results, stale memories compete with live ones, and abandoned rows accumulate forever. Consolidation is the CronJob that maintains it, running every 6 hours by default.

It has three phases, in order.

### Merge

Groups semantic memories by `(project_id, content)`. Where a group has more than one member, the most recently created memory is kept and a `SUPERSEDES` edge is drawn from it to each older duplicate.

Note that the duplicates are not deleted, they are superseded, which is the same policy the API follows: history stays queryable, and the current answer is unambiguous.

> This phase currently matches content **exactly**. Near-duplicates that differ by a word are not detected, and merging them will need similarity scoring.

### Decay

Recalculates `decay_score` for every memory:

```
decay = exp(-0.693 * hoursSinceCreation / halfLifeHours) * (1 + frequencyBoost)
frequencyBoost = min(1.0, ln(1 + accessCount) / 5.0)
```

Exponential decay against the configured half-life, 30 days by default, with a boost for memories that get retrieved often. A frequently accessed memory decays more slowly, which is the intended behaviour: retrieval is evidence of relevance. The result is clamped to `[0, 1]`.

### Prune

Deletes a memory only when **all three** hold at once:

- `decay_score` is below `staleThreshold` (default `0.1`)
- `access_count` is exactly zero, meaning it was never once retrieved
- age exceeds `pruneAgeDays` (default `30`)

Deliberately conservative. A memory that was ever useful, or that is merely old, survives. Only genuinely abandoned rows are removed.

### Configuration

```yaml
consolidation:
  enabled: true
  schedule: "0 */6 * * *"
  decayHalfLifeDays: "30"
  staleThreshold: "0.1"
  pruneAgeDays: "30"
```

## Metrics

Prometheus metrics are on `/metrics`, on the HTTP port, requiring no API key. Names are prefixed `kora_`:

| Metric | Type | What it tells you |
| --- | --- | --- |
| `kora_memories_total` | counter | Memories created |
| `kora_edges_total` | counter | Edges created |
| `kora_requests_total` | counter | Requests, by outcome |
| `kora_query_duration_seconds` | histogram | Query latency |
| `kora_store_duration_seconds` | histogram | Store latency |
| `kora_active_sessions` | gauge | Sessions currently open |
| `kora_pool_connections` | gauge | Connection pool state |
| `kora_pool_acquire_wait_seconds_total` | counter | Time spent waiting for a connection |

The histogram buckets are custom rather than `prometheus.DefBuckets`, which starts at 5ms and jumps 0.1 to 0.25 to 0.5 to 1s. A store costs about 4ms here, so the default buckets put nearly every observation in the first one and make percentiles meaningless.

`kora_pool_acquire_wait_seconds_total` is the one to alert on. Rising wait time means the pool is saturated, and it moves before latency does.

To scrape with the Prometheus Operator, set `metrics.serviceMonitor.enabled=true` and `metrics.serviceMonitor.labels` to whatever your Prometheus selects on. It is off by default so a plain install does not fail on a missing CRD.

## Probes

| Path | Purpose |
| --- | --- |
| `/livez` | Process is alive |
| `/readyz` | Ready to serve traffic |
| `/startupz` | Startup complete |
| `/v1/health` | Status, version, node and edge counts |

All four are public. `/v1/health` is retained for backward compatibility with earlier chart versions that pointed probes at it, and it reports graph totals, so it is the one public endpoint that discloses anything about your data. Keep it inside the cluster.

## Backups

Standard PostgreSQL backup practice applies, with one caveat specific to this system.

> **Verify restores by row count, not by exit status.** A backup that captured 0.017% of the graph once passed verification here, because the check only confirmed `pg_restore` succeeded. Assert that the restored node and edge counts match the source.

The second caveat is `/dev/shm`. Restoring a backup rebuilds the pgvector HNSW index from scratch, and an index build asks for far more shared memory than a live index that grew incrementally. If `postgres.shmSize` is at the Kubernetes default of 64Mi the restore fails - and only the restore, which means finding out during an incident. See [Configuration](configuration.md).

## Scaling

- **API replicas** scale horizontally and hold no state between requests.
- **Rate limits do not.** The token buckets are in process memory, so the effective cluster budget is `rateLimitPerMinute` times replicas. Scale it down as replicas go up, or enforce the budget at an ingress.
- **The connection pool is per replica.** `api.pool.maxConns` defaults to 10 against Postgres's `max_connections` of 100, so past roughly 10 replicas the pool settings need revisiting before Postgres refuses connections.
- **Postgres is CPU-bound**, not IO-bound: AGE traversals and pgvector distance calculations are arithmetic. Give it cores before disk.

## Upgrades

```bash
helm upgrade kora ./charts/kora -n kora --reuse-values
```

`--reuse-values` matters: without it, credentials passed with `--set` at install are lost and the upgrade fails the same validation a fresh install would.

Rollouts are graceful, given the shutdown settings in [Configuration](configuration.md) are left alone.

## Next

- [Configuration](configuration.md) - the values behind all of this
- [Architecture](architecture.md) - what is actually running
