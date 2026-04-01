# Context0 — Development Workflow & Release Strategy

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
- Docker images: `context0/context0:v0.2.0`, `context0/context0:latest`
- Helm chart version: `0.2.0`
- Python SDK: `context0==0.2.0` on PyPI
- GitHub Release with changelog and binaries

### Release Cadence

| Phase | Cadence | What |
|-------|---------|------|
| Pre-launch (now) | Every 2-4 weeks | Minor releases (v0.1, v0.2, v0.3...) |
| Post-launch (v1.0+) | Monthly | Minor releases with features |
| Patches | As needed | Bug fixes, security patches |

---

## Two Deployment Modes

Context0 ships in two configurations from the **same codebase, same branch**.
No separate branches — configuration controls the mode.

### Mode 1: Local (Development / Self-Hosted Minimal)

For developers trying Context0, local testing, or small self-hosted deployments.

```yaml
# values-local.yaml
mode: local

postgres:
  image: context0/postgres-age-vector:latest
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
  enabled: false            # No auth for local dev
```

**Install:**
```bash
helm install context0 ./charts/context0 -f charts/context0/values-local.yaml -n context0 --create-namespace
```

### Mode 2: Production (Enterprise / Managed)

For production deployments with HA, real embeddings, monitoring, and security.

```yaml
# values-production.yaml
mode: production

postgres:
  image: context0/postgres-age-vector:latest
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
    secretName: context0-api-keys

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
helm install context0 ./charts/context0 -f charts/context0/values-production.yaml -n context0 --create-namespace
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

#### Job 3: Build
```yaml
- go build ./cmd/server ./cmd/consolidate ./cmd/cli
- docker build -t context0/context0:ci .
- docker build -t context0/web:ci ./web
- docker build -t context0/postgres-age-vector:ci ./docker/postgres-age-vector
- helm lint ./charts/context0
```

#### Job 4: E2E Tests (on main only, not every PR)
```yaml
- Create kind cluster
- Deploy all components
- Run go test ./test/e2e/... -tags=e2e
- Teardown
```

#### Job 5: Security Scan
```yaml
- gosec ./...
- trivy image context0/context0:ci
- trivy image context0/postgres-age-vector:ci
```

### CD Jobs (Run on tag push v*)

#### Job 6: Release
```yaml
triggers: push tag v*.*.*

steps:
  - Build multi-arch Docker images (amd64 + arm64)
  - Push to Docker Hub:
      context0/context0:v0.2.0
      context0/context0:latest
      context0/web:v0.2.0
      context0/postgres-age-vector:v0.2.0
  - Package and push Helm chart to OCI registry
  - Build and publish Python SDK to PyPI
  - Create GitHub Release with:
      - Changelog (auto-generated from conventional commits)
      - Binary artifacts (context0-cli for linux/mac/windows)
      - SHA256 checksums
```

---

## Testing Strategy

### Test Pyramid

```
        ╱╲
       ╱  ╲      E2E Tests (7 tests)
      ╱    ╲     Full API flow on kind cluster
     ╱──────╲
    ╱        ╲    Integration Tests
   ╱          ╲   Real DB (AGE + pgvector), no mocks
  ╱────────────╲
 ╱              ╲  Unit Tests (37+ tests)
╱                ╲ Pure logic, no dependencies
╱──────────────────╲
```

### Test Categories

| Category | Directory | What | Runs on | Coverage target |
|----------|-----------|------|---------|----------------|
| **Unit** | `*_test.go` in each package | Pure logic: ranking, parsing, extraction, auth, config | Every PR | 80%+ |
| **Integration** | `test/integration/` | Real PostgreSQL + AGE: graph CRUD, vector search, schema init | Every PR (with DB service) | 60%+ |
| **E2E** | `test/e2e/` | Full system on kind: store → extract → query → profile flow | Main branch + release | All API endpoints covered |
| **Benchmark** | `bench/` | MemoryBench (LoCoMo, LongMemEval) performance comparison | Manual + release | Track score over time |

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
  - [ ] `internal/config/config.go` (default version)
  - [ ] `charts/context0/Chart.yaml` (appVersion + version)
  - [ ] `sdk/python/pyproject.toml` (version)
  - [ ] `web/package.json` (version)

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
context0/
├── .github/
│   └── workflows/
│       ├── ci.yaml              # Lint, test, build on every PR
│       ├── e2e.yaml             # E2E tests on main
│       ├── release.yaml         # Build + publish on tag push
│       └── security.yaml        # Weekly security scan
├── api/
│   ├── proto/                   # Proto definitions (source of truth)
│   └── gen/                     # Generated Go code (gitignored)
├── bench/                       # MemoryBench fork with Context0 provider
├── charts/
│   └── context0/
│       ├── values.yaml          # Default values
│       ├── values-local.yaml    # Local/dev override
│       └── values-production.yaml # Production override
├── cmd/
│   ├── server/                  # API server
│   ├── consolidate/             # Consolidation CronJob
│   └── cli/                     # CLI tool
├── deploy/                      # Raw K8s manifests for kind/dev
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
├── scripts/
│   ├── demo.sh                  # Full demo on kind
│   └── teardown.sh              # Cleanup
├── test/
│   ├── e2e/                     # E2E tests (build tag: e2e)
│   └── integration/             # Integration tests (need real DB)
├── web/                         # React web UI
├── .gitignore
├── ARCHITECTURE.md
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

### What we have (v0.1.0-dev)

- [x] Core engine (Go, gRPC + REST)
- [x] Apache AGE graph DB
- [x] pgvector hybrid search
- [x] Auto memory extraction
- [x] User profiles
- [x] CLI tool
- [x] Python SDK
- [x] React web UI
- [x] Helm chart
- [x] 37 unit tests + 7 E2E tests
- [x] Kind cluster deployment

### v0.1.0 release requirements

- [ ] CI/CD pipeline (GitHub Actions)
- [ ] Integration tests for graph + pgvector
- [ ] README.md with quickstart
- [ ] LICENSE file (Apache 2.0)
- [ ] CHANGELOG.md
- [ ] values-local.yaml + values-production.yaml
- [ ] Docker images pushed to registry

### v0.2.0 goals

- [ ] Ollama embedding integration
- [ ] Contradiction detection on extraction
- [ ] MemoryBench baseline results
- [ ] Content ingestion (PDF, URLs)
- [ ] LangChain / CrewAI SDK wrappers
