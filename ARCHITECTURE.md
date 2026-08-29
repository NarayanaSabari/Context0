# Kora — Architecture

How the Kora memory engine actually works. Every diagram here describes code
that ships and can be checked against the repository.

Designs that are not implemented -- the Kubernetes Operator, multi-scope shared
memory, the production topology -- live in [docs/vision.md](docs/vision.md).
They used to live here behind per-section banners, which failed twice: readers
learned the data model from node types the engine does not have.

---

## 1. System Overview — The Big Picture

How Kora fits into an AI agent ecosystem.

> Everything in this diagram ships. The Operator, sidecar cache and
> ServiceMonitor that used to appear here moved to
> [docs/vision.md](docs/vision.md).

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                          EXTERNAL WORLD                                      │
│                                                                              │
│   ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐         │
│   │  Claude     │  │  LangChain  │  │  CrewAI     │  │  Custom     │         │
│   │  Code       │  │  Agent      │  │  Crew       │  │  Agent      │         │
│   │             │  │             │  │             │  │             │         │
│   └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘         │
│          │                │                │                │                │
│          │    REST API    │    REST API     │   REST API     │               │
│          │  (external)    │  (external)     │  (external)    │               │
└──────────┼────────────────┼────────────────┼────────────────┼────────────────┘
           │                │                │                │
           ▼                ▼                ▼                ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                                                                              │
│    ┌─────────────────────────────────────────────────────────┐               │
│    │              Ingress / API Gateway                       │              │
│    │         (TLS termination, rate limiting)                 │              │
│    └────────────────────────┬────────────────────────────────┘               │
│                             │                                                │
│  ═══════════════════════════╪════════════════════════════════════════════    │
│   KUBERNETES CLUSTER        │                                                │
│  ═══════════════════════════╪════════════════════════════════════════════    │
│                             │                                                │
│    ┌────────────┐     ┌─────▼──────────────────────────────────────────┐     │
│    │ Agent Pod  │     │                                                │     │
│    │ (in-cluster│────▶│             KORA ENGINE                        │     │
│    │  via gRPC) │     │                                                │     │
│    └────────────┘     │   ┌─────────┐  ┌──────────┐  ┌────────────┐    │     │
│                       │   │  API    │  │  Query   │  │  Ingest    │    │     │
│                       │   │  Server │  │  Engine  │  │  Pipeline  │    │     │
│                       │   │         │  │          │  │            │    │     │
│                       │   └────┬────┘  └────┬─────┘  └─────┬──────┘    │     │
│                       │        │             │              │          │     │
│                       │        └─────────────┼──────────────┘          │     │
│                       │                      │                         │     │
│                       │                ┌─────▼──────┐                  │     │
│                       │                │ PostgreSQL │                  │     │
│                       │                │ + AGE      │                  │     │
│                       │                └────────────┘                  │     │
│                       │                                                │     │
│                       └────────────────────────────────────────────────┘     │
│                                                                              │
│                            ┌────────────────┐    ┌─────────────────┐        │
│                            │  Consolidation │    │  Prometheus     │        │
│                            │  CronJob       │    │  /metrics       │        │
│                            │  (sleep cycle) │    │  endpoint       │        │
│                            └────────────────┘    └─────────────────┘        │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Component Architecture — Inside the Engine

Every component, what it does, and how they connect.

```
┌──────────────────────────────────────────────────────────────────────────┐
│                        KORA ENGINE                                   │
│                                                                          │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │                      API LAYER                                     │  │
│  │                                                                    │  │
│  │  ┌──────────────────┐          ┌──────────────────┐                │  │
│  │  │   gRPC Server    │          │   REST Gateway   │                │  │
│  │  │                  │          │  (grpc-gateway)   │               │  │
│  │  │  • Store()       │◀────────▶│                   │               │  │
│  │  │  • Retrieve()    │          │  • POST /memories │               │  │
│  │  │  • Delete()      │          │  • GET  /query    │               │  │
│  │  │  • Query()       │          │  • GET  /graph    │               │  │
│  │  │  • Connect()     │          │  • DELETE /memories│              │  │
│  │  │  • StreamWatch() │          │  • GET  /health   │               │  │
│  │  └────────┬─────────┘          └──────────────────┘                │  │
│  │           │                                                        │  │
│  └───────────┼────────────────────────────────────────────────────────┘  │
│              │                                                           │
│  ┌───────────▼────────────────────────────────────────────────────────┐  │
│  │                    CORE SERVICES                                   │  │
│  │                                                                    │  │
│  │  ┌─────────────────┐  ┌─────────────────┐  ┌───────────────────┐   │  │
│  │  │  Ingest Service │  │  Query Service  │  │  Graph Service    │   │  │
│  │  │                 │  │                 │  │                   │   │  │
│  │  │ • Validate      │  │ • Parse query   │  │ • Traverse        │   │  │
│  │  │ • Classify type │  │ • Plan traversal│  │ • Subgraph extract│   │  │
│  │  │   (episodic/    │  │ • Execute graph │  │ • Path finding    │   │  │
│  │  │    semantic/    │  │   query         │  │ • Neighborhood    │   │  │
│  │  │    procedural)  │  │ • Rank results  │  │   expansion       │   │  │
│  │  │ • Extract       │  │ • Apply decay   │  │ • Edge weighting  │   │  │
│  │  │   relationships │  │   + recency     │  │                   │   │  │
│  │  │ • Generate      │  │ • Return top-K  │  │                   │   │  │
│  │  │   embeddings    │  │                 │  │                   │   │  │
│  │  │   (optional)    │  │                 │  │                   │   │  │
│  │  └────────┬────────┘  └────────┬────────┘  └─────────┬─────────┘   │  │
│  │           │                    │                      │            │  │
│  └───────────┼────────────────────┼──────────────────────┼────────────┘  │
│              │                    │                      │               │
│  ┌───────────▼────────────────────▼──────────────────────▼────────────┐  │
│  │                    MEMORY LAYER                                    │  │
│  │                                                                    │  │
│  │  ┌──────────────────────────────────────────────────────────────┐  │  │
│  │  │                    Graph Repository                          │  │  │
│  │  │                                                              │  │  │
│  │  │  Abstracts graph DB operations behind a clean interface:     │  │  │
│  │  │  • CreateNode(type, properties)                              │  │  │
│  │  │  • CreateEdge(from, to, relationship, properties)            │  │  │
│  │  │  • TraverseFrom(nodeId, depth, edgeFilters)                  │  │  │
│  │  │  • FindByProperties(filters)                                 │  │  │
│  │  │  • VectorSearch(embedding, topK, neighborhood)               │  │  │
│  │  │  • DeleteNode(nodeId, cascade)                               │  │  │
│  │  │  • UpdateProperties(nodeId, properties)                      │  │  │
│  │  └──────────────────────────┬───────────────────────────────────┘  │  │
│  │                             │                                      │  │
│  └─────────────────────────────┼──────────────────────────────────────┘  │
│                                │                                         │
│                                ▼                                         │
│  ┌──────────────────────────────────────────────────────────────────┐    │
│  │                     GRAPH DATABASE                               │    │
│  │                  (StatefulSet + PersistentVolume)                │    │
│  └──────────────────────────────────────────────────────────────────┘    │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Graph Data Model — How Memory is Structured

Every label and edge type below is created by `internal/graph`. Nothing here is
aspirational; the earlier version of this section drew Tenant, User, Preference,
Decision, Correction and Outcome nodes, none of which exist, and it was the
first thing a contributor read to learn the model. Those diagrams now live in
[docs/vision.md](docs/vision.md).

```
                       ┌────────────────────┐
                       │      PROJECT       │      created on demand; the scope
                       │  id: "backend-api" │      every memory belongs to
                       └─────────┬──────────┘
                                 │ belongs_to
                       ┌─────────┴──────────┐
                       │      SESSION       │      one agent's run within a
                       │  id, agent_id,     │      project
                       │  started/ended_at  │
                       └─────────┬──────────┘
                                 │ contains
                                 ▼
   ┌───────────────────────────────────────────────────────────┐
   │                         MEMORY                            │
   │  id, content, content_hash, project_id, tags,             │
   │  created_at, access_count, decay_score                    │
   │                                                           │
   │  type: semantic | episodic | procedural                   │
   └───────┬───────────────────────────┬───────────────────────┘
           │ mentions                  │ relates_to / supersedes / caused_by
           ▼                           ▼
   ┌────────────────┐          ┌───────────────────┐
   │     ENTITY     │          │      MEMORY       │
   │ name,          │          │  (another one)    │
   │ normalized_name│          └───────────────────┘
   │ project_id     │
   └────────────────┘
```

**Labels:** `Project`, `Session`, `Memory`, `Entity`. Memory type is a property,
not a label: one table, filtered by `type`.

**Edge types** (`pkg/model/edge.go`, and `Valid()` is what enforces the list):

| Edge | From → To | Meaning |
|---|---|---|
| `belongs_to` | Session → Project | which project a session ran in |
| `contains` | Session → Memory | a memory produced during that session |
| `relates_to` | Memory → Memory | general association, from shared tags or embedding proximity |
| `supersedes` | Memory → Memory | the newer memory replaces the older; the older is kept and marked |
| `caused_by` | Memory → Memory | the source memory is a consequence of the target |
| `mentions` | Memory → Entity | what the memory is about, and the only multi-hop path between memories that share no words |

**Vectors live outside the graph.** Embeddings are a plain `memory_embeddings`
table with a pgvector column and an HNSW index, joined back by memory id. AGE
stores every property inside one `agtype` column, which a vector index cannot
serve, so the hybrid query is a graph traversal and a vector search over the
same Postgres rather than one query language doing both.

---

## 4. Request Flow — Write Path

What happens when an agent stores a memory.

```
  Agent                    API Server          Ingest Service        PG + AGE
    │                          │                     │                  │
    │  Store(memory)           │                     │                  │
    │─────────────────────────▶│                     │                  │
    │                          │                     │                  │
    │                          │  Validate + Auth    │                  │
    │                          │─────────┐           │                  │
    │                          │◀────────┘           │                  │
    │                          │                     │                  │
    │                          │  Ingest(memory)     │                  │
    │                          │────────────────────▶│                  │
    │                          │                     │                  │
    │                          │                     │  1. Classify     │
    │                          │                     │     memory type  │
    │                          │                     │     (episodic/   │
    │                          │                     │      semantic/   │
    │                          │                     │      procedural) │
    │                          │                     │                  │
    │                          │                     │  2. Extract      │
    │                          │                     │     entities &   │
    │                          │                     │     relationships│
    │                          │                     │                  │
    │                          │                     │  3. Generate     │
    │                          │                     │     embedding    │
    │                          │                     │     (optional)   │
    │                          │                     │                  │
    │                          │                     │  4. Find related │
    │                          │                     │     existing     │
    │                          │                     │     nodes        │
    │                          │                     │─────────────────▶│
    │                          │                     │◀─────────────────│
    │                          │                     │                  │
    │                          │                     │  5. Check for    │
    │                          │                     │     supersedes/  │
    │                          │                     │     contradicts  │
    │                          │                     │                  │
    │                          │                     │  6. Create node  │
    │                          │                     │     + edges      │
    │                          │                     │─────────────────▶│
    │                          │                     │◀─────────────────│
    │                          │                     │                  │
    │                          │  { nodeId, edges }  │                  │
    │                          │◀────────────────────│                  │
    │                          │                     │                  │
    │  { ok, memoryId }        │                     │                  │
    │◀─────────────────────────│                     │                  │
    │                          │                     │                  │
```

---

## 5. Request Flow — Read Path (Graph Traversal)

What happens when an agent queries for memories.

> **Ranking, as implemented.** Retrieval runs two strategies in parallel -- graph
> keyword/tag matching and pgvector similarity -- and merges them. Each candidate
> carries a `relevance` score in [0, 1]: cosine similarity for vector hits, the
> fraction of query keywords matched for graph hits, combined with a bounded
> boost when both strategies find the same memory.
>
> The final score is a weighted sum of four normalized signals:
>
> ```
> score = 0.55 × relevance     # does it answer the query
>       + 0.25 × recency       # exp decay, 7-day half-life
>       + 0.10 × frequency     # log(access_count), saturating
>       + 0.10 × type_priority # semantic 1.0 > procedural 0.9 > episodic 0.6
> ```
>
> The weights sum to 1.0, so scores are in [0, 1] and comparable across queries.
> Relevance dominates deliberately: recency, frequency, and type only separate
> memories that already answer the query. Ties break by memory ID, so identical
> queries return identical orderings. See `internal/ranking`.
>
> `decay_score` and edge weight are stored and shown in results, but do not
> currently feed the retrieval score; decay drives the consolidation pipeline
> in §6 instead.

```
  Agent                  API Server         Query Service        Graph Service       PG + AGE
    │                       │                    │                    │                 │
    │  Query("what DB       │                    │                    │                 │
    │   does this project   │                    │                    │                 │
    │   use?")              │                    │                    │                 │
    │──────────────────────▶│                    │                    │                 │
    │                       │                    │                    │                 │
    │                       │  Plan query        │                    │                 │
    │                       │───────────────────▶│                    │                 │
    │                       │                    │                    │                 │
    │                       │                    │  1. Identify       │                 │
    │                       │                    │     entry points   │                 │
    │                       │                    │     ("DB",         │                 │
    │                       │                    │     "project",     │                 │
    │                       │                    │     "database")    │                 │
    │                       │                    │                    │                 │
    │                       │                    │  2. Build          │                 │
    │                       │                    │     traversal plan │                 │
    │                       │                    │                    │                 │
    │                       │                    │  Traverse(plan)    │                 │
    │                       │                    │───────────────────▶│                 │
    │                       │                    │                    │                 │
    │                       │                    │                    │  MATCH (p:Project│
    │                       │                    │                    │  {id: $project}) │
    │                       │                    │                    │  -[*1..3]->(m)   │
    │                       │                    │                    │  WHERE m:Semantic │
    │                       │                    │                    │  OR m:Decision    │
    │                       │                    │                    │────────────────▶│
    │                       │                    │                    │◀────────────────│
    │                       │                    │                    │                 │
    │                       │                    │                    │  Extract         │
    │                       │                    │                    │  subgraph        │
    │                       │                    │                    │  (nodes + edges) │
    │                       │                    │                    │                 │
    │                       │                    │  Raw subgraph      │                 │
    │                       │                    │◀───────────────────│                 │
    │                       │                    │                    │                 │
    │                       │                    │  3. Rank results   │                 │
    │                       │                    │     • relevance    │                 │
    │                       │                    │     • recency      │                 │
    │                       │                    │     • access_count │                 │
    │                       │                    │     • type         │                 │
    │                       │                    │                    │                 │
    │                       │                    │  4. Top-K results  │                 │
    │                       │                    │     with context   │                 │
    │                       │                    │     edges          │                 │
    │                       │                    │                    │                 │
    │                       │  Ranked memories   │                    │                 │
    │                       │◀───────────────────│                    │                 │
    │                       │                    │                    │                 │
    │  [                    │                    │                    │                 │
    │    {                  │                    │                    │                 │
    │      "content":       │                    │                    │                 │
    │        "uses Postgres │                    │                    │                 │
    │         15.x",        │                    │                    │                 │
    │      "type":"semantic"│                    │                    │                 │
    │      "confidence":0.95│                    │                    │                 │
    │      "context": [     │                    │                    │                 │
    │        { "edge":      │                    │                    │                 │
    │          "supersedes", │                    │                    │                 │
    │          "target":    │                    │                    │                 │
    │          "uses MySQL" │                    │                    │                 │
    │        }              │                    │                    │                 │
    │      ]                │                    │                    │                 │
    │    }                  │                    │                    │                 │
    │  ]                    │                    │                    │                 │
    │◀──────────────────────│                    │                    │                 │
    │                       │                    │                    │                 │
```

---

## 6. Consolidation Pipeline — The "Sleep" Cycle

Background process that keeps the memory graph clean and efficient.

```
┌──────────────────────────────────────────────────────────────────────────┐
│                   CONSOLIDATION PIPELINE (CronJob)                       │
│                                                                          │
│   Triggered by: K8s CronJob (every 6h) OR event hook (session end)       │
│                                                                          │
│   ┌──────────────────────────────────────────────────────────────────┐   │
│   │  PHASE 1: SCAN                                                   │   │
│   │                                                                  │   │
│   │  • Find episodic memories with high similarity                   │   │
│   │  • Find semantic nodes with low confidence + low access_count    │   │
│   │  • Find contradicting facts (contradicts edges)                  │   │
│   │  • Find orphan nodes (no incoming edges, old)                    │   │
│   └──────────────────────────┬───────────────────────────────────────┘   │
│                              ▼                                           │
│   ┌──────────────────────────────────────────────────────────────────┐   │
│   │  PHASE 2: MERGE                                                  │   │
│   │                                                                  │   │
│   │  Multiple episodic memories about the same topic:                │   │
│   │                                                                  │   │
│   │  ┌──────────┐ ┌──────────┐ ┌──────────┐                          │   │
│   │  │"discussed│ │"changed  │ │"finalized│                          │   │
│   │  │ DB choice│ │ DB to    │ │ Postgres │                          │   │
│   │  │ in standup│ │ Postgres"│ │ 15.x"   │                          │   │
│   │  └────┬─────┘ └────┬─────┘ └────┬─────┘                          │   │
│   │       │             │            │                               │   │
│   │       └─────────────┼────────────┘                               │   │
│   │                     │  merged_into                               │   │
│   │                     ▼                                            │   │
│   │              ┌──────────────┐                                    │   │
│   │              │   SEMANTIC   │                                    │   │
│   │              │  "project    │                                    │   │
│   │              │   uses       │                                    │   │
│   │              │   Postgres   │                                    │   │
│   │              │   15.x"      │                                    │   │
│   │              │  conf: 0.95  │                                    │   │
│   │              └──────────────┘                                    │   │
│   └──────────────────────────┬───────────────────────────────────────┘   │
│                              ▼                                           │
│   ┌──────────────────────────────────────────────────────────────────┐   │
│   │  PHASE 3: PROMOTE                                                │   │
│   │                                                                  │   │
│   │  Repeated corrections/patterns elevated to procedural memory:    │   │
│   │                                                                  │   │
│   │  ┌──────────┐ ┌──────────┐ ┌──────────┐                          │   │
│   │  │"user said│ │"user said│ │"user said│                          │   │
│   │  │ don't    │ │ use real │ │ no mocks │   (3 similar             │   │
│   │  │ mock DB" │ │ DB in    │ │ for DB"  │    corrections)          │   │
│   │  │ Session 1│ │ tests"   │ │ Session 5│                          │   │
│   │  └────┬─────┘ │ Session 3│ └────┬─────┘                          │   │
│   │       │       └────┬─────┘      │                                │   │
│   │       └────────────┼────────────┘                                │   │
│   │                    │  promoted_to                                │   │
│   │                    ▼                                             │   │
│   │             ┌──────────────┐                                     │   │
│   │             │  PROCEDURAL  │                                     │   │
│   │             │ "always use  │                                     │   │
│   │             │  real DB in  │                                     │   │
│   │             │  integration │                                     │   │
│   │             │  tests"      │                                     │   │
│   │             │ success: 100%│                                     │   │
│   │             └──────────────┘                                     │   │
│   └──────────────────────────┬───────────────────────────────────────┘   │
│                              ▼                                           │
│   ┌──────────────────────────────────────────────────────────────────┐   │
│   │  PHASE 4: DECAY + PRUNE                                          │   │
│   │                                                                  │   │
│   │  For each node, recalculate decay_score:                         │   │
│   │                                                                  │   │
│   │  decay_score = base_importance                                   │   │
│   │              × recency_factor(last_accessed)                     │   │
│   │              × frequency_boost(access_count)                     │   │
│   │              × confidence                                        │   │
│   │                                                                  │   │
│   │  If decay_score < threshold:                                     │   │
│   │    → Mark as [stale]                                             │   │
│   │    → Move to cold storage after 30 days                          │   │
│   │    → Delete after 90 days (configurable via MemoryPolicy CRD)    │   │
│   │                                                                  │   │
│   │  Orphan nodes (no edges, never accessed):                        │   │
│   │    → Delete immediately                                          │   │
│   │                                                                  │   │
│   └──────────────────────────────────────────────────────────────────┘   │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 7. SDK Interface — How Agents Interact

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     AGENT SDK (any language)                              │
│                                                                          │
│  // Initialize                                                           │
│  client = KoraClient(                                                │
│      endpoint = "kora.kora-system.svc.cluster.local:50051"       │
│      project  = "backend-api"                                            │
│      agent_id = "claude-code-01"                                         │
│  )                                                                       │
│                                                                          │
│  // ─── STORE ───────────────────────────────────────────────────────    │
│  // Store a memory (auto-classified, auto-linked)                        │
│  client.store(                                                           │
│      content = "Project uses PostgreSQL 15.x with pgvector extension"    │
│      type    = SEMANTIC              // or EPISODIC, PROCEDURAL          │
│      scope   = PROJECT               // or AGENT, GLOBAL                 │
│      tags    = ["database", "postgres"]                                   │
│  )                                                                       │
│                                                                          │
│  // ─── RETRIEVE ────────────────────────────────────────────────────    │
│  // Query with natural language (graph traversal under the hood)         │
│  memories = client.query(                                                │
│      question  = "what database does this project use?"                   │
│      max_depth = 3                   // max graph traversal depth        │
│      top_k     = 5                   // return top 5 results             │
│      types     = [SEMANTIC, DECISION] // filter by memory type           │
│  )                                                                       │
│                                                                          │
│  // Returns:                                                             │
│  // [                                                                    │
│  //   Memory(                                                            │
│  //     content: "Project uses PostgreSQL 15.x",                         │
│  //     type: SEMANTIC,                                                  │
│  //     confidence: 0.95,                                                │
│  //     context: [                                                       │
│  //       Edge(rel: "supersedes", target: "Project uses MySQL"),          │
│  //       Edge(rel: "caused_by",  target: "Need vector search support")  │
│  //     ]                                                                │
│  //   )                                                                  │
│  // ]                                                                    │
│                                                                          │
│  // ─── CONNECT ─────────────────────────────────────────────────────    │
│  // Explicitly create a relationship between memories                    │
│  client.connect(                                                         │
│      from_id      = "mem_abc123"                                         │
│      to_id        = "mem_def456"                                         │
│      relationship = "caused_by"                                          │
│      weight       = 0.9                                                  │
│  )                                                                       │
│                                                                          │
│  // ─── GRAPH ───────────────────────────────────────────────────────    │
│  // Get a subgraph visualization around a memory                         │
│  subgraph = client.graph(                                                │
│      center_id = "mem_abc123"                                            │
│      depth     = 2                                                       │
│  )                                                                       │
│  // Returns nodes + edges for rendering                                  │
│                                                                          │
│  // ─── SESSION LIFECYCLE ───────────────────────────────────────────    │
│  session = client.start_session()    // begins episodic tracking         │
│  // ... agent does work ...                                              │
│  session.end()                       // triggers event-based consolidation│
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Summary — How Everything Connects

```
                    ┌─────────────────────────────────┐
                    │          AGENTS                   │
                    │  (any framework, any language)    │
                    └──────────────┬──────────────────┘
                                   │
                          SDK / gRPC / REST
                                   │
                    ┌──────────────▼──────────────────┐
                    │         API LAYER                 │
                    │    Auth + Rate Limiting           │
                    └──────────────┬──────────────────┘
                                   │
                 ┌─────────────────┼─────────────────┐
                 │                 │                  │
          ┌──────▼──────┐  ┌──────▼──────┐  ┌───────▼──────┐
          │   INGEST    │  │   QUERY     │  │   GRAPH      │
          │   SERVICE   │  │   SERVICE   │  │   SERVICE    │
          │             │  │             │  │              │
          │ classify    │  │ plan        │  │ traverse     │
          │ extract     │  │ traverse    │  │ subgraph     │
          │ embed       │  │ rank        │  │ connect      │
          │ link        │  │ return      │  │ visualize    │
          └──────┬──────┘  └──────┬──────┘  └───────┬──────┘
                 │                │                  │
                 └────────────────┼──────────────────┘
                                  │
                    ┌─────────────▼──────────────────┐
                    │      GRAPH REPOSITORY            │
                    │   (abstraction over graph DB)    │
                    └─────────────┬──────────────────┘
                                  │
                    ┌─────────────▼──────────────────┐
                    │        GRAPH DATABASE            │
                    │  (CloudNativePG + Apache AGE)    │
                    │                                  │
                    │   Nodes: memories, facts,        │
                    │          decisions, sessions      │
                    │   Edges: caused_by, supersedes,  │
                    │          validates, relates_to    │
                    └──────────────────────────────────┘
                                  ▲
                                  │
                    ┌─────────────┴──────────────────┐
                    │     CONSOLIDATION PIPELINE       │
                    │        (K8s CronJob)             │
                    │                                  │
                    │   scan → merge → promote →       │
                    │   decay → prune                  │
                    └──────────────────────────────────┘

      All deployed by: HELM (charts/kora)
      All observed by: PROMETHEUS (/metrics)
```
