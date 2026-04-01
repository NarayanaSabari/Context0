# Context0

**The open-source memory engine for AI agents.** Graph-first, Kubernetes-native.

Context0 gives any AI agent persistent, intelligent memory. Store conversations, auto-extract facts, query by meaning, and build user profiles -- all through a simple API running in your own cluster.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-336791.svg)](https://www.postgresql.org)

---

## Why Context0?

Every AI agent framework today has the same problem: **agents are amnesiac**. Sessions start from scratch, context is lost, and agents never learn from past interactions.

Context0 solves this with a **graph-based memory engine** that runs in your Kubernetes cluster:

- **Graph-first retrieval** -- relationships between memories, not flat vector similarity
- **Hybrid search** -- Apache AGE graph traversal + pgvector similarity in one PostgreSQL instance
- **Auto extraction** -- feed raw conversations, get structured facts, preferences, and events
- **User profiles** -- auto-built static + dynamic profiles from stored memories
- **100% open source** -- Apache 2.0 license, every dependency OSI-approved
- **Self-hosted** -- your data stays in your cluster, period

## Quick Start

### Prerequisites

- Kubernetes cluster (or [kind](https://kind.sigs.k8s.io/) for local development)
- [Helm](https://helm.sh/) 3.x
- [kubectl](https://kubernetes.io/docs/tasks/tools/)

### Install with Helm

```bash
# Local development (single replica, no auth, built-in embeddings)
helm install context0 ./charts/context0 \
  -f charts/context0/values-local.yaml \
  -n context0 --create-namespace

# Production (HA, auth, Ollama embeddings, monitoring)
helm install context0 ./charts/context0 \
  -f charts/context0/values-production.yaml \
  -n context0 --create-namespace
```

### Try it with kind

```bash
# One command to spin up everything
./scripts/demo.sh
```

This creates a kind cluster, deploys PostgreSQL + Apache AGE + pgvector, Context0 API, and runs E2E tests.

## Usage

### Store a Memory

```bash
curl -X POST http://localhost:8080/v1/memories \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{
    "content": "Project uses PostgreSQL 15 with Apache AGE",
    "type": 2,
    "project_id": "my-project",
    "tags": ["database", "postgres"]
  }'
```

### Auto-Extract from Conversations

```bash
curl -X POST http://localhost:8080/v1/memories/extract \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{
    "conversation": "user: We use PostgreSQL for the database\nuser: I prefer Go for backend services\nuser: We switched from MySQL last week",
    "project_id": "my-project"
  }'
```

The engine automatically extracts facts, preferences, and events -- creating typed memory nodes with relationship edges.

### Query Memories

```bash
curl "http://localhost:8080/v1/memories/query?query=what+database&project_id=my-project&top_k=5" \
  -H "X-API-Key: $API_KEY"
```

Returns ranked results combining graph traversal + vector similarity (hybrid search).

### Get User Profile

```bash
curl "http://localhost:8080/v1/profiles/my-project" \
  -H "X-API-Key: $API_KEY"
```

Returns an aggregated profile:
- **Static profile**: facts and preferences (stable knowledge)
- **Dynamic profile**: recent events from the last 7 days

### Python SDK

```bash
pip install context0
```

```python
from context0 import Context0Client

client = Context0Client(
    endpoint="localhost:50051",
    api_key="your-key",
    project="my-project",
)

# Store
mem = client.store("Project uses PostgreSQL 15", type="semantic", tags=["database"])

# Query
results = client.query("what database does this project use?", top_k=3)

# Auto-extract from conversation
client.extract("user: We switched to PostgreSQL\nuser: I prefer vim")

# Get profile
profile = client.health()

# Session lifecycle
with client.session() as s:
    client.store("discussed auth architecture", type="episodic")
```

### CLI

```bash
# Build the CLI
go build -o context0 ./cmd/cli

# Store
context0 store "Project uses Go 1.26" --type semantic --tags golang

# Query
context0 query "what language" --top-k 5

# View graph neighborhood
context0 graph <memory-id> --depth 2

# Stats
context0 stats
```

## Architecture

```
┌─── Kubernetes Cluster ─────────────────────────────────────────┐
│                                                                 │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                     │
│  │ Agent A  │  │ Agent B  │  │ Agent C  │  (any framework)    │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘                     │
│       └──────────────┼──────────────┘                           │
│                      │  gRPC / REST                             │
│                      v                                          │
│  ┌──────────────────────────────────────────────┐              │
│  │            Context0 Engine                    │              │
│  │                                               │              │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────────┐ │              │
│  │  │ Ingest + │ │ Hybrid   │ │ Consolidation│ │              │
│  │  │ Extract  │ │ Query    │ │ (CronJob)    │ │              │
│  │  └──────────┘ └──────────┘ └──────────────┘ │              │
│  │                                               │              │
│  │  ┌──────────────────────────────────────────┐│              │
│  │  │  PostgreSQL + Apache AGE + pgvector      ││              │
│  │  │  Graph nodes + Vector embeddings         ││              │
│  │  └──────────────────────────────────────────┘│              │
│  └──────────────────────────────────────────────┘              │
│                                                                 │
│  ┌─────────────┐  ┌─────────────┐                              │
│  │ React Web UI│  │ Prometheus  │                              │
│  │ (graph viz) │  │ /metrics    │                              │
│  └─────────────┘  └─────────────┘                              │
└─────────────────────────────────────────────────────────────────┘
```

### How Memory Works

```
Conversation ──> Extract ──> Memory Nodes (fact/preference/event)
                                │
                    ┌───────────┼───────────┐
                    v           v           v
               [semantic]  [episodic]  [procedural]
                    │           │           │
                    └─── Graph Edges ───────┘
                    (relates_to, supersedes, caused_by)
                                │
                    ┌───────────┼───────────┐
                    v           v           v
              Graph Query + Vector Search + Ranking
                                │
                                v
                      Ranked Results with Context
```

### Tech Stack

| Component | Technology | License |
|-----------|-----------|---------|
| Engine | Go | BSD-3-Clause |
| Graph DB | Apache AGE (PostgreSQL extension) | Apache 2.0 |
| Vector Search | pgvector | PostgreSQL License |
| Database | PostgreSQL 18 | PostgreSQL License |
| API | gRPC + grpc-gateway (REST) | Apache 2.0 |
| Web UI | React + React Flow + Tailwind | MIT |
| Observability | Prometheus | Apache 2.0 |

**Every dependency is OSI-approved open source.** No SSPL, BSL, or proprietary components.

## API Reference

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/v1/memories` | Store a memory |
| POST | `/v1/memories/extract` | Auto-extract memories from conversation |
| GET | `/v1/memories/query` | Query memories (hybrid search) |
| POST | `/v1/memories/connect` | Create relationship between memories |
| DELETE | `/v1/memories/{id}` | Delete a memory |
| GET | `/v1/memories/{id}/graph` | Get subgraph around a memory |
| GET | `/v1/profiles/{project_id}` | Get aggregated user profile |
| POST | `/v1/sessions` | Start agent session |
| POST | `/v1/sessions/{id}/end` | End agent session |
| GET | `/v1/health` | Health check + stats |
| GET | `/metrics` | Prometheus metrics |

## Project Structure

```
context0/
├── cmd/                    # Server, consolidation job, CLI
├── api/proto/              # gRPC proto definitions
├── internal/
│   ├── auth/               # API key + rate limiting
│   ├── embedding/          # Vector embeddings (pluggable)
│   ├── extraction/         # Memory extraction (rule-based + LLM)
│   ├── graph/              # Apache AGE + pgvector repository
│   ├── llm/                # LLM providers (Ollama, OpenAI-compat)
│   ├── ranking/            # Scoring and ranking
│   └── service/            # gRPC service handlers
├── charts/context0/        # Helm chart
├── web/                    # React web UI
├── sdk/python/             # Python SDK
├── test/e2e/               # End-to-end tests
└── bench/                  # MemoryBench integration
```

## Development

### Build from source

```bash
# Build all binaries
make build

# Run tests
make test

# Lint
make lint

# Run locally (needs PostgreSQL + AGE)
make run

# Docker build
make docker-build
```

### Run on kind cluster

```bash
# Create cluster + deploy
make kind-up
make deploy

# Run E2E tests
CONTEXT0_E2E_HTTP=http://localhost:8080 \
CONTEXT0_E2E_API_KEY=ctx0_dev_key_1 \
go test ./test/e2e/... -v -tags=e2e

# Teardown
make kind-down
```

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for the full development workflow, branching strategy, and release process.

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

- **Issues**: Bug reports, feature requests, and questions
- **Pull Requests**: Fork, branch, commit, PR against `main`
- **Discussions**: Architecture decisions and roadmap input

## Roadmap

- [x] Core engine (gRPC + REST API)
- [x] Apache AGE graph database
- [x] pgvector hybrid search
- [x] Auto memory extraction
- [x] User profiles
- [x] React web UI with graph visualization
- [x] Python SDK + CLI
- [x] Helm chart + kind deployment
- [ ] Ollama embedding integration
- [ ] Contradiction detection
- [ ] Content ingestion (PDF, URLs, code)
- [ ] Framework SDKs (LangChain, CrewAI, MCP)
- [ ] Connectors (GitHub, Google Drive, Notion)
- [ ] K8s Operator with CRDs
- [ ] MemoryBench benchmark results

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
