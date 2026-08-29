# Kora

**The open-source memory engine for AI agents.** Graph-first, Kubernetes-native.

Kora gives any AI agent persistent, intelligent memory. Store conversations, auto-extract facts, query by meaning, and build user profiles -- all through a simple API running in your own cluster.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-336791.svg)](https://www.postgresql.org)

---

## Why Kora?

Every AI agent framework today has the same problem: **agents are amnesiac**. Sessions start from scratch, context is lost, and agents never learn from past interactions.

Kora solves this with a **graph-based memory engine** that runs in your Kubernetes cluster:

- **Graph-first retrieval** -- relationships between memories, not flat vector similarity
- **Hybrid search** -- Apache AGE graph traversal + pgvector similarity in one PostgreSQL instance
- **Auto extraction** -- feed raw conversations, get structured facts, preferences, and events
- **User profiles** -- auto-built static + dynamic profiles from stored memories
- **100% open source** -- Apache 2.0 license, every dependency OSI-approved
- **Self-hosted** -- your data stays in your cluster, period

## Quick Start

### Upgrading from Context0

This project was called Context0. If you are running an older deployment, the
rename is not purely cosmetic and a plain `helm upgrade` is not enough.

Environment variables moved from `CONTEXT0_*` to `KORA_*`. The engine now
**refuses to start** when it sees an old name rather than falling back to a
default, because an unset `KORA_API_KEYS` disables authentication entirely - a
silent fallback would have brought the API up serving every stored memory
unauthenticated.

The Postgres role and database were also renamed from `context0` to `kora`.
Those live in your data, not in this repo, so a new image points at names that
do not exist yet and fails with `role "kora" does not exist`. The Postgres
StatefulSet's volume survives a `helm upgrade`, so a Kubernetes deployment hits
this too, not just Docker Compose.

Pick whichever fits. Either point the chart back at the names your database
already uses, which changes nothing on disk:

```bash
helm upgrade kora ./charts/kora \
  --set postgres.user=context0 --set postgres.database=context0
```

Or rename the role and database to match the new defaults, with the API
stopped:

```bash
scripts/migrate_rename.sh
```

The script renames both (catalog-only, so the cost does not scale with database
size), reaps any privileged helper role left by an interrupted run, and
verifies the graph is still readable afterwards. It is idempotent.

The AGE graph and its schema keep the name `context0` under either option -
renaming those is a data migration with no functional benefit. See `GraphName`
in `internal/graph/age.go`.

### Prerequisites

- Kubernetes cluster (or [kind](https://kind.sigs.k8s.io/) for local development)
- [Helm](https://helm.sh/) 3.x
- [kubectl](https://kubernetes.io/docs/tasks/tools/)

### Install with Helm

```bash
# Generate an API key (offline; the server stores only its hash)
go run ./cmd/cli keys generate

helm install kora ./charts/kora -n kora --create-namespace \
  --set postgres.password="$(openssl rand -base64 24 | tr -d '/+=')" \
  --set auth.apiKeys="<the key printed above>"
```

The chart ships no default password or API key, and refuses to install without
them: a default credential in a public chart is a published credential. For
anything beyond a local trial, point it at Secrets you manage instead:

```bash
helm install kora ./charts/kora -n kora --create-namespace \
  --set postgres.existingSecret=my-postgres-secret \
  --set auth.existingSecret=my-api-keys
```

### Try it with Docker Compose

```bash
# Generate credentials once, into .env (gitignored). Compose refuses to start
# without them: this file used to ship a working API key, and a credential
# published in a public repo is one every unchanged install shares.
cat > .env <<EOF
POSTGRES_PASSWORD=$(openssl rand -hex 16)
KORA_API_KEYS=$(go run ./cmd/cli keys generate)
EOF

docker compose up
```

This builds and starts PostgreSQL + Apache AGE + pgvector, the Kora API, and
the web UI on <http://localhost:3000>. See
[docker-compose.yaml](docker-compose.yaml) for service details.

Host ports are overridable, for when something else already holds them:

```bash
echo "API_HTTP_PORT=18080" >> .env   # default 8080
echo "WEB_PORT=13000"      >> .env   # default 3000
```

### Try it on kind

```bash
make kind-up
make deploy
```

This creates a local kind cluster and installs the Helm chart into it.

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
pip install kora
```

```python
from kora import KoraClient

client = KoraClient(
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
go build -o kora ./cmd/cli

# Store
kora store "Project uses Go 1.26" --type semantic --tags golang

# Query
kora query "what language" --top-k 5

# View graph neighborhood
kora graph <memory-id> --depth 2

# Stats
kora stats
```

### MCP server (Claude Code, Cursor, Windsurf)

Gives any MCP-compatible agent persistent memory as tools, rather than
hand-written HTTP calls.

```bash
pip install ./mcp-server
```

Then in the editor's MCP config:

```json
{
  "mcpServers": {
    "kora": {
      "command": "kora-mcp",
      "env": {
        "KORA_HTTP_URL": "http://localhost:8080",
        "KORA_API_KEY": "<kora keys generate>"
      }
    }
  }
}
```

Seven tools: `memory_store`, `memory_query`, `memory_extract`,
`memory_profile`, `memory_connect`, `memory_delete`, `memory_graph`. See
[mcp-server/](mcp-server/) for the full setup.

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
│  │            Kora Engine                    │              │
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
kora/
├── cmd/                    # Server, consolidation job, CLI
├── api/proto/              # gRPC proto definitions
├── internal/
│   ├── auth/               # API key + rate limiting
│   ├── embedding/          # Vector embeddings (pluggable)
│   ├── extraction/         # Memory extraction (rule-based + LLM)
│   ├── graph/              # Apache AGE + pgvector repository
│   ├── llm/                # LLM providers (Ollama, OpenAI-compat)
│   ├── ranking/            # Scoring and ranking
│   ├── retrieval/          # Read path: three retrievers, merge, rank
│   └── service/            # gRPC service handlers
├── charts/kora/            # Helm chart (deployment topology)
├── web/                    # React web UI
├── sdk/python/             # Python SDK
├── mcp-server/             # MCP server for Claude Code, Cursor, Windsurf
└── test/e2e/               # End-to-end tests
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
# Create cluster + deploy.
# `make deploy` generates a password and API key into .dev-credentials
# (gitignored) on first run and reuses them afterwards. The chart ships no
# default credentials, because a default in a public chart is a published one.
make kind-up
make deploy

# Run E2E tests against the deployed cluster
. ./.dev-credentials
KORA_E2E_HTTP=http://localhost:8080 \
KORA_E2E_API_KEY="$DEV_API_KEY" \
go test ./test/e2e/... -v -tags=e2e

# Teardown
make kind-down
```

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for the full development workflow, branching strategy, and release process.

### Verifying a change

Beyond `go test`, the repository carries checks that run against a deployed
cluster rather than a mock:

| Script | What it checks |
|---|---|
| `scripts/verify_k8s.sh` | Checks a live deployment (80 at present): probes, security contexts, credentials, NetworkPolicy, PDB evictions, metrics, recoverability, session accounting |
| `scripts/verify_install.sh` | Installs from scratch into a fresh cluster, as a new user would |
| `scripts/verify_perf.sh` | Each performance claim printed next to its observed value, with the statistics and bloat regime it was measured in |
| `scripts/backup.sh` | Dump, restore, and verify -- the restore path is the one that silently skipped the HNSW index |
| `scripts/soak.py` | Long-running invariant checks under continuous load |
| `scripts/mutate.py` | Whether the tests fail when the code is wrong; see [docs/mutation-testing.md](docs/mutation-testing.md) |

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
- [x] Ollama embedding integration
- [x] Contradiction detection
- [ ] Content ingestion (PDF, URLs, code)
- [ ] Framework SDKs (LangChain, CrewAI, MCP)
- [ ] Connectors (GitHub, Google Drive, Notion)
- [ ] K8s Operator with CRDs
- [ ] MemoryBench benchmark results

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
