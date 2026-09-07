# Kora

**The open-source memory engine for AI agents.** Graph-first, Kubernetes-native.

Kora gives any AI agent persistent memory. Store conversations, extract facts, query by meaning, and run the whole thing in your own cluster.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8.svg)](https://go.dev)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-336791.svg)](https://www.postgresql.org)

---

## What the memory is worth

An agent's memory is easy to claim and hard to price. So this repository ships an agent that prices it: a B2B receivables chaser working 50 overdue invoices across 20 customers and 21 simulated days, deciding for each one whether to remind, escalate, offer a payment link, or hand off to a human.

Both configurations recover the same money. Only one of them stops nagging.

| | recovered | messages sent |
|---|---|---|
| escalation ladder alone | ₹633,300 | 216 |
| **with Kora as memory** | **₹633,300** | **49** |

The engine does not recover more. It recovers the same amount without re-chasing anyone who had already promised to pay, disputed the invoice, or been contacted that day. Every decision is a named rule in an audit trail, and a language model only ever rewords the message text -- it never chooses the action.

```bash
make demo                                     # no memory: 216 messages
KORA_URL=http://localhost:8080 \
KORA_API_KEY=<your key> make demo             # with Kora: 49
```

Both rows are reproducible from a clean clone. The agent is in [examples/receivables-chaser/](examples/receivables-chaser/).

## Measured, not claimed

Every retrieval number here comes from `make eval`, an offline benchmark that runs the real engine against the 200-question LoCoMo set and scores it against the dataset's own evidence annotations. It makes no network or model call, and two runs produce identical numbers and an identical digest of every ranked list.

The 69% accuracy figure that preceded it was an LLM judge's verdict on an LLM's answer, and could not be reproduced without an API. That story is in [docs/OPTIMIZATION_REPORT.md](docs/OPTIMIZATION_REPORT.md).

Verbatim-turns corpus, 158 answerable questions:

| metric | before | after |
|---|---|---|
| hit@10 | 0.722 | **0.766** |
| recall@10 | 0.590 | **0.646** |
| MRR@10 | 0.491 | **0.518** |
| query latency p50 / p99 | 24.9 / 53.2 ms | **16.7 / 40.4 ms** |
| allocations per query | 36,215 | **17,905** |

What moved it: 43 of the 44 misses were evidence a retriever had already found and the fusion had buried, because a saturated keyword score outvoted a compressed cosine. Normalising both per query fixed most of that.

**The graph's entity signal, the feature this project is named for, measured +0.005 MRR.** It was left in at its measured weight and the report says so plainly. The full ablation, the failure buckets, and the papers used and rejected are in the report; the step-by-step log with every revert is [docs/WORKLOG.md](docs/WORKLOG.md).

```bash
make eval            # needs Docker; loads the corpus into a throwaway Postgres and prints the table above
make test            # unit tests, no database
```

## Why Kora?

Most agent frameworks are amnesiac: sessions start from scratch, context is lost, and nothing is learned from past interactions. Kora is a memory engine that runs in your own cluster:

- **One store** -- Apache AGE graph traversal and pgvector similarity in a single PostgreSQL instance, so edges and embeddings share one backup and one trust domain
- **Hybrid retrieval** -- full-text, vector, and entity signals fused per query, with every weight traceable to a measurement
- **Auto extraction** -- feed raw conversations, get structured facts, preferences, and events
- **Self-hosted** -- your data stays in your cluster
- **Apache 2.0** -- every dependency OSI-approved, no SSPL or BSL

## Quick Start

### Prerequisites

- Kubernetes cluster (or [kind](https://kind.sigs.k8s.io/) for local development)
- [Helm](https://helm.sh/) 3.x
- [kubectl](https://kubernetes.io/docs/tasks/tools/)

### Retrieval quality: the defaults are a floor

A fresh install runs the zero-dependency backends: hashed bag-of-words
embeddings and a line-by-line rule extractor. Nothing leaves your cluster,
which is the point, and retrieval finds paraphrases poorly because those
vectors score token overlap rather than meaning.

The engine says so rather than leaving you to find out. It warns at startup,
and `/v1/health` reports both backends:

```json
{"status":"ok","embeddingProvider":"bag-of-words","extractionProvider":"rule","zeroDependencyDefaults":true}
```

For real embeddings without sending anything outside the cluster, install with
the quality profile, which points at an Ollama you run:

```bash
helm install kora ./charts/kora -n kora --create-namespace \
  -f ./charts/kora/values-quality.yaml \
  --set postgres.password="..." --set auth.apiKeys="..."
```

See [charts/kora/values-quality.yaml](charts/kora/values-quality.yaml) for what
it changes and what it costs on the write path.

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
make k8s-setup
```

This creates a local kind cluster, labels the namespace for pod security, and installs the Helm chart into it.
Use `make k8s-setup` for the fastest local start.
Use `make k8s-teardown` when you want to remove the local environment.
Use `make k8s-status` to inspect pods/services quickly.
Use `make k8s-smoke-check` for a quick end to end setup + write-path check.
Use `make k8s-verify` for a full in-cluster verification.

## Usage

### Store a Memory

```bash
curl -X POST http://localhost:8080/v1/memories \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{
    "content": "Project uses PostgreSQL 18 with Apache AGE",
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

The SDK is not published to PyPI yet, and the name `kora` there belongs to an
unrelated Colab package. Install it from this repository:

```bash
pip install ./sdk/python
```

```python
from kora import KoraClient

client = KoraClient(
    endpoint="localhost:50051",
    api_key="your-key",
    project="my-project",
)

# Store
mem = client.store("Project uses PostgreSQL 18", type="semantic", tags=["database"])

# Query
results = client.query("what database does this project use?", top_k=3)

# Auto-extract from conversation
client.extract("user: We switched to PostgreSQL\nuser: I prefer vim")

# Engine health and stats
status = client.health()

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

```mermaid
flowchart TB
    subgraph agents [" "]
        A["Agent A"]
        B["Agent B"]
        C["Agent C"]
    end

    agents -->|gRPC / REST| API

    subgraph cluster ["Kubernetes cluster"]
        API["Kora engine"]
        API --- ING["Ingest + extract"]
        API --- QRY["Hybrid query"]
        API --- CON["Consolidation (CronJob)"]
        ING --> PG
        QRY --> PG
        CON --> PG
        PG[("PostgreSQL<br/>Apache AGE graph + pgvector embeddings")]
        WEB["React web UI<br/>(graph visualisation)"]
        PROM["Prometheus<br/>/metrics"]
        API --- WEB
        API --- PROM
    end
```

Agents talk to one engine over gRPC or REST. The engine writes graph nodes and
vectors into a single PostgreSQL instance, so a memory and its edges are
committed together and restored together.

### How Memory Works

```
Conversation ──> Extract ──> Memory Nodes (fact/preference/event)
                                │
                    ┌───────────┼───────────┐
                    v           v           v
               [semantic]  [episodic]  [procedural]
                    │           │           │
                    └─── Graph Edges ───────┘
                    (relates_to, supersedes, mentions -> Entity)
                                │
                                v
   Query ──> full-text search (IDF-weighted ts_rank_cd, GIN index)
         ──> vector search    (pgvector cosine, exact within a project)
         ──> entity match     (memories naming the query's entities)
                                │
                                v
   per-query min-max normalisation, weighted sum 0.45 / 0.40 / 0.15
   then recency, frequency and type priors as tie-breaks
                                │
                                v
                      Ranked Results with Context
```

The read path is `internal/retrieval`; the fusion and priors are `internal/ranking`, and every constant there carries the measurement that set it.

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
│   ├── ranking/            # Scoring and ranking
│   ├── retrieval/          # Read path: three retrievers, merge, rank
│   └── service/            # gRPC service handlers
├── charts/kora/            # Helm chart (deployment topology)
├── eval/                   # Offline retrieval benchmark (`make eval`)
├── examples/
│   └── receivables-chaser/ # The demo agent behind the table at the top
├── web/                    # React web UI
├── sdk/python/             # Python SDK
├── mcp-server/             # MCP server for Claude Code, Cursor, Windsurf
└── test/e2e/               # End-to-end tests
```

## Documentation

| | |
|---|---|
| [docs/ARCHITECTURE_DECISIONS.md](docs/ARCHITECTURE_DECISIONS.md) | Why one Postgres, why Go, why rules over an LLM, why gRPC plus REST, and what breaks at 10M nodes |
| [docs/OPTIMIZATION_REPORT.md](docs/OPTIMIZATION_REPORT.md) | The full technical report: baseline vs final, ablations, failure analysis, papers used and rejected |
| [docs/WORKLOG.md](docs/WORKLOG.md) | Every hypothesis, measurement and revert, in order |
| [eval/README.md](eval/README.md) | The offline benchmark and how to run it |
| [examples/receivables-chaser/](examples/receivables-chaser/README.md) | The demo agent behind the table at the top |
| [docs/kubernetes.md](docs/kubernetes.md) | Chart settings and the reason for each |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | Workflow, branching, release process |
| [docs/PROJECT_DESCRIPTION.md](docs/PROJECT_DESCRIPTION.md) | The project at four lengths, plus a counted fact sheet |

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
# `make k8s-setup` creates a local kind cluster, generates local credentials, and
# deploys the chart in one command.
make k8s-setup

# Run E2E tests against the deployed cluster
. ./.dev-credentials
KORA_E2E_HTTP=http://localhost:8080 \
KORA_E2E_API_KEY="$DEV_API_KEY" \
go test ./test/e2e/... -v -tags=e2e

# Teardown
make k8s-teardown
```

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for the full development workflow, branching strategy, and release process.

### Verifying a change

Beyond `go test`, the repository carries checks that run against a deployed
cluster rather than a mock:

| Script | What it checks |
|---|---|
| `scripts/verify_k8s.sh` | Checks a live deployment (95 at present): probes, security contexts, credentials, NetworkPolicy, PDB evictions, metrics, recoverability, session accounting |
| `scripts/verify_install.sh` | Installs from scratch into a fresh cluster, as a new user would |
| `scripts/verify_perf.sh` | Each performance claim printed next to its observed value, with the statistics and bloat regime it was measured in |
| `scripts/backup.sh` | Dump, restore, and verify -- the restore path is the one that silently skipped the HNSW index |
| `scripts/soak.py` | Long-running invariant checks under continuous load |
| `scripts/mutate.py` | Whether the tests fail when the code is wrong; see [docs/mutation-testing.md](docs/mutation-testing.md) |

## Migrating from Context0

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

### What still says context0, and why

Three names survived the rename on purpose. Each is data-bearing or
credential-bearing, so renaming it would cost an existing deployment something
real in exchange for consistency:

| Name | Where | Why it stays |
|---|---|---|
| The AGE graph and its schema | `GraphName` in `internal/graph/age.go` | It is the Postgres schema holding every deployment's data. Renaming it is a data migration with no functional benefit, and the SQL in `docs/research/` refers to it. |
| The `ctx0_` API key prefix | `KeyPrefix` in `internal/auth/keys.go` | It is in every key ever issued. Changing it invalidates all of them to fix an aesthetic problem. |
| The Cloudflare Pages project | `.github/workflows/site.yaml` | Renaming it changes the deployment target of the docs site. |

Everything else - the module, the binaries, the chart, the environment
variables, the repository - is Kora. If you see `context0` anywhere other than
the three rows above, it is a leftover and worth an issue.

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
- [x] MCP server (Claude Code, Cursor, Windsurf)
- [x] Helm chart + kind deployment
- [x] Ollama embedding integration
- [x] Contradiction detection
- [x] Deterministic offline retrieval benchmark ([`eval/`](eval/README.md))
- [ ] Publish the Python SDK to PyPI
- [ ] Per-embedder fusion defaults, so the zero-dependency install does not lose hit@10
- [ ] Merge verbatim rounds with extracted facts, which addresses the 36-of-200 extraction loss
- [ ] Content ingestion (PDF, URLs, code)
- [ ] Framework SDKs (LangChain, CrewAI)
- [ ] Connectors (GitHub, Google Drive, Notion)

## Known limitations

Stated here rather than left to be discovered:

- **Extraction drops evidence.** For 36 of 200 benchmark questions, every
  supporting memory is lost before retrieval sees it. The remedy is measured in
  the literature and not yet implemented.
- **The entity graph contributes +0.005 MRR** on this benchmark. It carries
  provenance the vector store cannot, but it is not what makes retrieval work.
- **The zero-dependency defaults are a floor, not a target.** Hashed
  bag-of-words embeddings score token overlap, not meaning; see above for the
  quality profile.
- **The agent result is a synthetic fixture**, not production traffic. Both
  arms replay the same scripted world, which makes the comparison fair, but it
  is not a claim about real merchants.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
