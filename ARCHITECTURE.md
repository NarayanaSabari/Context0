# Context0 — Architecture

Detailed architecture diagrams for the Context0 memory engine.

---

## 1. System Overview — The Big Picture

How Context0 fits into an AI agent ecosystem.

> **Partly aspirational.** The engine, PostgreSQL + AGE, the consolidation
> CronJob, and the Prometheus `/metrics` endpoint all ship today. The Context0
> Operator, the agent-pod sidecar cache, and the Prometheus ServiceMonitor in
> the diagram below do not exist yet — see §7 and §9.

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
│    │ (in-cluster│────▶│             CONTEXT0 ENGINE                    │     │
│    │  via gRPC) │     │                                                │     │
│    └────────────┘     │   ┌─────────┐  ┌──────────┐  ┌────────────┐    │     │
│                       │   │  API    │  │  Query   │  │  Ingest    │    │     │
│    ┌────────────┐     │   │  Server │  │  Engine  │  │  Pipeline  │    │     │
│    │ Agent Pod  │────▶│   │         │  │          │  │            │    │     │
│    │ (sidecar   │     │   └────┬────┘  └────┬─────┘  └─────┬──────┘    │     │
│    │  cache)    │     │        │             │              │          │     │
│    └────────────┘     │        └─────────────┼──────────────┘          │     │
│                       │                      │                         │     │
│                       │                ┌─────▼──────┐                  │     │
│                       │                │ PostgreSQL │                  │     │
│                       │                │ + AGE      │                  │     │
│                       │                └────────────┘                  │     │
│                       │                                                │     │
│                       └────────────────────────────────────────────────┘     │
│                                                                              │
│    ┌──────────────────┐    ┌────────────────┐    ┌─────────────────┐         │
│    │ Context0 Operator│    │  Consolidation │    │  Prometheus     │         │
│    │ (manages CRDs,   │    │  CronJob       │    │  ServiceMonitor │         │
│    │  lifecycle)      │    │  (sleep cycle) │    │  + Grafana      │         │
│    └──────────────────┘    └────────────────┘    └─────────────────┘         │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Component Architecture — Inside the Engine

Every component, what it does, and how they connect.

```
┌──────────────────────────────────────────────────────────────────────────┐
│                        CONTEXT0 ENGINE                                   │
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

The node types, edge types, and their relationships in the memory graph.

```
                            ┌─────────────────────┐
                            │      TENANT         │
                            │                     │
                            │  id: "acme-corp"    │
                            │  plan: "pro"        │
                            └──────────┬──────────┘
                                       │ owns
                          ┌────────────┼────────────┐
                          ▼            ▼            ▼
                   ┌────────────┐ ┌────────────┐ ┌────────────┐
                   │  PROJECT   │ │  PROJECT   │ │   USER     │
                   │            │ │            │ │            │
                   │ "backend"  │ │ "frontend" │ │ "sabari"   │
                   └─────┬──────┘ └────────────┘ └──────┬─────┘
                         │                              │
            ┌────────────┼───────────────┐              │ prefers
            │            │               │              ▼
            ▼            ▼               ▼      ┌──────────────┐
     ┌────────────┐ ┌──────────┐ ┌────────────┐ │  PREFERENCE  │
     │  SESSION   │ │  FACT    │ │  PATTERN   │ │              │
     │ (episodic) │ │(semantic)│ │(procedural)│ │ "uses vim"   │
     │            │ │          │ │            │ │ "concise     │
     │ 2024-03-28 │ │ "uses    │ │ "always    │ │  responses"  │
     │ 14:00-16:00│ │  Postgres│ │  run tests │ └──────────────┘
     └──┬───┬─────┘ │  15.x"   │ │  before    │
        │   │       └────┬─────┘ │  commit"   │
        │   │            │       └────────────┘
        │   │            │
        │   │            │ supersedes
        │   │            ▼
        │   │     ┌──────────────┐
        │   │     │    FACT      │
        │   │     │  (archived)  │
        │   │     │              │
        │   │     │ "uses MySQL" │
        │   │     │ [stale]      │
        │   │     └──────────────┘
        │   │
        │   │ contains
        │   ▼
        │  ┌──────────────────┐    caused_by     ┌──────────────────┐
        │  │    DECISION      │─────────────────▶│   CONSTRAINT     │
        │  │                  │                  │                  │
        │  │ "chose Next.js   │                  │ "team knows      │
        │  │  for frontend"   │                  │  React already"  │
        │  └──────────────────┘                  └──────────────────┘
        │
        │ contains
        ▼
     ┌──────────────────┐   validated_by    ┌──────────────────┐
     │   CORRECTION     │──────────────────▶│    OUTCOME       │
     │                  │                   │                  │
     │ "don't mock DB   │                   │ "tests caught    │
     │  in integration  │                   │  migration bug   │
     │  tests"          │                   │  in staging"     │
     └──────────────────┘                   └──────────────────┘
```

### Node Types

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          NODE TYPES                                     │
│                                                                         │
│  ┌─────────────┐  Structural nodes — define scope and ownership         │
│  │  Tenant     │  Properties: id, name, plan, created_at                │
│  │  Project    │  Properties: id, name, tenant_id, created_at           │
│  │  User       │  Properties: id, name, role, created_at                │
│  │  Agent      │  Properties: id, framework, version, created_at        │
│  │  Session    │  Properties: id, started_at, ended_at, agent_id        │
│  └─────────────┘                                                        │
│                                                                         │
│  ┌─────────────┐  Memory nodes — the actual memories                    │
│  │  Episodic   │  What happened. Properties: content, timestamp,        │
│  │             │  session_id, confidence, access_count, decay_score     │
│  │  Semantic   │  What is true. Properties: content, confidence,        │
│  │             │  source, last_validated, access_count, decay_score     │
│  │  Procedural │  How to do things. Properties: content, trigger,       │
│  │             │  success_rate, times_applied, decay_score              │
│  │  Preference │  What the user/agent prefers. Properties: content,     │
│  │             │  strength, context, last_confirmed                     │
│  └─────────────┘                                                        │
│                                                                         │
│  ┌─────────────┐  Meta nodes — decisions, corrections, outcomes         │
│  │  Decision   │  Properties: content, rationale, decided_at, decided_by│
│  │  Correction │  Properties: wrong_behavior, right_behavior, severity  │
│  │  Constraint │  Properties: content, source, active                   │
│  │  Outcome    │  Properties: content, success, measured_at             │
│  └─────────────┘                                                        │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Edge Types

```
┌──────────────────────────────────────────────────────────────────────────┐
│                          EDGE TYPES                                      │
│                                                                          │
│  Structural Edges:                                                       │
│  ─────────────────                                                       │
│  owns            Tenant ──▶ Project, User                                │
│  belongs_to      Session ──▶ Project                                     │
│  performed_by    Session ──▶ Agent                                       │
│  participated     User ──▶ Session                                       │
│                                                                          │
│  Temporal Edges:                                                         │
│  ───────────────                                                         │
│  contains        Session ──▶ Episodic (memory created during session)    │
│  followed_by     Session ──▶ Session (session ordering)                  │
│  happened_before Episodic ──▶ Episodic (event ordering)                  │
│                                                                          │
│  Causal Edges:                                                           │
│  ─────────────                                                           │
│  caused_by       Decision ──▶ Constraint (why a decision was made)       │
│  led_to          Decision ──▶ Outcome (what resulted from a decision)    │
│  triggered_by    Correction ──▶ Episodic (what event caused correction)  │
│  validated_by    Pattern ──▶ Outcome (proof that a pattern works)        │
│                                                                          │
│  Knowledge Edges:                                                        │
│  ────────────────                                                        │
│  supersedes      Semantic ──▶ Semantic (newer fact replaces older)       │
│  contradicts     Semantic ──▶ Semantic (conflicting facts)               │
│  relates_to      Any ──▶ Any (general association)                       │
│  derived_from    Semantic ──▶ Episodic (fact extracted from event)       │
│  generalizes     Procedural ──▶ Episodic[] (pattern from episodes)       │
│                                                                          │
│  Consolidation Edges:                                                    │
│  ─────────────────                                                       │
│  merged_into     Episodic[] ──▶ Semantic (consolidation output)          │
│  promoted_to     Episodic ──▶ Procedural (pattern promotion)             │
│  archived_by     Semantic ──▶ Semantic (old fact replaced)               │
│                                                                          │
│  All edges carry:                                                        │
│  • weight (float)     — strength of relationship                         │
│  • created_at (time)  — when the relationship was established            │
│  • access_count (int) — how often this edge is traversed                 │
│  • confidence (float) — how certain we are about this relationship       │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

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

## 7. Kubernetes Resource Model — CRDs and Operator

> **Status: design sketch, not implemented.**
> No CRDs and no operator exist in this repo today.
> Deployment is via the Helm chart in `charts/context0/`.
> Everything below describes intended future shape, not current behaviour.

How Context0 is managed as Kubernetes-native resources.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                   CUSTOM RESOURCE DEFINITIONS                            │
│                                                                          │
│  apiVersion: context0.io/v1alpha1                                        │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  Kind: MemoryStore                                                 │  │
│  │                                                                    │  │
│  │  The core resource. One per tenant/project.                        │  │
│  │                                                                    │  │
│  │  spec:                                                             │  │
│  │    graphDB:                                                        │  │
│  │      engine: apache-age          # fully open source (Apache 2.0)                         │  │
│  │      replicas: 3                                                   │  │
│  │      storage: 50Gi                                                 │  │
│  │      resources:                                                    │  │
│  │        memory: 4Gi                                                 │  │
│  │        cpu: 2                                                      │  │
│  │    api:                                                            │  │
│  │      replicas: 2                                                   │  │
│  │      grpc: true                                                    │  │
│  │      rest: true                                                    │  │
│  │    embedding:                                                      │  │
│  │      enabled: true                                                 │  │
│  │      model: "bge-small-en-v1.5"                                    │  │
│  │                                                                    │  │
│  │  status:                                                           │  │
│  │    phase: Running                                                  │  │
│  │    nodeCount: 12,847                                               │  │
│  │    edgeCount: 45,231                                               │  │
│  │    lastConsolidation: "2024-03-28T06:00:00Z"                       │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  Kind: MemoryPolicy                                                │  │
│  │                                                                    │  │
│  │  Retention, access, and isolation rules.                           │  │
│  │                                                                    │  │
│  │  spec:                                                             │  │
│  │    retention:                                                      │  │
│  │      episodic: 90d          # auto-archive after 90 days          │  │
│  │      stale: 30d             # delete stale nodes after 30 days    │  │
│  │      orphan: 7d             # delete orphans after 7 days         │  │
│  │    isolation:                                                      │  │
│  │      level: project         # project | user | agent              │  │
│  │      networkPolicy: true    # enforce K8s NetworkPolicy           │  │
│  │    access:                                                         │  │
│  │      maxTraversalDepth: 5                                          │  │
│  │      maxResultsPerQuery: 20                                        │  │
│  │      rateLimitPerMinute: 100                                       │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  Kind: ConsolidationSchedule                                       │  │
│  │                                                                    │  │
│  │  Controls the "sleep" cycle — when and how memory is consolidated. │  │
│  │                                                                    │  │
│  │  spec:                                                             │  │
│  │    schedule: "0 */6 * * *"  # every 6 hours                       │  │
│  │    phases:                                                         │  │
│  │      merge:                                                        │  │
│  │        enabled: true                                               │  │
│  │        similarityThreshold: 0.85                                   │  │
│  │      promote:                                                      │  │
│  │        enabled: true                                               │  │
│  │        minOccurrences: 3    # promote after 3 similar episodes    │  │
│  │      decay:                                                        │  │
│  │        enabled: true                                               │  │
│  │        staleThreshold: 0.2                                         │  │
│  │        halfLifeDays: 30     # decay halves every 30 days          │  │
│  │      prune:                                                        │  │
│  │        enabled: true                                               │  │
│  │        dryRun: false                                               │  │
│  │    llm:                                                            │  │
│  │      provider: anthropic                                           │  │
│  │      model: claude-haiku-4-5                                       │  │
│  │      budgetPerRun: "$0.50"                                         │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘


┌─────────────────────────────────────────────────────────────────────────┐
│                   CONTEXT0 OPERATOR — Reconciliation Loop                │
│                                                                          │
│      ┌──────────────┐                                                    │
│      │  Watch CRDs  │◀─────────────────────────────────────┐            │
│      └──────┬───────┘                                      │            │
│             │                                               │            │
│             ▼                                               │            │
│      ┌──────────────┐     ┌───────────────┐     ┌─────────┴──────┐     │
│      │ MemoryStore  │────▶│ Deploy/Scale  │────▶│ Update Status  │     │
│      │ changed?     │     │ • PG + AGE    │     │ • phase        │     │
│      │              │     │   StatefulSet │     │ • nodeCount    │     │
│      │              │     │ • API Server  │     │ • health       │     │
│      │              │     │   Deployment  │     │                │     │
│      └──────────────┘     │ • PVCs        │     └────────────────┘     │
│                           │ • Services    │                             │
│      ┌──────────────┐     └───────────────┘                             │
│      │ MemoryPolicy │────▶ Apply NetworkPolicy, update rate limits      │
│      │ changed?     │                                                    │
│      └──────────────┘                                                    │
│                                                                          │
│      ┌──────────────┐                                                    │
│      │ Consolidation│────▶ Create/Update CronJob with schedule          │
│      │ Schedule     │                                                    │
│      │ changed?     │                                                    │
│      └──────────────┘                                                    │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 8. Multi-Agent Shared Memory — Scoping Model

How multiple agents share and isolate memory within the graph.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        MEMORY SCOPING                                    │
│                                                                          │
│  Three levels of memory visibility:                                      │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │                                                                    │  │
│  │  GLOBAL SCOPE (shared across all projects in a tenant)             │  │
│  │  ┌─────────────────────────────────────────────────────────────┐  │  │
│  │  │  • User preferences ("prefers TypeScript", "concise replies") │  │  │
│  │  │  • Organization standards ("use conventional commits")       │  │  │
│  │  │  • Cross-project patterns                                    │  │  │
│  │  └─────────────────────────────────────────────────────────────┘  │  │
│  │                                                                    │  │
│  │  ┌──────────────────────────┐  ┌──────────────────────────┐      │  │
│  │  │  PROJECT SCOPE           │  │  PROJECT SCOPE            │      │  │
│  │  │  "backend-api"           │  │  "mobile-app"             │      │  │
│  │  │                          │  │                           │      │  │
│  │  │  • Project facts         │  │  • Project facts          │      │  │
│  │  │  • Architecture decisions│  │  • Architecture decisions │      │  │
│  │  │  • Team patterns         │  │  • Team patterns          │      │  │
│  │  │                          │  │                           │      │  │
│  │  │  ┌──────────┐ ┌────────┐│  │  ┌──────────┐ ┌────────┐│      │  │
│  │  │  │ AGENT    │ │ AGENT  ││  │  │ AGENT    │ │ AGENT  ││      │  │
│  │  │  │ SCOPE    │ │ SCOPE  ││  │  │ SCOPE    │ │ SCOPE  ││      │  │
│  │  │  │ Claude   │ │ Cursor ││  │  │ CrewAI   │ │ Claude ││      │  │
│  │  │  │ Code     │ │        ││  │  │ worker   │ │ Code   ││      │  │
│  │  │  │          │ │        ││  │  │          │ │        ││      │  │
│  │  │  │• Session │ │• Session││  │  │• Session │ │• Session││      │  │
│  │  │  │  history │ │  history││  │  │  history │ │  history││      │  │
│  │  │  │• Agent-  │ │• Agent- ││  │  │• Agent-  │ │• Agent- ││      │  │
│  │  │  │  specific│ │  specific││  │  │  specific│ │  specific││      │  │
│  │  │  │  context │ │  context││  │  │  context │ │  context││      │  │
│  │  │  └──────────┘ └────────┘│  │  └──────────┘ └────────┘│      │  │
│  │  │                          │  │                           │      │  │
│  │  └──────────────────────────┘  └──────────────────────────┘      │  │
│  │                                                                    │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  Query resolution order:                                                 │
│  1. Agent scope   → check agent's own memories first                     │
│  2. Project scope → then project-wide shared memories                    │
│  3. Global scope  → finally tenant-wide memories                         │
│                                                                          │
│  Write visibility:                                                       │
│  • Agent writes default to PROJECT scope (shared)                        │
│  • Agent can explicitly write to AGENT scope (private)                   │
│  • Only admins/operators can write to GLOBAL scope                       │
│  • Consolidation can promote agent → project → global                    │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 9. Deployment Topology — Production Setup

> **Status: target topology, not implemented.**
> The shipped chart runs a single-replica Postgres StatefulSet, one API
> Deployment, a consolidation CronJob, and the web UI. CloudNativePG,
> read replicas, HPA, the sidecar cache, the embedding worker, and the
> OTel collector below are all future work.

```
┌─── Region: us-east-1 ────────────────────────────────────────────────────┐
│                                                                           │
│  ┌─── K8s Cluster ─────────────────────────────────────────────────────┐ │
│  │                                                                      │ │
│  │  Namespace: context0-system                                          │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ context0-operator│  │ context0-api-0   │  │ context0-api-1   │  │ │
│  │  │ (Deployment 1/1) │  │ (Deployment 2/2) │  │ (HPA: 2-10)     │  │ │
│  │  │                  │  │                  │  │                  │  │ │
│  │  │ Watches CRDs,    │  │ gRPC + REST      │  │ gRPC + REST      │  │ │
│  │  │ reconciles state │  │ :50051 / :8080   │  │ :50051 / :8080   │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ pg-age-1         │  │ pg-age-2         │  │ pg-age-3         │  │ │
│  │  │ (CloudNativePG   │  │ (read replica)   │  │ (read replica)   │  │ │
│  │  │  Cluster 3/3)    │  │                  │  │                  │  │ │
│  │  │ Primary (writes) │  │ Replica (reads)  │  │ Replica (reads)  │  │ │
│  │  │ PVC: 100Gi SSD   │  │ PVC: 100Gi SSD   │  │ PVC: 100Gi SSD   │  │ │
│  │  │ Extensions:      │  │                  │  │                  │  │ │
│  │  │  apache_age      │  │                  │  │                  │  │ │
│  │  │  pgvector (v0.2) │  │                  │  │                  │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  │  ┌──────────────────┐  ┌──────────────────┐                        │ │
│  │  │ consolidation    │  │ embedding-worker │                        │ │
│  │  │ (CronJob 0/6h)  │  │ (Deployment 1/1) │                        │ │
│  │  │                  │  │                  │                        │ │
│  │  │ Runs merge,      │  │ Generates node   │                        │ │
│  │  │ promote, decay,  │  │ embeddings async │                        │ │
│  │  │ prune phases     │  │ (optional)       │                        │ │
│  │  └──────────────────┘  └──────────────────┘                        │ │
│  │                                                                      │ │
│  │  Namespace: monitoring                                               │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ prometheus       │  │ grafana          │  │ otel-collector   │  │ │
│  │  │                  │  │                  │  │                  │  │ │
│  │  │ Scrapes metrics  │  │ Dashboards:      │  │ Traces + logs    │  │ │
│  │  │ via              │  │ • Graph health   │  │ collection       │  │ │
│  │  │ ServiceMonitor   │  │ • Query latency  │  │                  │  │ │
│  │  │                  │  │ • Consolidation  │  │                  │  │ │
│  │  │                  │  │ • Memory growth  │  │                  │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  │  Namespace: agent-workloads                                          │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ agent-pod-1      │  │ agent-pod-2      │  │ agent-pod-3      │  │ │
│  │  │ ┌──────────────┐ │  │ ┌──────────────┐ │  │                  │  │ │
│  │  │ │ agent        │ │  │ │ agent        │ │  │  agent           │  │ │
│  │  │ │ container    │ │  │ │ container    │ │  │  (no sidecar,    │  │ │
│  │  │ ├──────────────┤ │  │ ├──────────────┤ │  │   uses ClusterIP │  │ │
│  │  │ │ context0     │ │  │ │ context0     │ │  │   service        │  │ │
│  │  │ │ sidecar      │ │  │ │ sidecar      │ │  │   directly)      │  │ │
│  │  │ │ (local cache)│ │  │ │ (local cache)│ │  │                  │  │ │
│  │  │ └──────────────┘ │  │ └──────────────┘ │  │                  │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  └──────────────────────────────────────────────────────────────────────┘ │
│                                                                           │
│  ┌─── External Services ─────────────────────────────────────────────┐   │
│  │  • S3/MinIO: context0-backups (volume snapshots, graph exports)     │   │
│  │  • LLM API: Anthropic or self-hosted Ollama (for consolidation)    │   │
│  │  • Embedding: self-hosted via Ollama or sentence-transformers      │   │
│  └────────────────────────────────────────────────────────────────────┘   │
│                                                                           │
└───────────────────────────────────────────────────────────────────────────┘
```

---

## 10. SDK Interface — How Agents Interact

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     AGENT SDK (any language)                              │
│                                                                          │
│  // Initialize                                                           │
│  client = Context0Client(                                                │
│      endpoint = "context0.context0-system.svc.cluster.local:50051"       │
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

      All managed by: CONTEXT0 OPERATOR (watches CRDs, reconciles state)
      All observed by: OTEL + PROMETHEUS + GRAFANA
```
