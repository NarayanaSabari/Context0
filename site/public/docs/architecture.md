# Architecture

How kora is put together. The [ARCHITECTURE.md](https://github.com/NarayanaSabari/Kora/blob/main/ARCHITECTURE.md) in the repository has the full diagrams; this is the readable summary.

## The shape of it

```
Agents (any framework)
        |  gRPC :50051  /  REST :8080
        v
+-------------------------------------------+
|  kora engine (Go)                          |
|                                            |
|  Ingest + Extract   Hybrid Query   Consolidation
|                                     (CronJob)
|                                            |
|  +--------------------------------------+  |
|  |  PostgreSQL 18                       |  |
|  |  Apache AGE (graph) + pgvector       |  |
|  +--------------------------------------+  |
+-------------------------------------------+
        |
   React web UI (graph view)   Prometheus /metrics
```

## One database, not two

The single most consequential decision here: the graph and the vectors live in the **same PostgreSQL instance**. Apache AGE provides graph storage and Cypher traversal; pgvector provides embeddings and similarity search. Both are PostgreSQL extensions.

The usual alternative is a dedicated vector database beside a graph database, which means two systems to operate, two backup regimes, two failure modes, and no transactional consistency between the memory and the edges pointing at it. Here, a hybrid query is one query, a backup is one backup, and a memory and its relationships commit or roll back together.

The cost is that you scale PostgreSQL rather than scaling each store independently. For this workload that has not been the binding constraint - see the CPU findings in [Configuration](configuration.md).

## The write path

```
Conversation --> Extract --> Memory nodes (semantic / episodic / procedural)
                                  |
                             Embed each
                                  |
                          Detect contradictions
                                  |
                    Write nodes + edges in one transaction
```

Extraction turns raw conversation into typed memories. Contradiction detection (`internal/extraction/contradiction.go`) is what produces `SUPERSEDES` edges automatically: a new statement that conflicts with a stored memory supersedes it rather than sitting beside it.

Embeddings are pluggable. `bag-of-words` is the default because it needs no network, no key, and no model download, so a first run works offline; Ollama and any OpenAI-compatible endpoint are the real options. See [Configuration](configuration.md).

## The read path

A query runs two retrievers and merges them:

1. **Vector retrieval** - cosine similarity over pgvector embeddings.
2. **Graph retrieval** - AGE traversal outward from matched nodes, up to `max_depth` hops, following edge weights.

Results from both are scored by a weighted linear combination of four normalised signals:

```
score = 0.55 * relevance
      + 0.25 * recency
      + 0.10 * frequency
      + 0.10 * typePriority
```

- **Relevance** (0.55) is query-match quality: cosine similarity for vector hits, lexical overlap for graph hits, boosted when both retrievers agree on the same memory. It dominates on purpose. A memory that does not answer the question should not surface merely because it is new or popular.
- **Recency** (0.25) is exponential decay with a 7-day half-life.
- **Frequency** (0.10) is `log(1 + accessCount)` squashed into `[0, 1]`, saturating around 10 accesses so a heavily-read memory cannot swamp relevance.
- **Type priority** (0.10) is static: semantic `1.0`, procedural `0.9`, episodic `0.6`. Stable facts are usually more useful than raw events.

The weights sum to 1.0 and every signal is normalised, so scores are in `[0, 1]` and comparable across queries, which matters if you want to threshold them.

Note this is a different decay from the one consolidation writes. Ranking recency has a 7-day half-life and is computed per query; the stored `decay_score` has a 30-day half-life and is recalculated by the CronJob. See [Operations](operations.md).

## Package layout

| Package | Responsibility |
| --- | --- |
| `cmd/server` | gRPC server and REST gateway |
| `cmd/consolidate` | The consolidation job |
| `cmd/cli` | Command-line client |
| `internal/auth` | API key validation, rate limiting |
| `internal/embedding` | Embedding providers: bag-of-words, Ollama, OpenAI, Google |
| `internal/extraction` | Memory extraction and contradiction detection |
| `internal/graph` | The AGE and pgvector repository |
| `internal/ranking` | Scoring and ranking |
| `internal/service` | Service handlers, consolidation phases |
| `internal/server` | Health and readiness probes |
| `internal/metrics` | Prometheus instrumentation |
| `api/proto` | Protobuf definitions, the API's source of truth |
| `charts/kora` | Helm chart |
| `web/` | React UI |
| `sdk/python/` | Python SDK |

## API surface

gRPC is the native interface; REST is grpc-gateway generated from the same protos, not a second implementation. Both are therefore always in step. See the [API reference](api.md).

## What does not exist yet

Being honest about the gap between the diagrams and the code:

- There is **no Kubernetes operator**. Deployment is the Helm chart.
- There is **no agent-pod sidecar cache**. Agents call the API directly.
- The ServiceMonitor is **off by default**, since it needs the Prometheus Operator CRDs.
- Consolidation's merge phase matches content **exactly**, not by similarity.

## Next

- [Concepts](concepts.md) - the model this implements
- [Operations](operations.md) - running it
