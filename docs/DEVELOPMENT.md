# Kora — Development Workflow & Release Strategy

## Git Branching Strategy

We use **trunk-based development** with short-lived feature branches and release branches.

```
main (protected)
│
├── feat/auto-extraction        ← feature branch (short-lived, 1-5 days)
├── feat/ollama-embeddings      ← feature branch
├── fix/query-timeout           ← bugfix branch
│
├── release/v0.1.0              ← release branch (cut from main)
├── release/v0.2.0              ← release branch
│
└── hotfix/v0.1.1               ← hotfix from release branch (critical only)
```

### Branch Rules

| Branch | Who merges | Requirements | Protected |
|--------|-----------|--------------|-----------|
| `main` | PR only | CI passes + 1 approval + all tests green | Yes |
| `feat/*` | Developer | CI passes | No |
| `fix/*` | Developer | CI passes | No |
| `release/v*` | Release manager | All tests + E2E + manual QA | Yes |
| `hotfix/v*` | Release manager | Fix + test only, no new features | Yes |

### Branch Naming

```
feat/<short-description>     # New features
fix/<short-description>       # Bug fixes
refactor/<short-description>  # Code restructuring (no behavior change)
docs/<short-description>      # Documentation only
ci/<short-description>        # CI/CD pipeline changes
perf/<short-description>      # Performance improvements
```

### Workflow

```
1. Create feature branch from main
   git checkout -b feat/ollama-embeddings main

2. Develop, commit (conventional commits), push
   git push -u origin feat/ollama-embeddings

3. Open PR against main
   - CI runs automatically (lint, test, build)
   - Request review

4. Squash-merge into main
   - PR title becomes commit message
   - Feature branch deleted after merge

5. When ready for release, cut release branch
   git checkout -b release/v0.2.0 main
```

---

## Versioning Strategy

We follow **Semantic Versioning (SemVer)**: `MAJOR.MINOR.PATCH`

| Component | When to bump | Example |
|-----------|-------------|---------|
| MAJOR | Breaking API changes | v1.0.0 → v2.0.0 |
| MINOR | New features, backward compatible | v0.1.0 → v0.2.0 |
| PATCH | Bug fixes, no new features | v0.1.0 → v0.1.1 |

### Pre-1.0 Rules

While we're in `v0.x.x`, the API is not considered stable:
- MINOR bumps may include breaking changes (documented in CHANGELOG)
- PATCH bumps are always backward compatible

### Version Artifacts

Every version produces:
- Git tag: `v0.2.0`
- Docker images: `kora/kora:v0.2.0`, `kora/kora:latest`
- Helm chart version: `0.2.0`
- Python SDK: `kora==0.2.0` on PyPI
- GitHub Release with changelog and binaries

### Release Cadence

| Phase | Cadence | What |
|-------|---------|------|
| Pre-launch (now) | Every 2-4 weeks | Minor releases (v0.1, v0.2, v0.3...) |
| Post-launch (v1.0+) | Monthly | Minor releases with features |
| Patches | As needed | Bug fixes, security patches |

---

## Two Deployment Modes

Kora ships in two configurations from the **same codebase, same branch**.
No separate branches — configuration controls the mode.

### Mode 1: Local (Development / Self-Hosted Minimal)

For developers trying Kora, local testing, or small self-hosted deployments.

```yaml
# Example local overrides (pass with -f, or use `make deploy`)
mode: local

postgres:
  image: kora/postgres-age-vector:latest
  replicas: 1
  storage: 5Gi

api:
  replicas: 1
  resources:
    requests: { memory: 128Mi, cpu: 100m }

embedding:
  provider: bag-of-words    # Zero dependencies, works out of the box
  # provider: ollama        # If Ollama is available locally
  # ollamaUrl: http://host.docker.internal:11434

llm:
  provider: rule-based      # No LLM API needed
  # provider: ollama
  # model: llama3.2

consolidation:
  enabled: true
  schedule: "0 */6 * * *"

auth:
  # Running without authentication is now explicit rather than the result of
  # leaving a field blank. Only sensible when nothing outside your machine can
  # reach the cluster.
  allowUnauthenticated: true
```

**Install:**
```bash
# `make deploy` is the shortcut: it generates a password and API key into
# .dev-credentials (gitignored) on first run and reuses them afterwards.
make kind-up && make deploy

# Or explicitly:
helm install kora ./charts/kora -n kora --create-namespace \
  --set postgres.password="$(openssl rand -hex 16)" \
  --set auth.apiKeys="$(go run ./cmd/cli keys generate)"
```

### Mode 2: Production (Enterprise / Managed)

For production deployments with HA, real embeddings, monitoring, and security.

```yaml
# Example production overrides (pass with -f)
mode: production

postgres:
  image: kora/postgres-age-vector:latest
  replicas: 3              # 1 primary + 2 read replicas
  storage: 100Gi
  resources:
    requests: { memory: 2Gi, cpu: 1000m }
    limits: { memory: 4Gi, cpu: 2000m }

api:
  replicas: 3
  hpa:
    enabled: true
    minReplicas: 2
    maxReplicas: 10
    targetCPU: 70
  resources:
    requests: { memory: 256Mi, cpu: 200m }
    limits: { memory: 1Gi, cpu: 1000m }

embedding:
  provider: ollama          # Or openai-compatible
  ollamaUrl: http://ollama.ollama.svc.cluster.local:11434
  model: nomic-embed-text

llm:
  provider: ollama          # Or openai-compatible
  ollamaUrl: http://ollama.ollama.svc.cluster.local:11434
  model: llama3.2

consolidation:
  enabled: true
  schedule: "0 */4 * * *"
  llmEnabled: true          # Use LLM for smart merging

auth:
  enabled: true
  apiKeys:
    secretName: kora-api-keys

monitoring:
  prometheus: true
  grafanaDashboards: true
  otelCollector: true

security:
  networkPolicies: true
  podDisruptionBudget: true
  tls:
    enabled: true
    certManager: true
```

**Install:**
```bash
# Credentials come from Secrets you manage (External Secrets, Sealed Secrets,
# SOPS, or a CSI driver). With existingSecret set, the chart creates no Secret
# of its own and never sees the values.
helm install kora ./charts/kora -n kora --create-namespace \
  -f my-production-values.yaml \
  --set postgres.existingSecret=kora-postgres \
  --set auth.existingSecret=kora-api-keys
```

Then enforce the Pod Security "restricted" profile on the namespace. Helm does
not manage the namespace it installs into, so this is a separate step:

```bash
kubectl label namespace kora \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest
```

### How modes differ (same code, different config)

| Aspect | Local | Production |
|--------|-------|------------|
| Postgres replicas | 1 | 3 (HA) |
| API replicas | 1 | 2-10 (HPA) |
| Embeddings | BagOfWords (built-in) | Ollama / real model |
| LLM extraction | Rule-based | Ollama / API |
| Auth | Disabled | API keys + rate limiting |
| TLS | Off | cert-manager |
| Monitoring | /metrics endpoint only | Prometheus + Grafana + OTel |
| Network policies | None | Strict |
| PDB | None | Enabled |
| Resources | Minimal (128Mi) | Production-sized |

---

## CI/CD Pipeline

### Pipeline Architecture

```
┌─────────────┐     ┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   Commit    │────▶│    CI         │────▶│   Build      │────▶│   Publish    │
│   Push/PR   │     │  (quality)    │     │  (artifacts)  │     │  (release)   │
└─────────────┘     └──────────────┘     └──────────────┘     └──────────────┘
                     │                    │                    │
                     ├─ Lint (golangci)   ├─ Go binaries      ├─ Docker push
                     ├─ Unit tests        ├─ Docker images    ├─ Helm publish
                     ├─ Race detection    ├─ Helm package     ├─ GitHub Release
                     ├─ Coverage check    ├─ Python SDK dist  ├─ PyPI publish
                     ├─ Proto check       └─ Web UI build     └─ Changelog
                     ├─ Vet
                     └─ Security scan
```

### CI Jobs (Run on every PR and push to main)

#### Job 1: Lint & Static Analysis
```yaml
- golangci-lint run ./...
- go vet ./...
- buf lint (proto files)
- pnpm -C web lint
```

#### Job 2: Unit Tests
```yaml
- go test ./... -race -cover -coverprofile=coverage.out
- Check coverage >= 80% on critical packages
- pnpm -C web test (when we add frontend tests)
```

#### Job 3: Retrieval Regression
```yaml
- docker compose up -d postgres   # via .github/actions/postgres
- go test ./test/golden/... -race -count=1 -v
```
A fixed corpus and fixed queries with committed floors on recall@10 and MRR.
The only job that can tell that a ranking, extraction, or graph change made
retrieval worse.

#### Job 4: Build
```yaml
- go build ./cmd/server ./cmd/consolidate ./cmd/cli
- docker build -t kora/kora:ci .
- docker build -t kora/web:ci ./web
- docker build -t kora/postgres-age-vector:ci ./docker/postgres-age-vector
- helm lint ./charts/kora
```

#### Job 5: E2E Tests (on main only, not every PR)
```yaml
- Create kind cluster
- Deploy all components
- Run go test ./test/e2e/... -tags=e2e
- Teardown
```

#### Job 6: Security Scan
```yaml
- gosec ./...
- trivy image kora/kora:ci
- trivy image kora/postgres-age-vector:ci
```

### CD Jobs (Run on tag push v*)

#### Job 6: Release
```yaml
triggers: push tag v*.*.*

steps:
  - Build multi-arch Docker images (amd64 + arm64)
  - Push to Docker Hub:
      kora/kora:v0.2.0
      kora/kora:latest
      kora/web:v0.2.0
      kora/postgres-age-vector:v0.2.0
  - Package and push Helm chart to OCI registry
  - Build and publish Python SDK to PyPI
  - Create GitHub Release with:
      - Changelog (auto-generated from conventional commits)
      - Binary artifacts (kora-cli for linux/mac/windows)
      - SHA256 checksums
```

---

## Testing Strategy

### Test Pyramid

```
        ╱╲
       ╱  ╲      E2E Tests (11 tests)
      ╱    ╲     Full API flow on kind cluster
     ╱──────╲
    ╱        ╲    Integration Tests
   ╱          ╲   Real DB (AGE + pgvector), no mocks
  ╱────────────╲
 ╱              ╲  Unit Tests
╱                ╲ Pure logic, no dependencies
╱──────────────────╲
```

Counting them: `go test ./... -list '.*'` reports 368 Go tests, of which 129
skip unless `KORA_TEST_DATABASE_URL` points at a real Postgres with AGE and
pgvector. The Python SDK adds 28 end-to-end tests against a live engine.

A specific number is deliberately left out of the pyramid above: it was
written as "37+" and stayed there while the count grew to 239, which is worse
than no number at all.

### Test Categories

| Category | Directory | What | Runs on | Coverage target |
|----------|-----------|------|---------|----------------|
| **Unit** | `*_test.go` in each package | Pure logic: ranking, parsing, extraction, auth, config | Every PR | 80%+ |
| **Integration** | `internal/graph/age_test.go` | Real PostgreSQL + AGE: graph CRUD, Cypher injection resistance, vector search, schema init | Every PR (with DB service) | 60%+ |
| **E2E** | `test/e2e/` | Full system on kind: store → extract → query → profile flow | Main branch + release | All API endpoints covered |
| **Retrieval regression** | `test/golden/` | A fixed corpus and fixed queries, with committed floors on recall@10 and MRR | Every PR (with DB service) | Not a coverage measure |

### Running the integration tests

The graph repository builds Cypher by hand, so its tests run against a real
Apache AGE instance rather than a mock. A query can be valid Go and malformed
openCypher, and only the real parser will say so.

```bash
make test-integration
```

That starts the Compose `postgres` service, waits for it, and runs the suite.
To point at a database you already have running:

```bash
KORA_TEST_DATABASE_URL="postgres://kora:kora@localhost:5432/kora?sslmode=disable" \
  go test ./internal/graph/... -count=1
```

Without `KORA_TEST_DATABASE_URL` the suite skips, so plain `go test ./...`
needs no database. Each test scopes its data to a unique project id and cleans
up after itself, so runs are repeatable against a persistent database.

### Running the retrieval regression suite

Changes to ranking, extraction, or the graph can make retrieval quietly worse
without breaking a single unit test. This suite is the tripwire for that: it
stores a fixed corpus through the real `Store` path, runs fixed queries through
the real `Query` path, and fails when recall@10 or MRR falls below the floors
committed in `test/golden/golden_test.go`.

```bash
make test-golden
```

It is deterministic: ten consecutive runs produce identical numbers, because
every retriever's candidate pool is larger than the corpus, so ranking never
has to break a tie on the arbitrary order AGE returns rows in.

It uses the offline bag-of-words embedder, so it needs no credentials and makes
no network call. That makes its numbers a floor, not a measure of quality: on
this corpus full-text search answers nearly everything, and disabling vector or
entity retrieval barely moves the result. Point it at a real embedder when the
question is quality rather than regression:

```bash
KORA_TEST_EMBEDDING_PROVIDER=ollama KORA_TEST_EMBEDDING_MODEL=nomic-embed-text \
KORA_TEST_EMBEDDING_DIM=768 \
KORA_TEST_DATABASE_URL="postgres://kora:$POSTGRES_PASSWORD@localhost:5432/kora?sslmode=disable" \
  go test ./test/golden/... -count=1 -v
```

A provider whose dimension differs from the default 384 needs its own database:
the embedding column is created at the width first seen.

`make test-golden-quality` runs exactly that, against Ollama and
`nomic-embed-text`, and scores it against a **second, higher set of floors**.
The two tiers measure different things:

| tier | embedder | floors (overall) | runs |
|---|---|---|---|
| offline | bag-of-words | recall 0.90, MRR 0.83 | every PR |
| online | nomic-embed-text | recall 0.92, MRR 0.86 | by hand, before a benchmark |

The online tier exists because the offline one cannot see the vector retriever
at all. Delete vector retrieval and the gated suite stays green -- with hashed
bag-of-words there is no paraphrase this corpus can pose that vectors answer
and full-text search does not. With a real embedder there is, and three
paraphrase cases exist to be unreachable lexically. Verified by deleting it:
the online tier fails on four assertions.

When a change legitimately improves retrieval, raise the floors in the same
commit, so the gain cannot be given back silently later. When a change lowers
them, the commit message has to say which behaviour was traded away and why.

### Benchmarking

`scripts/bench_api.py` measures through the public REST API. Seed first with
`scripts/seed_corpus.py`, which stores through the API so embeddings are
generated:

```bash
docker compose up -d
scripts/seed_corpus.py --count 5000
scripts/bench_api.py
scripts/bench_write.py
```

`bench_write.py` measures `POST /v1/memories`, split by memory type and by
whether tags are present, because only semantic memories run contradiction
detection and only tagged ones run auto-linking. That separation is what makes
it obvious which stage is responsible when writes get slower.

Do **not** seed by writing Cypher directly into AGE for a benchmark. It is much
faster, but it leaves `public.memory_embeddings` empty, so `SearchByVector`
matches nothing and the entire hybrid retrieval path silently does not run. An
earlier round of this work measured exactly that and reported query latencies
that excluded the vector half of the engine.

### Verifying the Helm chart

`helm lint` and `helm template` check that YAML renders. They cannot tell you
whether a pod starts, whether `readOnlyRootFilesystem` breaks the container, or
whether a probe path exists. Deploy and check the running cluster:

```bash
make kind-up
for img in kora/kora:dev kora/postgres-age-vector:dev kora/web:dev; do
  kind load docker-image "$img" --name kora-dev
done
helm install kora ./charts/kora -n kora --create-namespace \
  --set postgres.password="$(openssl rand -hex 16)" \
  --set auth.apiKeys="$(go run ./cmd/cli keys generate)"
KORA_API_KEY=<the key above> scripts/verify_k8s.sh
```

The script maps each requirement to a check against the live cluster: probe
wiring and responses, the security context enforced at runtime, `GOMEMLIMIT`
and pool sizing reaching the process, Postgres tuning, the property indexes,
the public API round-trip, and the database-outage failure mode. It exits
non-zero on any failure.

Note that its last section deliberately scales Postgres to zero and back, so
run it against a throwaway cluster rather than anything you care about.

### Coverage Requirements

| Package | Minimum | Current |
|---------|---------|---------|
| `internal/auth` | 80% | 85% |
| `internal/config` | 80% | 97.6% |
| `internal/ranking` | 80% | 96% |
| `internal/extraction` | 80% | ~85% |
| `internal/embedding` | 80% | ~90% |
| `internal/service` | 60% | 21.8% (needs integration tests) |
| `internal/graph` | 60% | 0% (needs integration tests) |

### When to write tests

1. **New feature**: write unit tests in the same PR
2. **Bug fix**: write a test that reproduces the bug first, then fix
3. **Refactor**: ensure existing tests still pass, add if coverage drops
4. **API change**: update E2E tests

---

## Release Checklist

### Before cutting a release

- [ ] All CI checks pass on main
- [ ] E2E tests pass on kind cluster
- [ ] No open P0/P1 bugs for this version
- [ ] CHANGELOG.md updated
- [ ] Version bumped in:
  - [ ] `charts/kora/Chart.yaml` (appVersion + version)
  - [ ] `sdk/python/pyproject.toml` (version)
  - [ ] `mcp-server/pyproject.toml` (version)
  - [ ] `web/package.json` (version)

> `internal/config/config.go` is deliberately not on that list. `DefaultVersion`
> is stamped at link time by the release workflow from the tag, so editing it by
> hand would be overwritten. See [RELEASING.md](../RELEASING.md).

### Release process

```bash
# 1. Ensure main is clean
git checkout main && git pull

# 2. Cut release branch
git checkout -b release/v0.2.0

# 3. Bump versions
# (edit files above)

# 4. Commit version bump
git commit -am "chore: bump version to v0.2.0"

# 5. Tag
git tag v0.2.0

# 6. Push (triggers CI/CD release pipeline)
git push origin release/v0.2.0 --tags

# 7. Merge release branch back to main
git checkout main
git merge release/v0.2.0
git push origin main
```

---

## Repository Structure (Final)

```
kora/
├── .github/
│   └── workflows/
│       ├── ci.yaml              # Lint, test, build on every PR
│       ├── e2e.yaml             # E2E tests on main
│       ├── release.yaml         # Build + publish on tag push
│       └── security.yaml        # Weekly security scan
├── api/
│   ├── proto/                   # Proto definitions (source of truth)
│   └── gen/                     # Generated Go code (gitignored)
├── charts/
│   └── kora/
│       ├── values.yaml          # All values, documented inline
│       └── templates/           # Manifests, NetworkPolicy, ServiceAccounts
├── cmd/
│   ├── server/                  # API server
│   ├── consolidate/             # Consolidation CronJob
│   └── cli/                     # CLI tool
├── docker/
│   └── postgres-age-vector/     # Custom PG image
├── docs/
│   ├── COMPETITIVE_ANALYSIS.md
│   └── DEVELOPMENT.md           # This file
├── internal/
│   ├── auth/                    # API key + rate limiting
│   ├── config/                  # Env-based configuration
│   ├── embedding/               # Embedder interface + implementations
│   ├── extraction/              # Memory extraction (rule-based + LLM)
│   ├── graph/                   # Graph repository (AGE + pgvector)
│   ├── llm/                     # LLM provider interface + implementations
│   ├── metrics/                 # Prometheus metrics
│   ├── ranking/                 # Scoring + ranking
│   └── service/                 # gRPC service handlers
├── pkg/
│   └── model/                   # Shared domain types
├── sdk/
│   └── python/                  # Python SDK
├── mcp-server/                  # MCP server (Claude Code, Cursor, Windsurf)
├── scripts/
│   ├── backup.sh                # Dump, restore, verify
│   ├── demo.sh                  # Full demo on kind
│   ├── migrate_rename.sh        # Context0 -> Kora role/database rename
│   ├── verify_docs.sh           # Docs describe settings that exist
│   ├── verify_install.sh        # The documented install paths work
│   └── teardown.sh              # Cleanup
├── test/
│   ├── e2e/                     # E2E tests (build tag: e2e)
│   └── golden/                  # Retrieval regression suite (fixed corpus + floors)
├── web/                         # React web UI
├── .gitignore
├── ARCHITECTURE.md              # What ships
├── docs/vision.md               # What does not (yet)
├── CHANGELOG.md                 # Maintained per release
├── Dockerfile
├── LICENSE                      # Apache 2.0
├── Makefile
├── MVP.md
├── PROJECT.md
└── README.md
```

---

## Current State → Next Steps

> Counts and checkboxes here are corrected as of the Kora rename. The previous
> version of this section claimed 37 unit tests against an actual 239, and
> listed the CI pipeline, README, LICENSE, CHANGELOG, Ollama embeddings, and
> contradiction detection as outstanding when all of them had shipped.
> `CHANGELOG.md` is the authority for what is in a release; this is orientation.

### What we have

- [x] Core engine (Go, gRPC + REST)
- [x] Apache AGE graph DB
- [x] pgvector hybrid search
- [x] Auto memory extraction, with contradiction detection
- [x] User profiles
- [x] CLI tool
- [x] Python SDK, with 28 end-to-end tests against a live engine
- [x] MCP server for Claude Code, Cursor, and Windsurf, with 27 end-to-end tests
- [x] React web UI
- [x] Helm chart, and a published docs site
- [x] 239 Go tests plus 11 E2E tests
- [x] CI/CD pipeline: lint, unit, integration, kind-cluster E2E, security scanning
- [x] Kind cluster deployment
- [x] Backup and restore, with verification
- [x] Structured logging and Prometheus RED metrics
- [x] API key hashing, deny-by-default auth, rate limiting

### Known gaps

- [ ] Docker images published to a registry: the release workflow pushes to
      GHCR on a tag, but no tag has been cut since `v0.1.0`
- [ ] The Python SDK does not reach PyPI; the publish step in `release.yaml`
      is commented out pending a token
- [ ] Trigram and tsvector keyword indexing, see
      [docs/research](research/README.md)
- [ ] Content ingestion (PDF, URLs)
- [ ] LangChain / CrewAI SDK wrappers
- [ ] MemoryBench baseline results; `bench/` was removed as a non-building
      fragment and would need rebuilding
