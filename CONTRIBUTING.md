# Contributing to Kora

Thank you for your interest in contributing to Kora! This guide will help you get started.

## Code of Conduct

By participating in this project, you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md). Please read it before contributing.

## How to Contribute

### Reporting Issues

- **Bug reports**: Use the [Bug Report](https://github.com/NarayanaSabari/Kora/issues/new?template=bug_report.md) template
- **Feature requests**: Use the [Feature Request](https://github.com/NarayanaSabari/Kora/issues/new?template=feature_request.md) template
- **Questions**: Open a [Discussion](https://github.com/NarayanaSabari/Kora/discussions)

Before opening an issue, please search existing issues to avoid duplicates.

### Pull Requests

1. **Fork** the repository
2. **Create a branch** from `main`:
   ```bash
   git checkout -b feat/your-feature main
   ```
3. **Make your changes** following our coding standards (see below)
4. **Write tests** for new functionality
5. **Run the test suite**:
   ```bash
   make test      # Unit tests
   make lint      # Linting
   go vet ./...   # Static analysis
   ```
6. **Commit** using conventional commit messages:
   ```
   feat: add Ollama embedding provider
   fix: handle empty query in search endpoint
   docs: update API reference for extract endpoint
   test: add integration tests for pgvector search
   ```
7. **Push** and open a PR against `main`

### PR Requirements

All PRs must:
- [ ] Pass CI checks (lint, test, build)
- [ ] Maintain or improve test coverage (80%+ for new packages)
- [ ] Include tests for new functionality
- [ ] Follow the coding standards below
- [ ] Have a clear description of what changed and why
- [ ] Reference any related issues

## Development Setup

### Prerequisites

- Go 1.26+
- Docker
- kind (for local Kubernetes)
- kubectl + Helm 3.x
- pnpm (for web UI)
- protoc + plugins (for proto changes)

### Getting Started

```bash
# Clone
git clone https://github.com/NarayanaSabari/Kora.git
cd kora

# Install Go dependencies
go mod download

# Build
make build

# Run tests
make test

# Start a local kind cluster with everything running
./scripts/demo.sh
```

### Running Locally

```bash
# Option 1: Docker Compose (simplest)
# Generate credentials once, then start everything:
echo "POSTGRES_PASSWORD=$(openssl rand -hex 16)" >> .env
echo "KORA_API_KEYS=$(go run ./cmd/cli keys generate)" >> .env
docker compose up

# Option 2: kind cluster
make kind-up
make deploy

# Option 3: Local PostgreSQL + AGE
# Start PostgreSQL with AGE extension, then:
KORA_DATABASE_URL="postgres://user:pass@localhost:5432/kora" make run
```

## Coding Standards

### Go

- **Format**: `gofmt` and `goimports` (enforced by CI)
- **Lint**: `golangci-lint` (config in `.golangci.yml`)
- **Vet**: `go vet ./...`
- **Tests**: Table-driven tests with `-race` flag
- **Errors**: Wrap with context using `fmt.Errorf("operation: %w", err)`
- **Interfaces**: Accept interfaces, return structs. Define interfaces where they're used.
- **Naming**: Follow [Effective Go](https://go.dev/doc/effective_go) conventions

### Proto

- Follow the [Protocol Buffers Style Guide](https://protobuf.dev/programming-guides/style/)
- Every RPC must have HTTP annotations for grpc-gateway
- Run `make proto-gen` after any `.proto` changes

### TypeScript (Web UI)

- **Format**: Prettier (enforced by CI)
- **Lint**: ESLint with TypeScript plugin
- **Types**: Strict mode, no `any`

### Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>: <description>

<optional body>
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `ci`, `chore`

## What to Work On

### Good First Issues

Look for issues labeled [`good first issue`](https://github.com/NarayanaSabari/Kora/labels/good%20first%20issue). These are beginner-friendly tasks.

### Areas Where Help is Needed

| Area | What | Skill Level |
|------|------|-------------|
| **Tests** | Integration tests for `internal/graph`, `internal/service` | Intermediate |
| **Embeddings** | Ollama provider implementation | Intermediate |
| **SDKs** | LangChain, CrewAI integration wrappers | Intermediate |
| **Connectors** | GitHub, Notion, Google Drive ingestion | Advanced |
| **Docs** | API reference, tutorials, examples | Any |
| **Web UI** | Graph visualization improvements | Intermediate |
| **Benchmarks** | Run MemoryBench, analyze results | Intermediate |

### Architecture Decisions

For significant changes (new features, API changes, architectural shifts), please:

1. Open a GitHub Discussion first
2. Describe the problem and proposed solution
3. Wait for maintainer feedback before implementing
4. Large changes should be broken into smaller PRs

## Testing

### Test Categories

| Category | How to Run | When |
|----------|-----------|------|
| Unit tests | `go test ./... -race` | Every PR |
| Integration tests | `make test-integration` | Every PR (starts Postgres + AGE) |
| Retrieval regression | `make test-golden` | Every PR, and before any ranking or extraction change |
| E2E tests | `go test ./test/e2e/... -tags=e2e` | Needs running server |
| Linting | `make lint` | Every PR |
| Coverage | `go test ./... -cover` | Check before submitting |

### Writing Tests

- Unit tests go in the same package as the code (`*_test.go`)
- Integration tests go beside the code they exercise and skip unless
  `KORA_TEST_DATABASE_URL` is set, as `internal/graph/age_test.go` does
- Retrieval cases go in `test/golden/golden.json`; adding one needs no Go
- E2E tests go in `test/e2e/` with build tag `//go:build e2e`
- Use table-driven tests for multiple cases
- Test error paths, not just happy paths
- No mocks for `internal/graph` -- use real PostgreSQL + AGE

## Release Process

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for the full release process.

Releases are cut from `main` using tags (`v0.1.0`, `v0.2.0`). Contributors don't need to worry about releases -- maintainers handle this.

## Getting Help

- **GitHub Issues**: For bugs and feature requests
- **GitHub Discussions**: For questions and architecture discussions
- **Code Review**: Maintainers will review and provide feedback on PRs

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
