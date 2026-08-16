# Context0 — MVP Scope

> **Status: historical.**
> This plan is complete — the engine, API, retrieval, Helm chart, SDK, and CLI
> it describes have all shipped. The unchecked boxes below reflect the document's
> age, not outstanding work. Kept for the record of why each piece of the stack
> was chosen. See [README](README.md) for current state and the live roadmap.

## MVP Goal

Ship a working, K8s-deployable memory engine that a single agent can use to store, retrieve, and relate memories via a graph database. Prove that graph-first retrieval is meaningfully better than flat vector search.

**One sentence:** An agent can `helm install context0`, store memories, and get back the right memory at the right time — via graph traversal, not vector similarity.

---

## Tech Stack Decisions (Locked for MVP)

**Constraint: Every dependency must be fully open source (OSI-approved license). No SSPL, BSL, or proprietary components.**

| Decision | Choice | License | Rationale |
|----------|--------|---------|-----------|
| Language | **Go** | BSD-3-Clause | K8s-native, Operator SDK, single binary |
| Graph DB | **Apache AGE** (on PostgreSQL) | Apache 2.0 (AGE) + PostgreSQL License (PG) | Fully open source graph DB, runs as a Postgres extension, openCypher support, can leverage pgvector later for hybrid retrieval, familiar Postgres ops |
| Query Language | **openCypher** | Apache 2.0 | Supported by AGE, readable, widely adopted |
| API | **gRPC + REST gateway** | Apache 2.0 (grpc, grpc-gateway) | gRPC for performance, grpc-gateway for curl/debug |
| Auth | **API keys** | - | Simplest to implement. K8s ServiceAccount auth deferred to v0.2 |
| Retrieval | **Graph-only** | - | No embeddings in MVP. Pure graph traversal + metadata filters. Add pgvector embeddings in v0.2 |
| Consolidation | **Manual + basic CronJob** | - | On-demand `POST /consolidate` + simple time-based CronJob. LLM-powered consolidation deferred to v0.2 |
| Observability | **Prometheus** | Apache 2.0 | `/metrics` endpoint, basic counters and histograms. Full OTel + Grafana deferred |
| K8s DB Operator | **CloudNativePG** | Apache 2.0 | Manages PostgreSQL lifecycle on K8s (HA, backups, failover) — AGE runs inside it |

---

## What's IN the MVP

### Phase 1: Foundation (Week 1-2)

#### 1.1 Project Scaffolding
- [ ] Go module setup with standard project layout
- [ ] Makefile with targets: `build`, `test`, `lint`, `docker-build`, `proto-gen`
- [ ] Dockerfile (multi-stage, distroless base)
- [ ] CI pipeline: lint + test + build on every PR
- [ ] `.proto` files for the gRPC API

#### 1.2 Graph DB Setup
- [ ] PostgreSQL + Apache AGE deployed via CloudNativePG operator (single-node Cluster CR + PVC)
- [ ] Custom Postgres container image with AGE extension pre-installed
- [ ] Go client using `jackc/pgx` (PostgreSQL driver) with AGE Cypher queries via `ag_catalog.cypher()`
- [ ] Graph repository interface (abstraction layer — so we can swap AGE for another graph DB later)
- [ ] Schema initialization: create graph with `SELECT create_graph('context0')` + base node labels and relationship types on startup

#### 1.3 Graph Schema (Initial)

**Node types for MVP:**

| Node Type | Properties | Purpose |
|-----------|-----------|---------|
| `Project` | id, name, created_at | Scoping — all memories belong to a project |
| `Session` | id, project_id, agent_id, started_at, ended_at | Groups memories from one agent session |
| `Memory` | id, content, type (episodic/semantic/procedural), created_at, access_count, decay_score, tags[] | The core memory node |

**Edge types for MVP:**

| Edge Type | From → To | Properties | Purpose |
|-----------|-----------|-----------|---------|
| `belongs_to` | Session → Project | - | Scoping |
| `contains` | Session → Memory | - | Which session created this memory |
| `relates_to` | Memory → Memory | weight, created_at | General association |
| `supersedes` | Memory → Memory | created_at | Newer fact replaces older |
| `caused_by` | Memory → Memory | weight | Causal relationship |

---

### Phase 2: Core API (Week 3-4)

#### 2.1 gRPC API

```protobuf
service Context0 {
  // Store a new memory
  rpc Store(StoreRequest) returns (StoreResponse);

  // Retrieve memories by query
  rpc Query(QueryRequest) returns (QueryResponse);

  // Create an explicit relationship between memories
  rpc Connect(ConnectRequest) returns (ConnectResponse);

  // Delete a memory and its edges
  rpc Delete(DeleteRequest) returns (DeleteResponse);

  // Get subgraph around a memory
  rpc GetGraph(GetGraphRequest) returns (GetGraphResponse);

  // Session lifecycle
  rpc StartSession(StartSessionRequest) returns (StartSessionResponse);
  rpc EndSession(EndSessionRequest) returns (EndSessionResponse);

  // Health check
  rpc Health(HealthRequest) returns (HealthResponse);
}
```

**Store flow:**
1. Validate input (content not empty, type is valid)
2. Create `Memory` node with properties
3. If active session exists, create `contains` edge from Session → Memory
4. If tags match existing memories, auto-create `relates_to` edges (tag-based linking)
5. Return memory ID

**Query flow:**
1. Parse natural language query into graph traversal plan (simple keyword/tag matching for MVP, no LLM)
2. Execute Cypher query against Apache AGE (PostgreSQL)
3. Rank results by: recency + access_count + edge weight
4. Increment `access_count` on returned nodes
5. Return top-K memories with their connected edges (context)

**Connect flow:**
1. Validate both memory IDs exist
2. Create edge with specified relationship type and weight
3. Return edge ID

#### 2.2 REST Gateway
- [ ] grpc-gateway auto-generated from `.proto` files
- [ ] Endpoints mirror gRPC 1:1:
  - `POST /v1/memories` → Store
  - `GET /v1/memories/query?q=...` → Query
  - `POST /v1/memories/connect` → Connect
  - `DELETE /v1/memories/{id}` → Delete
  - `GET /v1/memories/{id}/graph?depth=2` → GetGraph
  - `POST /v1/sessions` → StartSession
  - `POST /v1/sessions/{id}/end` → EndSession
  - `GET /v1/health` → Health

#### 2.3 Authentication
- [ ] API key middleware — `X-API-Key` header
- [ ] API keys stored in K8s Secret, loaded at startup
- [ ] Each API key is scoped to a project ID
- [ ] Rate limiting: token bucket per API key (configurable, default 100 req/min)

---

### Phase 3: Retrieval Engine (Week 5-6)

#### 3.1 Query Parser
- [ ] Keyword extraction from natural language query
- [ ] Tag matching: if query contains known tags, filter by tag
- [ ] Type filtering: caller can restrict to episodic/semantic/procedural
- [ ] Time filtering: "recent", "last week", "today" → Cypher `WHERE` clauses

#### 3.2 Graph Traversal
- [ ] Configurable traversal depth (default: 2, max: 5)
- [ ] Neighborhood expansion: return not just matching nodes but their 1-hop neighbors
- [ ] Edge-type filtering: caller can specify which relationship types to follow
- [ ] Cypher query builder: programmatically construct Cypher queries from parsed filters

#### 3.3 Ranking
- [ ] Scoring function for each returned memory:
  ```
  score = (recency_weight × recency_factor)
        + (frequency_weight × log(access_count + 1))
        + (edge_weight × avg_edge_weight_to_query_match)
        + (type_boost × type_priority)
  ```
- [ ] Configurable weights via API (or use sensible defaults)
- [ ] Top-K selection (default: 5, max: 20)
- [ ] Return results with score + context edges (why this memory was relevant)

---

### Phase 4: K8s Deployment (Week 7-8)

#### 4.1 Helm Chart
- [ ] `charts/context0/` with standard Helm structure
- [ ] Configurable values:
  - Apache AGE (PostgreSQL) resources (memory, CPU, storage size)
  - API server replicas
  - API key secret name
  - Rate limit config
  - Consolidation schedule
- [ ] Single command install: `helm install context0 ./charts/context0`

#### 4.2 K8s Manifests (Non-Helm Alternative)
- [ ] `deploy/` directory with raw YAML manifests
- [ ] Kustomize overlays for dev/staging/prod

#### 4.3 Workloads
- [ ] **context0-api** — Deployment (2 replicas default), ClusterIP Service (gRPC :50051, REST :8080)
- [ ] **age-postgres** — CloudNativePG Cluster CR (1 instance for MVP, PVC auto-managed), ClusterIP Service
- [ ] **consolidation** — CronJob (every 6h default), runs merge + decay

#### 4.4 Basic Consolidation CronJob
- [ ] **Merge:** Find memory pairs with identical tags + high content overlap → create `supersedes` edge, mark older as stale
- [ ] **Decay:** Recalculate `decay_score` for all nodes based on last access time
- [ ] **Prune:** Delete nodes where `decay_score < 0.1` and `access_count == 0` and `age > 30 days`
- [ ] Configurable via environment variables on the CronJob

#### 4.5 Observability (Basic)
- [ ] Prometheus `/metrics` endpoint on the API server
- [ ] Metrics:
  - `context0_memories_total` (counter, by type)
  - `context0_edges_total` (counter, by relationship)
  - `context0_query_duration_seconds` (histogram)
  - `context0_store_duration_seconds` (histogram)
  - `context0_query_results_count` (histogram)
  - `context0_active_sessions` (gauge)

---

### Phase 5: SDK + Demo (Week 9-10)

#### 5.1 Python SDK (First SDK)
- [ ] `pip install context0`
- [ ] Generated from `.proto` files via `grpcio-tools`
- [ ] Thin wrapper with Pythonic API:
  ```python
  from context0 import Context0Client

  client = Context0Client(
      endpoint="context0.default.svc.cluster.local:50051",
      api_key="ctx0_...",
      project="my-project",
  )

  # Store
  mem = client.store(
      content="Project uses PostgreSQL 15.x",
      type="semantic",
      tags=["database", "postgres"],
  )

  # Query
  results = client.query("what database does this project use?", top_k=3)

  # Connect
  client.connect(mem.id, other_mem.id, relationship="supersedes")

  # Session
  with client.session() as s:
      client.store("discussed auth architecture", type="episodic")
      client.store("decided on JWT + refresh tokens", type="semantic")
  # session auto-ends, memories linked to session
  ```

#### 5.2 CLI Tool
- [ ] `context0` CLI for debugging and manual operations
- [ ] Commands:
  - `context0 store "memory content" --type semantic --tags db,postgres`
  - `context0 query "what database?" --top-k 5`
  - `context0 graph <memory-id> --depth 2` (ASCII visualization of subgraph)
  - `context0 list --type semantic --limit 20`
  - `context0 delete <memory-id>`
  - `context0 stats` (node count, edge count, memory types breakdown)

#### 5.3 Demo
- [ ] Demo script: spin up a local K8s cluster (kind/minikube), install Context0, run a scripted agent interaction
- [ ] Shows: store → query → connect → query-with-context → consolidate → query-again
- [ ] README with GIF/recording of the demo

---

## What's NOT in the MVP (Deferred)

| Feature | Deferred To | Reason |
|---------|-------------|--------|
| K8s Operator + CRDs | v0.2 | Helm chart is sufficient for MVP. Operator adds complexity. |
| Embeddings / vector search (pgvector) | v0.2 | Pure graph traversal first. Since we're on PostgreSQL, pgvector (PostgreSQL License) can be added alongside AGE for hybrid retrieval. |
| LLM-powered consolidation | v0.2 | Basic rule-based merge/decay for MVP. LLM summarization adds cost + LLM dependency. |
| Multi-agent shared memory | v0.2 | MVP is single-agent. Multi-agent scoping (global/project/agent) comes next. |
| Sidecar injection | v0.2 | In-cluster ClusterIP is fast enough. Sidecar cache is an optimization. |
| K8s ServiceAccount auth | v0.2 | API keys are simpler. SA auth needs webhook token review setup. |
| Multiple graph DB support | v0.3 | MVP locks to Apache AGE (PostgreSQL). Graph repository interface allows swapping later. |
| Grafana dashboards | v0.2 | Prometheus `/metrics` endpoint is enough for MVP. |
| Multi-cluster federation | v0.3+ | Way too early. Single cluster first. |
| UI / graph visualization | v0.3+ | API-only for MVP. CLI `graph` command provides basic viz. |
| Event-driven consolidation | v0.2 | CronJob is simpler. Event hooks add complexity. |
| Go / TypeScript SDKs | v0.2 | Python SDK first. Other SDKs generated from same `.proto`. |
| Preference / Correction nodes | v0.2 | MVP has 3 memory types (episodic, semantic, procedural). Expand later. |
| HPA auto-scaling | v0.2 | Fixed replicas for MVP. |

---

## MVP Success Criteria

Before calling the MVP done, these must be true:

### Functional
- [ ] Agent can store a memory and retrieve it by natural language query
- [ ] Memories are linked via typed edges (relates_to, supersedes, caused_by)
- [ ] Query returns memories with context edges (not just isolated nodes)
- [ ] Session lifecycle works (start → store memories → end → memories linked to session)
- [ ] Consolidation CronJob runs and prunes stale memories
- [ ] API keys restrict access per project

### Deployment
- [ ] `helm install context0 ./charts/context0` works on a fresh cluster
- [ ] Works on kind, minikube, EKS, GKE (standard K8s)
- [ ] Apache AGE (PostgreSQL) data persists across pod restarts (PVC)
- [ ] API is accessible in-cluster via ClusterIP and externally via Ingress

### Performance
- [ ] Query latency: <50ms p95 (in-cluster, graph with <10K nodes)
- [ ] Store latency: <30ms p95
- [ ] Handles 100 req/s sustained on a 2-CPU API server

### Quality
- [ ] >80% test coverage on core services (ingest, query, graph repository)
- [ ] Integration tests against real Apache AGE (PostgreSQL) (not mocks)
- [ ] CI pipeline passes: lint + test + build + docker-build
- [ ] Proto files have complete documentation

---

## Timeline

```
Week 1-2:   Foundation — project setup, graph DB, schema, CI
Week 3-4:   Core API — gRPC/REST, store/query/connect, auth
Week 5-6:   Retrieval — query parser, traversal, ranking
Week 7-8:   K8s — Helm chart, manifests, consolidation CronJob, metrics
Week 9-10:  SDK + Demo — Python SDK, CLI, demo script, README
```

**Total: ~10 weeks to a working, deployable MVP.**

---

## Repo Structure (Planned)

```
context0/
├── cmd/
│   ├── server/              # API server entrypoint
│   │   └── main.go
│   ├── consolidate/         # Consolidation job entrypoint
│   │   └── main.go
│   └── cli/                 # CLI tool entrypoint
│       └── main.go
├── api/
│   └── proto/               # .proto files
│       └── context0/
│           └── v1/
│               ├── memory.proto
│               ├── session.proto
│               └── health.proto
├── internal/
│   ├── server/              # gRPC + REST server setup
│   ├── service/             # Core business logic
│   │   ├── ingest.go        # Memory ingestion
│   │   ├── query.go         # Query planning + execution
│   │   └── consolidate.go   # Consolidation logic
│   ├── graph/               # Graph DB layer
│   │   ├── repository.go    # Interface
│   │   ├── age.go           # Apache AGE implementation
│   │   └── schema.go        # Graph schema init
│   ├── ranking/             # Result ranking
│   │   └── scorer.go
│   ├── auth/                # API key middleware
│   │   └── apikey.go
│   └── metrics/             # Prometheus metrics
│       └── metrics.go
├── pkg/
│   └── model/               # Shared types (Memory, Edge, Session)
│       ├── memory.go
│       ├── edge.go
│       └── session.go
├── charts/
│   └── context0/            # Helm chart
│       ├── Chart.yaml
│       ├── values.yaml
│       └── templates/
├── deploy/                  # Raw K8s manifests + Kustomize
├── sdk/
│   └── python/              # Python SDK
│       ├── pyproject.toml
│       └── src/context0/
├── scripts/                 # Build, test, demo scripts
├── docs/                    # Additional documentation
├── Dockerfile
├── Makefile
├── go.mod
└── README.md
```
