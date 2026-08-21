# Contributing

kora is Apache 2.0 and developed in the open. The authoritative guides live in the repository - [CONTRIBUTING.md](https://github.com/NarayanaSabari/Kora/blob/main/CONTRIBUTING.md) and [docs/DEVELOPMENT.md](https://github.com/NarayanaSabari/Kora/blob/main/docs/DEVELOPMENT.md) - and this page is the short version.

## Getting set up

```bash
git clone https://github.com/NarayanaSabari/Kora.git
cd Kora

make build      # all binaries
make test       # unit tests
make lint
go vet ./...
```

Running the engine locally needs PostgreSQL with Apache AGE. `make run` expects one; `docker compose up` provides one, as does `make kind-up && make deploy` for a real cluster.

## Branching

Trunk-based, with short-lived branches off `main`. `main` is protected: PR only, CI green, one approval.

| Prefix | For |
| --- | --- |
| `feat/` | New features |
| `fix/` | Bug fixes |
| `refactor/` | Restructuring, no behaviour change |
| `docs/` | Documentation only |
| `ci/` | Pipeline changes |
| `perf/` | Performance |

Commits follow [Conventional Commits](https://www.conventionalcommits.org/). PRs are squash-merged, so the PR title becomes the commit message: write it accordingly.

## Verifying a change

`go test` is the floor, not the bar. The repository carries checks that run against a deployed cluster rather than a mock, because this system's interesting failures are not reproducible in a unit test: connection pool exhaustion, CPU throttling, HNSW index builds running out of shared memory, backups that restore almost nothing.

End-to-end, against a real deployment:

```bash
make kind-up
make deploy

. ./.dev-credentials
KORA_E2E_HTTP=http://localhost:8080 \
KORA_E2E_API_KEY="$DEV_API_KEY" \
go test ./test/e2e/... -v -tags=e2e
```

There are also soak and mutation testing setups documented in [docs/soak-testing.md](https://github.com/NarayanaSabari/Kora/blob/main/docs/soak-testing.md) and [docs/mutation-testing.md](https://github.com/NarayanaSabari/Kora/blob/main/docs/mutation-testing.md).

## The API is generated

`api/proto/kora/v1` is the source of truth for both gRPC and REST. Changing an endpoint means changing the proto and regenerating, never hand-editing `api/gen/`. Generated code is committed, which is standard Go practice.

## Reporting things

- **Bugs, features, and questions**: [issues](https://github.com/NarayanaSabari/Kora/issues), using the templates where they fit
- **Security**: do not open a public issue. See [SECURITY.md](https://github.com/NarayanaSabari/Kora/blob/main/SECURITY.md)

## Dependencies

Every dependency must be OSI-approved. No SSPL, no BSL, no proprietary components. This is a hard constraint, not a preference: it is the reason kora can be self-hosted without a licensing conversation.

## Next

- [Architecture](architecture.md) - the layout before you change it
- [FAQ](faq.md)
