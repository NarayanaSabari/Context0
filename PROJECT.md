# Context0 — Universal Memory Engine for AI Agents

## One-Liner

A Kubernetes-native memory engine that gives any AI agent persistent, intelligent memory — like a human brain for agentic AI. Graph-first, not vector-first. **100% open source.**

### Open-Source Stack

| Component | Technology | License |
|-----------|-----------|---------|
| Engine | Go | BSD-3-Clause |
| Graph DB | Apache AGE (PostgreSQL extension) | Apache 2.0 |
| Database | PostgreSQL | PostgreSQL License (OSI-approved) |
| Vector search (v0.2) | pgvector | PostgreSQL License (OSI-approved) |
| K8s DB operator | CloudNativePG | Apache 2.0 |
| API | gRPC + grpc-gateway | Apache 2.0 |
| Observability | OpenTelemetry + Prometheus + Grafana | Apache 2.0 / AGPLv3 |
| CI | GitHub Actions | Free for open source |
| Container | Distroless base image | Apache 2.0 |

---

## The Problem

Every agentic AI framework today suffers from the same fundamental flaw: **agents are amnesiac**.

- **Sessions are isolated.** Each conversation starts from scratch. Context built over hours of collaboration is lost the moment a session ends.
- **Memory is framework-locked.** LangChain has its own memory, CrewAI has its own, AutoGen has its own — none of them talk to each other. Switch frameworks, lose everything.
- **Retrieval is dumb.** Most implementations are flat vector stores with cosine similarity. No concept of importance, recency, decay, or consolidation. You get 50 vaguely similar chunks instead of the 3 that actually matter.
- **No learning over time.** Agents don't get better at working with you. They don't remember your preferences, your codebase patterns, your communication style, or what worked last time.
- **No shared knowledge.** Multiple agents working on the same project can't share what they've learned. Agent A discovers a critical constraint, Agent B repeats the same mistake.

### Real-World Pain Points

| Scenario | What Happens Today |
|----------|-------------------|
| Developer uses Cursor + Claude Code on the same project | Each tool maintains separate, incompatible context |
| Team of agents working on a codebase | No shared understanding — each agent re-discovers the same things |
| Long-running project over weeks | Context is rebuilt from scratch every session |
| Switching from LangChain to CrewAI | All accumulated agent memory is lost |
| Agent makes a mistake, gets corrected | Same mistake next session — correction wasn't persisted |

---

## The Vision

Context0 is a **standalone memory service** that any AI agent, in any framework, can use to store, retrieve, and reason over persistent memory. It models memory the way the human brain does — not as a flat database, but as a layered, adaptive system.

### Human Brain Analogy

| Human Memory | Context0 Equivalent | Purpose |
|-------------|---------------------|---------|
| **Working Memory** | Active context buffer | What's relevant right now for the current task |
| **Episodic Memory** | Session logs, interactions | What happened — past conversations, decisions, outcomes |
| **Semantic Memory** | Facts, knowledge, preferences | What is true — project structure, user preferences, domain knowledge |
| **Procedural Memory** | Learned patterns, workflows | How to do things — validated approaches, effective patterns |
| **Emotional Memory** | Priority/importance signals | What matters — urgency markers, user sentiment, correction history |

### How It Works (High Level)

```
┌─── Kubernetes Cluster ──────────────────────────────────────┐
│                                                              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                  │
│  │ Agent A  │  │ Agent B  │  │ Agent C  │   (any framework) │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘                  │
│       │              │              │                         │
│       └──────────────┼──────────────┘                        │
│                      │  gRPC (in-cluster)                    │
│                      ▼                                       │
│  ┌───────────────────────────────────────────────────┐      │
│  │              Context0 Engine (Operator)            │      │
│  │                                                    │      │
│  │  ┌───────────┐  ┌────────────┐  ┌──────────────┐ │      │
│  │  │  Ingest   │  │  Retrieve  │  │ Consolidate  │ │      │
│  │  │  Layer    │→ │  (Graph    │← │ (CronJob)    │ │      │
│  │  │           │  │  Traversal)│  │              │ │      │
│  │  └───────────┘  └────────────┘  └──────────────┘ │      │
│  │                                                    │      │
│  │  ┌────────────────────────────────────────────┐   │      │
│  │  │        Graph DB (StatefulSet + PV)          │   │      │
│  │  │  Nodes: memories, sessions, agents, facts  │   │      │
│  │  │  Edges: caused_by, learned_during,         │   │      │
│  │  │         supersedes, relates_to, validates   │   │      │
│  │  └────────────────────────────────────────────┘   │      │
│  └───────────────────────────────────────────────────┘      │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

---

## Goals

### G1: Kubernetes-Native Deployment
- Runs as a K8s Operator with CRDs (`MemoryStore`, `MemoryPolicy`, `ConsolidationSchedule`)
- One-command install: `helm install context0` or `kubectl apply -f context0.yaml`
- In-cluster gRPC API — agents in the same cluster get sub-20ms latency
- External REST API for agents outside the cluster
- Auto-scales with HPA, backs up via volume snapshots

### G2: Typed Memory System
- First-class memory types (episodic, semantic, procedural) — not a flat key-value store
- Each type has its own storage, retrieval, and lifecycle semantics
- Agents declare what type of memory they're storing/querying

### G3: Graph-First Intelligent Retrieval
- Relationship-aware traversal instead of flat vector similarity
- Multi-signal ranking: edge weight + recency + traversal depth + access frequency + decay
- Context-aware retrieval — follow relationship edges to find what's connected, not just what's similar
- Subgraph extraction — return a connected neighborhood of memories, not isolated chunks
- Return fewer, better results (precision over recall)

### G4: Automatic Consolidation
- Merge redundant memories over time
- Promote frequently-accessed memories, decay stale ones
- Summarize verbose episodic memories into compact semantic facts
- Like how the brain consolidates during sleep — a background process that keeps memory clean

### G5: Multi-Agent Shared Memory
- Multiple agents on the same project share a memory namespace
- Scoped access — project-level, user-level, global-level memories
- Conflict resolution when agents write contradictory facts

### G6: Privacy and Isolation
- Strict tenant isolation — no memory leaks between users or projects
- Encryption at rest and in transit
- User-controlled retention policies (auto-delete after N days, export, purge)
- Self-hostable for enterprise use cases

---

## Technical Challenges

### C1: Retrieval Quality — Graph-First, Not Vector-First

Vector databases are the wrong primitive for agent memory. They find "similar" things, not "related" things. Memory isn't about similarity — it's about **relationships**.

**Why vector search fails for memory:**
- "What database does this project use?" returns 50 chunks that mention databases — but doesn't know which one was the **decision**
- No concept of causality — can't answer "why did we choose PostgreSQL?" because it doesn't link the decision to its reasoning
- No temporal relationships — can't distinguish "we used MySQL last month" from "we migrated to PostgreSQL this week"
- Flat retrieval — every memory is equally connected to every other memory (it's not)

**Why a graph database is the right fit:**

Memory is naturally a graph. Memories have relationships:
- A **decision** was made **because of** a **constraint**
- A **preference** was learned **during** a **session**
- A **fact** about the project **supersedes** an older **fact**
- A **pattern** was **validated by** multiple **episodes**

```
[User prefers TypeScript] ──learned_during──▶ [Session 2024-03-15]
        │                                            │
        │ influences                                  │ contains
        ▼                                            ▼
[Chose Next.js for frontend] ──because_of──▶ [Team already knows React]
        │
        │ supersedes
        ▼
[Previous: considered Svelte]
```

With a graph, retrieval becomes **traversal**:
- "What do I know about the frontend?" → traverse from `frontend` node, follow all edges
- "Why did we pick Next.js?" → follow `because_of` edges from the decision node
- "What changed since last week?" → filter by temporal edges, traverse from recent nodes

**Graph retrieval approaches:**
- Relationship-aware traversal — follow edges by type (caused_by, learned_during, supersedes)
- Subgraph extraction — pull the relevant neighborhood of nodes for a query
- Weighted edges — frequently traversed paths rank higher (like neural pathways strengthening)
- Temporal filtering — traverse only edges within a time window
- LLM-powered graph queries — natural language → graph traversal query
- Hybrid: graph for relationships + lightweight embeddings on node content for fuzzy matching within the graph

### C2: Memory Consolidation

Raw episodic memory is verbose and redundant. Over time it needs to be:
- **Merged** — 10 sessions discussing auth should become one semantic fact about auth architecture
- **Summarized** — long interaction logs compressed into key takeaways
- **Pruned** — outdated memories removed or archived
- **Promoted** — repeated patterns elevated from episodic to procedural memory

This is essentially a background ETL pipeline with LLM-powered transformation. Cost and latency matter.

### C3: Graph Modeling — Getting the Schema Right

The graph schema is the foundation. Get it wrong and retrieval breaks.

- **Node types** — what are the core entities? Memories, sessions, agents, users, projects, decisions, facts, preferences?
- **Edge types** — what relationships matter? caused_by, learned_during, supersedes, validates, contradicts, relates_to?
- **Edge direction** — is "A supersedes B" the same as "B is superseded by A"? Traversal direction matters.
- **Property design** — what metadata lives on nodes vs edges? Timestamps, confidence scores, access counts?
- **Schema evolution** — how do we add new node/edge types without breaking existing graphs?
- **Granularity** — is "the project uses PostgreSQL" one node or three (project, uses, PostgreSQL)?

The schema will evolve. Need to design for that from day one.

### C4: Consistency and Conflicts

When multiple agents write to shared memory:
- Agent A says "use PostgreSQL", Agent B says "use MySQL" — which wins?
- Agent A updates a fact that Agent B cached locally — stale reads
- Two agents write the same memory simultaneously — last-write-wins? Merge?

Need a clear consistency model (eventual? causal? strong?) and conflict resolution strategy.

### C5: Cost Control

- Embedding generation for every memory write costs money
- LLM-powered consolidation costs money
- Storage at scale costs money
- Need tiered storage (hot/warm/cold) and budget-aware consolidation schedules

### C6: Latency — Kubernetes-Native Architecture

Context0 is not just "deployed on K8s" — it's **built for K8s**. The memory engine runs as a native Kubernetes workload, leveraging the platform for performance, scaling, and operational simplicity.

**Why K8s-native matters for latency:**

Traditional approach: Agent → Load Balancer → API Server → Database → back
K8s-native approach: Agent (same cluster) → Context0 sidecar/service → in-cluster graph DB → back

When the agent and Context0 run in the same cluster, you eliminate external network hops entirely.

**K8s-Native Design:**

```
┌─── Kubernetes Cluster ───────────────────────────────┐
│                                                      │
│  ┌─────────────┐     ┌──────────────────────────┐    │
│  │ Agent Pod   │     │ Context0 Operator        │    │
│  │             │────▶│                          │    │
│  │ (sidecar or │     │ ┌──────────┐ ┌─────────┐ │    │
│  │  ClusterIP) │     │ │ Memory   │ │ Consoli-│ │    │
│  └─────────────┘     │ │ API      │ │ dation  │ │    │
│                      │ └──────────┘ │ CronJob │ │    │
│  ┌─────────────┐     │              └─────────┘ │    │
│  │ Agent Pod   │────▶│                          │    │
│  └─────────────┘     │ ┌──────────────────────┐ │    │
│                      │ │Graph DB (StatefulSet)│ │    │
│  ┌─────────────┐     │ │with persistent vols  │ │    │
│  │ Agent Pod   │────▶│ └──────────────────────┘ │    │
│  └─────────────┘     └──────────────────────────┘    │
│                                                      │
└───────────────────────────────────────────────────────┘
```

**K8s primitives we leverage:**
- **Custom Resource Definitions (CRDs)** — `MemoryStore`, `MemoryPolicy`, `ConsolidationSchedule` as K8s resources
- **Operator pattern** — Context0 Operator manages the full lifecycle (deploy, scale, backup, consolidation)
- **StatefulSet** — graph DB runs as a StatefulSet with persistent volumes
- **CronJobs** — memory consolidation runs as K8s CronJobs (the "sleep" cycle)
- **Sidecar injection** — optional: inject a Context0 sidecar into agent pods for sub-millisecond local cache
- **ClusterIP service** — in-cluster traffic only, no external network hops
- **HPA** — auto-scale the memory API based on query load
- **NetworkPolicy** — tenant isolation enforced at the network level

**Latency targets with K8s-native:**
- In-cluster retrieval: <20ms (p95) — no external hops
- Sidecar cache hit: <5ms — local memory
- Write (async, acknowledged): <50ms
- Write (sync, persisted): <100ms

**Operational benefits:**
- `kubectl apply -f context0.yaml` — one command to deploy the entire memory engine
- Helm chart for configuration
- Scales with the cluster — more agents = auto-scale Context0
- Backup/restore via K8s volume snapshots
- Monitoring via Prometheus ServiceMonitor + Grafana dashboards

### C7: Graph DB Operations on K8s

Running a stateful graph database on Kubernetes is non-trivial:
- **Persistent storage** — graph data needs reliable PVs, not ephemeral storage
- **Backup/restore** — volume snapshots + graph-level export for disaster recovery
- **Upgrades** — rolling updates without data loss or query downtime
- **Resource sizing** — graph traversal is memory-intensive, need proper resource limits
- **Monitoring** — graph-specific metrics (node count, edge count, traversal latency, cache hit rate)

---

## Competitive Landscape

| Product | Approach | Gaps |
|---------|----------|------|
| **Mem0** | Memory layer for AI apps, vector + graph | Tightly coupled to their platform, limited memory types |
| **Zep** | Long-term memory for LLM apps | Session-focused, less on cross-agent sharing |
| **Letta (MemGPT)** | Self-editing memory in context window | Agent-specific, not a shared service |
| **LangMem** | LangChain's built-in memory | Framework-locked, basic implementations |
| **Cognee** | Knowledge graph memory | Heavy setup, enterprise-focused |

### Where Context0 Differentiates

1. **100% open source** — every component is OSI-approved. No SSPL, BSL, or proprietary lock-in. Apache 2.0 + PostgreSQL License throughout.
2. **Graph-first** — relationships between memories, not flat vector similarity
3. **Kubernetes-native** — not "runs on K8s" but built *for* K8s with CRDs, Operators, and in-cluster performance
4. **Typed memory model** — episodic/semantic/procedural as first-class node types in the graph
5. **Smart consolidation** — memory graph gets refined over time via K8s CronJobs
6. **Multi-agent native** — shared memory graph with scoped subgraphs per agent/project
7. **Self-hostable** — `helm install` into your own cluster, zero vendor dependencies

---

## Decisions Needed

Before building the MVP, we need to make choices in the following areas. Each lists the options with trade-offs.

**Constraint: All dependencies must be fully open source (OSI-approved license). No SSPL, BSL, BUSL, or proprietary components.**

---

### D1: Graph Database

Only OSI-approved open-source options are considered. FalkorDB (SSPL), SurrealDB (BSL), and Memgraph (BSL) are excluded.

| Option | License | K8s Support | Query Language | Strengths | Weaknesses |
|--------|---------|-------------|----------------|-----------|------------|
| **Apache AGE** | Apache 2.0 | Runs as Postgres extension — use CloudNativePG (Apache 2.0) or Zalando operator | openCypher (subset) | Rides on Postgres — familiar ops, reuse existing Postgres infra, SQL + Cypher in same query, free clustering via Postgres HA, can add pgvector (PostgreSQL License) for hybrid retrieval later | Younger project, smaller community, openCypher support is partial (not full Cypher), graph performance trails dedicated graph DBs |
| **Neo4j Community** | GPLv3 (copyleft) | Official Helm chart, Neo4j Operator | Cypher | Most mature graph DB, largest ecosystem, APOC plugin library, GDS (Graph Data Science) for analytics, massive community | GPLv3 copyleft (any linked code must also be GPL), Community edition is single-instance (no clustering), JVM-based (heavy memory footprint) |
| **NebulaGraph** | Apache 2.0 | K8s Operator (nebula-operator) | nGQL (Cypher-like) | Distributed-native, horizontal scaling, designed for massive graphs, good K8s story | Custom query language (nGQL, not Cypher), steeper learning curve, smaller Western community, heavier infra for small deployments |
| **JanusGraph** | Apache 2.0 | Helm charts available | Gremlin | Distributed, pluggable backends (Cassandra, HBase, Bigtable), mature | Heavy infra requirements, Gremlin is verbose, complex to operate |

**Decision: Apache AGE** — Apache 2.0 (permissive), rides on PostgreSQL (PostgreSQL License, OSI-approved), CloudNativePG for K8s ops, and pgvector can be added later for hybrid retrieval. One database for graph + relational + vector.

---

### D2: Graph Query Language

| Option | Used By | Standardized | Strengths | Weaknesses |
|--------|---------|-------------|-----------|------------|
| **Cypher** | Neo4j, Memgraph, FalkorDB, AGE (partial) | De facto standard, GQL is based on it | Most widely adopted, readable ASCII-art syntax, huge learning resources | Originated from Neo4j (vendor-tied history), full spec not available in all DBs |
| **GQL (ISO/IEC 39075)** | Emerging | ISO standard (2024) | Official international standard, vendor-neutral by design | Very new, limited DB support today, tooling still maturing |
| **Gremlin** | JanusGraph, Amazon Neptune, Azure CosmosDB | Apache TinkerPop standard | Cloud provider support (AWS, Azure), imperative traversal style gives fine control | Verbose syntax, harder to read than Cypher, less intuitive for graph beginners |
| **nGQL** | NebulaGraph | No | Designed for distributed queries | Vendor-locked to NebulaGraph, small community |
| **SurrealQL** | SurrealDB | No | Multi-model queries (graph + document + SQL in one) | Vendor-locked to SurrealDB |
| **SPARQL** | RDF stores (Blazegraph, Stardog) | W3C standard | Semantic web standard, good for ontologies | Designed for RDF triples, overkill for our use case, verbose |

**Leaning toward:** Cypher — widest adoption, most readable, and most graph DB options support it.

---

### D3: Hybrid Retrieval (Graph + Embeddings)

Since we chose Apache AGE on PostgreSQL, **pgvector** (PostgreSQL License, OSI-approved) is the natural fit for embeddings — same database, no extra infrastructure.

| Option | How It Works | Strengths | Weaknesses |
|--------|-------------|-----------|------------|
| **Graph-only** | All retrieval via graph traversal and metadata filters | Simple, no embedding cost, deterministic results | Can't handle fuzzy/semantic queries ("memories about deployment issues") |
| **AGE graph + pgvector in same Postgres** | Store embeddings in a relational table with pgvector, link to graph nodes via ID. Query = graph traversal + vector similarity in one DB | Single database for everything, no sync issues, one backup/restore, pgvector is battle-tested | Slightly more complex queries (SQL + Cypher in same transaction), pgvector performance is good but not best-in-class |
| **AGE graph + external vector DB** | Graph for structure, separate open-source vector store (Qdrant AGPLv3, Milvus Apache 2.0) for semantic search | Can use best-in-class for each | Two systems to maintain, consistency issues, added latency, more ops burden |
| **Embeddings on edges** | Embed the relationship context, not just node content | Captures "how things relate" semantically | Novel approach, less tooling support, higher storage cost |

**Decision: AGE graph + pgvector in same Postgres** — one database for graph + vector + relational. Zero extra infrastructure. Add pgvector in v0.2 after proving graph-only retrieval in MVP.

---

### D4: Authentication Model

| Option | How It Works | Strengths | Weaknesses |
|--------|-------------|-----------|------------|
| **K8s ServiceAccount + RBAC** | Agents authenticate via their pod's ServiceAccount, RBAC rules control memory access | Zero additional auth infra, native to K8s, no secrets to manage | Only works for in-cluster agents, no external access |
| **API keys** | Static tokens per agent/project, validated by Context0 API | Simple, works everywhere (in-cluster and external), easy to integrate | Key rotation burden, keys can leak, no fine-grained scoping |
| **mTLS between pods** | Mutual TLS certificates, identity from cert CN/SAN | Strong identity, encrypted by default, no tokens to manage | Certificate management complexity (need cert-manager), harder to debug |
| **OAuth2 / OIDC** | Standard token-based auth with an identity provider | Industry standard, integrates with existing IdPs (Keycloak, Auth0), fine-grained scopes | Heavy for agent-to-service calls, token refresh overhead, needs an IdP |
| **Hybrid: ServiceAccount (in-cluster) + API keys (external)** | K8s RBAC for pods inside the cluster, API keys for external agents | Best of both — zero-config for K8s agents, simple auth for external | Two auth paths to maintain |

**Leaning toward:** Hybrid — ServiceAccount for in-cluster (most common case), API keys for external access. Add mTLS later for enterprise.

---

### D5: Memory Consolidation Strategy

| Option | Trigger | Strengths | Weaknesses |
|--------|---------|-----------|------------|
| **Time-based CronJob** | Runs every N hours (e.g., every 6h, daily) | Predictable, simple to configure, K8s-native | May run when unnecessary (wasted compute) or too late (stale memories linger) |
| **Threshold-based** | Triggers when node/edge count exceeds a threshold | Only runs when needed, resource-efficient | Needs monitoring infra to detect thresholds, bursty workloads can trigger too often |
| **Event-driven** | Triggers on specific events (session end, agent disconnect, memory conflict detected) | Most responsive, consolidates at natural breakpoints | Complex event wiring, may consolidate too aggressively |
| **Hybrid: CronJob + event hooks** | Scheduled baseline + event triggers for urgent consolidation (conflicts, session ends) | Covers both routine cleanup and urgent cases | More moving parts to configure |
| **On-demand API** | Agent explicitly calls `POST /consolidate` | Full control, agent decides when to consolidate | Agents forget to call it, inconsistent behavior |

**Leaning toward:** Hybrid — CronJob for routine consolidation (the "sleep" cycle) + event hooks for session-end and conflict resolution.

---

### D6: API Protocol

| Option | Strengths | Weaknesses |
|--------|-----------|------------|
| **gRPC** | Fast (HTTP/2, binary), strongly typed (protobuf), streaming support, great for service-to-service | Harder to debug (binary), browser support needs grpc-web proxy, steeper learning curve for integrators |
| **REST (JSON)** | Universal, easy to debug (curl), every language has HTTP clients, lowest barrier to adoption | Slower than gRPC (JSON serialization), no streaming, no type safety without OpenAPI codegen |
| **GraphQL** | Flexible queries (agents request exactly what they need), introspection, fits graph data model well | Complexity for a backend service, N+1 query risk, caching harder, overkill for most memory operations |
| **gRPC (primary) + REST (gateway)** | gRPC for in-cluster performance, REST via grpc-gateway for external/debug access | Two interfaces to document, gateway adds a hop for REST clients |

**Leaning toward:** gRPC primary + REST gateway. In-cluster agents get gRPC speed, external users get REST simplicity.

---

### D7: Programming Language for Context0 Engine

| Option | Strengths | Weaknesses |
|--------|-----------|------------|
| **Go** | K8s ecosystem is Go-native (controller-runtime, client-go, Operator SDK), excellent for building operators, fast compilation, single binary, low memory footprint | Less ergonomic for complex graph logic, no generics until recently |
| **Rust** | Maximum performance, memory safety, great for latency-critical paths | Steeper learning curve, slower development velocity, K8s operator ecosystem less mature (kube-rs exists but smaller) |
| **Python** | Fastest to prototype, rich AI/ML ecosystem, good graph DB clients | Too slow for a latency-critical engine, GIL limits concurrency, not ideal for K8s operators |
| **TypeScript/Node** | Fast prototyping, good ecosystem | Not suited for systems-level K8s operator, GC pauses affect latency |
| **Java/Kotlin** | Mature K8s operator frameworks (Java Operator SDK), good graph DB clients (Neo4j driver is Java-native) | JVM memory overhead, cold start times, heavier container images |

**Decision: Go** — it's the native language of the K8s ecosystem. All Go tooling (compiler, standard library, controller-runtime, client-go, Operator SDK) is BSD/Apache licensed.

---

### D8: Observability Stack

| Option | Strengths | Weaknesses |
|--------|-----------|------------|
| **Prometheus + Grafana** | K8s standard, ServiceMonitor CRD, massive dashboard ecosystem, free | Pull-based (needs scrape config), not great for high-cardinality metrics |
| **OpenTelemetry (OTel)** | Vendor-neutral, traces + metrics + logs unified, growing K8s adoption | More setup, needs a backend (Jaeger, Tempo, etc.), still maturing |
| ~~Datadog / New Relic~~ | ~~Turnkey, great UX, K8s integration out of the box~~ | **Excluded — proprietary, not open source** |
| **OTel (collection) + Prometheus (metrics) + Grafana (dashboards)** | Best of both — OTel for traces/logs, Prometheus for metrics, Grafana for visualization | More components to manage |

**Decision: OTel + Prometheus + Grafana** — all open source (Apache 2.0 / AGPLv3). Standard K8s observability stack, fully self-hostable.

---

## Open Questions (Remaining)

- **CRD design** — what's the right granularity for custom resources? One CRD per memory type or a single `MemoryStore` CRD with type field?
- **Multi-cluster** — do we need to support memory federation across clusters from day one, or defer to post-MVP?
- **Pricing model** — per-memory? per-query? per-project? Free tier for open-source, paid for managed?
- **UI** — graph visualization dashboard for browsing/managing memories, or API-only for MVP?
- **LLM for consolidation** — which model? Self-hosted (Llama, Mistral) for privacy, or API (Claude, GPT) for quality?

---

## Target Users

1. **AI developers** building agents with LangChain, CrewAI, AutoGen, etc.
2. **Dev tool builders** who want to add memory to their AI-powered products
3. **Teams running multi-agent workflows** that need shared context
4. **Enterprise** needing self-hosted, privacy-compliant agent memory

---

## Success Metrics

- Retrieval precision: >85% of returned memories are relevant to the query
- Latency: p95 retrieval <100ms, p95 write <200ms
- Adoption: SDKs for top 5 agentic frameworks within 6 months
- Retention: developers keep using it after initial integration (not just a demo)
