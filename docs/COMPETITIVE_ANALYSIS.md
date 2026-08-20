# Competitive Analysis: Kora vs Supermemory

## Supermemory — What They Do Well

Supermemory is a **managed SaaS memory API** (closed-source, hosted). Here's what they've built:

### Their Core Features

| Feature | How It Works |
|---------|-------------|
| **Memory API** | Auto-extracts facts from conversations, builds knowledge graph |
| **User Profiles** | Static facts (role, expertise) + dynamic facts (current project, recent events). Auto-built from interactions. |
| **Super RAG** | Hybrid search (vector + memory extraction), content-aware chunking (AST for code, headers for docs), reranking, query rewriting |
| **Graph Memory** | Living knowledge graph with 3 relationship types: updates (supersedes), extends (enriches), derives (inferred) |
| **Auto Forgetting** | Time-based decay for episodes, contradiction detection, noise filtering |
| **Content Types** | Text, URLs, PDFs, DOCX, XLSX, PPTX, images (OCR), audio/video (transcription), code (AST chunking), JSON, CSV |
| **Connectors** | GitHub, Google Drive, Gmail, Notion, OneDrive, S3, Web Crawler |
| **Benchmarks** | 81.6% on LongMemEval-S (vs Zep 71.2%), strong on temporal reasoning and multi-session |

### Their Processing Pipeline

```
Document → Queued → Extracting → Chunking → Embedding → Indexing → Done
```

### Their Memory Types (Auto-Detected)

| Type | Lifespan | Example |
|------|----------|---------|
| Facts | Persistent until updated | "Alex is a senior engineer" |
| Preferences | Strengthens with repetition | "Prefers TypeScript over JavaScript" |
| Episodes | Decay unless significant | "Had a meeting about auth today" |

### Their API (Simple)

```python
# Store
client.add(content="conversation text", container_tag="user-123")

# Retrieve profile + context
result = client.profile(container_tag="user-123", q="what does this user know?")
# Returns: static_profile + dynamic_profile + search_results

# Search
results = client.search(query="auth architecture", container_tag="user-123")
```

---

## Where Supermemory Falls Short (Our Opportunity)

### 1. Closed Source, Hosted Only
- **Their weakness:** No self-hosting. Data lives on their servers. Enterprise customers with compliance requirements (HIPAA, SOC2, data residency) can't use it.
- **Our advantage:** 100% open source (Apache 2.0), self-hostable, K8s-native. Data never leaves your cluster.

### 2. No Infrastructure Control
- **Their weakness:** You can't run it in your own cloud, your own region, or your own cluster. Latency depends on their infrastructure.
- **Our advantage:** Deploy with `helm install` into any K8s cluster. No network round-trip to a vendor. You control scaling, backups, and regions. See the latency note below for measured numbers rather than a claim.

### 3. Vendor Lock-in
- **Their weakness:** Proprietary API, proprietary storage format. Switching away means losing all your memory data.
- **Our advantage:** Open standard (graph DB), open API (gRPC + REST), data is in PostgreSQL — export it anytime with standard tools.

### 4. No Graph Transparency
- **Their weakness:** Graph is a black box. You can't see, query, or debug the actual graph structure. No way to understand WHY a memory was retrieved.
- **Our advantage:** Full graph visibility via Web UI (React Flow), direct Cypher queries against AGE, subgraph API endpoint, CLI graph command.

### 5. Pricing Risk at Scale
- **Their weakness:** Usage-based pricing. As your agents scale, costs grow unpredictably.
- **Our advantage:** Run on your own infra. Cost is just compute + storage — fixed and predictable.

### 6. No K8s Integration
- **Their weakness:** External API call from your cluster to their cloud. Network latency, dependency on their uptime.
- **Our advantage:** Runs as a K8s workload alongside your agents. ClusterIP service, no external network hop.

---

## Feature Gap Analysis: What We're Missing

Here's what Supermemory has that Kora doesn't yet, and what we should build:

### CRITICAL — Must Build for MVP+

| Feature | Supermemory | Kora Today | Priority |
|---------|-------------|----------------|----------|
| **Auto memory extraction from conversations** | Parses unstructured text, extracts facts automatically | Manual: agent must explicitly call Store with structured content | **P0** — This is their killer feature |
| **User Profiles (static + dynamic)** | Auto-built, auto-maintained, queryable as a unit | No profile concept — just raw memories | **P0** — Needed for personalization use case |
| **Content-aware chunking** | AST for code, headers for docs, OCR for images | No document ingestion — text only | **P1** |
| **Hybrid search (vector + graph)** | Combined retrieval with reranking | Graph-only (pgvector planned for v0.2) | **P1** |
| **Memory type auto-detection** | Classifies as fact/preference/episode automatically | Manual type selection by caller | **P1** |
| **Automatic forgetting/decay** | Time-based expiration, contradiction detection | Basic decay score in consolidation CronJob — needs to be smarter | **P1** |

### HIGH — Build Soon After MVP

| Feature | Supermemory | Kora Today | Priority |
|---------|-------------|----------------|----------|
| **Relationship auto-detection** | Detects updates/extends/derives automatically | Manual: agent calls Connect() explicitly | **P2** |
| **Multi-content ingestion** | PDF, DOCX, images, audio, video, URLs | Text only | **P2** |
| **Connectors** | GitHub, Google Drive, Gmail, Notion, OneDrive, S3 | None | **P2** |
| **Reranking** | Cross-encoder reranking for better precision | Simple scoring function | **P2** |
| **Query rewriting** | LLM-powered query expansion | Keyword extraction + stop words | **P2** |
| **Container tags** | Simple grouping mechanism for memories | Project-based scoping | **P2** |

### MEDIUM — Differentiation Opportunities

| Feature | Supermemory | Kora Today | Priority |
|---------|-------------|----------------|----------|
| **Framework SDKs** | LangChain, CrewAI, Vercel AI, Claude Code integrations | Python SDK + Go CLI | **P3** |
| **Benchmarking** | LongMemEval-S, LoCoMo benchmarks | No benchmarks yet | **P3** |
| **Migration tools** | Migrate from Mem0, Zep | None | **P3** |

---

## Where Kora Already Wins

Things we have that Supermemory doesn't offer:

| Kora Advantage | Details |
|-------------------|---------|
| **Open source** | Full source code, Apache 2.0. Supermemory is closed-source SaaS. |
| **Self-hostable** | Helm chart, K8s manifests. Supermemory is hosted-only. |
| **Graph transparency** | Web UI to explore graph, Cypher queries, subgraph API. Their graph is a black box. |
| **K8s-native** | Runs as K8s workloads, CronJobs for consolidation. They're a remote API. |
| **Data ownership** | Your data stays in your PostgreSQL. They hold your data. |
| **In-cluster latency** | ~22ms idle, ~54ms under sustained load (measured, see below). No round-trip to a vendor cloud. |
| **Predictable cost** | Fixed infra cost. Their usage-based pricing scales unpredictably. |
| **gRPC API** | Binary protocol, streaming, strong typing. They only have REST. |

---

## Roadmap: Closing the Gaps

### v0.2 — Close the Critical Gaps

1. **Auto Memory Extraction** (P0)
   - Accept raw conversation text (multi-turn messages)
   - Use LLM (Claude Haiku / Llama) to extract facts, preferences, and episodes
   - Auto-create Memory nodes + relationship edges from extracted data
   - Detect contradictions → create `supersedes` edges automatically

2. **User Profiles** (P0)
   - New `Profile` node type in graph
   - Static profile: aggregated from semantic memories with high confidence
   - Dynamic profile: recent episodic memories (last N days)
   - New `GET /v1/profiles/{user_id}` endpoint that returns combined static + dynamic context
   - Auto-build profiles from stored memories via consolidation

3. **pgvector Hybrid Search** (P1)
   - Add pgvector extension alongside Apache AGE
   - Store embeddings on Memory nodes
   - Hybrid retrieval: graph traversal + vector similarity, merge + rerank
   - Support open-source embedding models (BGE, E5) via Ollama

4. **Smarter Auto-Detection** (P1)
   - Classify memory type (fact/preference/episode) using lightweight LLM
   - Detect relationship type (updates/extends/derives) on ingest
   - No manual type selection needed — agents just send raw content

### v0.3 — Differentiate

5. **Content-Aware Ingestion Pipeline**
   - Accept PDFs, URLs, Markdown, code files
   - Content-type detection + appropriate chunking (AST for code, headers for docs)
   - OCR for images (via Tesseract, open source)

6. **Connectors**
   - GitHub (repos, issues, PRs)
   - Google Drive, Notion
   - Pluggable connector interface for community extensions

7. **Framework SDKs**
   - LangChain, CrewAI, AutoGen, Vercel AI SDK integrations
   - MCP (Model Context Protocol) server for Claude Code

8. **MemoryBench**
   - Run LongMemEval-S and LoCoMo benchmarks against Kora
   - Publish results, establish credibility

---

## Positioning Summary

```
Supermemory = Managed memory SaaS (closed source, hosted)
Kora    = Self-hosted memory engine (open source, K8s-native)

Supermemory is Vercel.
Kora is Kubernetes.

Same problem, different philosophy:
- They optimize for "get started in 5 minutes" (developer convenience)
- We optimize for "own your data, run it your way" (infrastructure control)

Both are valid. We target a different audience:
- Teams that need data sovereignty
- Enterprises with compliance requirements
- Organizations already running K8s
- Developers who want to understand and debug their memory system
```


## Latency: measured, not claimed

Earlier versions of this document asserted "<20ms in-cluster" without a
measurement behind it. The figures below come from a kind cluster running the
shipped chart, against a graph of ~94,000 memories and ~312,000 edges, with a
1.5-core Postgres.

| Operation | Idle (serial) | Under 6-way concurrent load |
|---|---|---|
| Query, scoped to a project | ~22 ms | ~54 ms mean, p50 ~50 ms |
| Store (full pipeline) | ~38 ms | ~93 ms mean |
| `/v1/health` | ~1 ms | ~1 ms p50 |

Method matters for reading these:

- **Store is not one write.** It creates the vertex, generates and stores an
  embedding, runs contradiction detection, writes supersedes edges, and
  auto-links by tag. It is the whole pipeline, not an insert.
- **Measure in-cluster.** Through `kubectl port-forward` the tunnel becomes the
  bottleneck under concurrency and the numbers describe it, not the service.
- **Graph size matters, and it is stated.** A latency number without the size of
  the database behind it is not reproducible. These are at 94k vertices; smaller
  deployments are faster.

Reproduce with `scripts/soak.py`; see `docs/soak-testing.md`.
