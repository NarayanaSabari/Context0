# Configuration

The server reads everything from environment variables, so no configuration file is needed for local development. The Helm chart sets the same variables from its values.

## Environment variables

### Server

| Variable | Default | Meaning |
| --- | --- | --- |
| `KORA_GRPC_PORT` | `50051` | gRPC listen port |
| `KORA_HTTP_PORT` | `8080` | REST gateway and `/metrics` port |
| `KORA_VERSION` | `0.1.0-dev` | Version reported by `/v1/health` |

### Database

| Variable | Default | Meaning |
| --- | --- | --- |
| `KORA_DATABASE_URL` | `postgres://kora:kora@localhost:5432/kora?sslmode=disable` | Connection string. The database must have Apache AGE installed |

The default `sslmode=disable` exists only because the in-cluster Postgres this chart ships does not serve TLS. Pointing at a TLS-terminating Postgres means setting `sslMode: verify-full`.

### Authentication

| Variable | Default | Meaning |
| --- | --- | --- |
| `KORA_API_KEYS` | empty | Comma-separated accepted keys. **Empty disables authentication entirely** |
| `KORA_RATE_LIMIT_PER_MINUTE` | `6000` | Per-key budget, per replica |

An empty key list disables auth rather than locking everything out. In the chart this is gated behind an explicit `auth.allowUnauthenticated: true`, so a deployment that means it has to say so, and one that forgot its keys fails to install instead of silently opening.

The rate limit is **per pod, not cluster-wide**: the token buckets live in process memory, so N replicas admit roughly N times this rate. Scale the number down as replicas go up, or move rate limiting to an ingress or service mesh for a true global budget.

### Embeddings

| Variable | Default | Meaning |
| --- | --- | --- |
| `KORA_EMBEDDING_PROVIDER` | `bag-of-words` | `bag-of-words`, `ollama`, `openai`, or `google` |
| `KORA_EMBEDDING_MODEL` | empty | e.g. `nomic-embed-text`, `text-embedding-3-small` |
| `KORA_EMBEDDING_API_KEY` | empty | For the cloud providers |
| `KORA_EMBEDDING_BASE_URL` | empty | Override for Ollama or any OpenAI-compatible endpoint |
| `KORA_EMBEDDING_DIM` | `0` | Vector dimension; `0` auto-detects from the provider |

`bag-of-words` is the default because it needs no network, no API key, and no model download, which makes a first run work offline. It is also the weakest option: semantic recall is noticeably better with a real embedding model. Use Ollama for self-hosted, or OpenAI-compatible for hosted.

Changing the provider or dimension on a populated database invalidates the existing embeddings. They are not comparable across models, so plan on re-embedding.

### Extraction

How `POST /v1/memories/extract` turns a raw conversation into memories.

| Variable | Default | Meaning |
| --- | --- | --- |
| `KORA_EXTRACTION_PROVIDER` | `rule` | `rule` or `llm` |
| `KORA_EXTRACTION_MODEL` | `gpt-4o-mini` | Chat model, when the provider is `llm` |
| `KORA_EXTRACTION_API_KEY` | empty | Not needed for a local endpoint such as Ollama |
| `KORA_EXTRACTION_BASE_URL` | `https://api.openai.com` | Any OpenAI-compatible chat-completions endpoint |

`rule` is the default for the same reason `bag-of-words` is: no network, no API key, no spend. It scans the conversation line by line, which means it transcribes rather than distils. Each utterance becomes its own memory, so pronouns are never resolved and questions and small talk are stored alongside facts. A memory reading `He hates thunderstorms` is useful in context and useless once retrieved on its own.

`llm` sends the conversation to a chat model and asks for standalone facts: pronouns resolved against the speakers, related statements merged, filler dropped. The same five-line exchange yields `Caroline adopted a rescue dog named Biscuit last month` rather than five fragments. It costs one request per conversation.

Any provider speaking the OpenAI chat-completions API works, including Ollama, vLLM, LiteLLM, OpenRouter, and Gemini via its compatibility layer:

```bash
KORA_EXTRACTION_PROVIDER=llm
KORA_EXTRACTION_MODEL=gemini-2.5-flash
KORA_EXTRACTION_BASE_URL=https://generativelanguage.googleapis.com/v1beta/openai
KORA_EXTRACTION_API_KEY=...
```

A provider that is unreachable, slow, or returns something unparseable falls back to rule-based extraction for that request. `Extract` is a write path and the caller keeps no copy of the conversation, so degrading beats losing it.

### Logging

| Variable | Default | Meaning |
| --- | --- | --- |
| `KORA_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `KORA_LOG_FORMAT` | `json` | `json` for aggregators, `text` for reading directly |

## Helm values worth knowing

The full list is in [`charts/kora/values.yaml`](https://github.com/NarayanaSabari/Kora/blob/main/charts/kora/values.yaml), which is heavily commented. These are the ones with reasoning behind them that is not obvious from the name.

### Postgres CPU has no limit, on purpose

`postgres.resources.requests.cpu` is `1500m` and there is deliberately **no CPU limit**.

Under a soak of 8 concurrent clients, roughly 35 operations per second, Postgres sustains about 1.5 cores: AGE traversals and pgvector distance calculations are CPU-bound, not IO-bound. A 1000m limit therefore throttled it in 94% of scheduling periods. That is invisible in every pod-level signal - the container is Running, healthy, under its memory limit, never restarting - and shows up only as uniformly slow queries.

Removing the ceiling took store p50 from 396ms to 33ms and query p50 from 523ms to 45ms. A 12x improvement, from a value originally picked because it was a round number.

CPU limits throttle even when the node is idle, so the limit is absent rather than merely raised. Set one only where quota or noisy-neighbour policy requires it, and size it well above 1.5 cores.

### /dev/shm is 256Mi, not the Kubernetes default

`postgres.shmSize` defaults to `256Mi` because Kubernetes defaults `/dev/shm` to 64Mi, which is not enough to build the pgvector HNSW index. At 94k embeddings the build asked for 131MB in a single allocation and failed.

That failure is only reachable on a *rebuild*, which in practice means restoring a backup: a live index grows incrementally and never hits it. So it goes unnoticed until recovery, which is the worst possible moment to find out.

### Postgres memory is 2Gi, sized from its parts

`shmSize` counts against the pod memory limit because it is `medium: Memory`. Adding it up: 256MB shm, 512MB `shared_buffers`, 160MB for `work_mem` across 10 connections, and roughly 1GB of headroom for query execution, page cache, and WAL buffers. At 1Gi a six-worker soak OOM-killed Postgres six times.

PostgreSQL does not size itself from the cgroup limit, so `postgres.tuning` sets `sharedBuffers`, `effectiveCacheSize`, `workMem`, and `maintenanceWorkMem` explicitly. Raise them together with the resource limits, never one alone.

### Connection pool

`api.pool.maxConns` defaults to `10`. pgxpool would otherwise default `MaxConns` to the **node's** core count, which has nothing to do with the container's CPU limit: a few replicas on a large node would exhaust Postgres's `max_connections` of 100.

### Graceful shutdown

`terminationGracePeriodSeconds` is 30 and `preStopSleepSeconds` is 5. The grace period has to exceed the process's own 15 second drain plus the preStop sleep, or Kubernetes SIGKILLs mid-drain and the connection pool is never closed. The preStop sleep covers the gap between kubelet sending SIGTERM and kube-proxy finishing endpoint removal; without it, rollouts drop connections.

### Consolidation

```yaml
consolidation:
  enabled: true
  schedule: "0 */6 * * *"
  decayHalfLifeDays: "30"
  staleThreshold: "0.1"
  pruneAgeDays: "30"
```

See [Operations](operations.md).

### Metrics

`metrics.serviceMonitor.enabled` is `false` by default because a ServiceMonitor requires the Prometheus Operator CRDs, and a plain install should not fail on a missing CRD. Turn it on where kube-prometheus-stack is present, and set `metrics.serviceMonitor.labels` to whatever your Prometheus selects on.

## Next

- [Operations](operations.md) - running it day to day
- [Installation](installation.md) - getting it deployed in the first place
