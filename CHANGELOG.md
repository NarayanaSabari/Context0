# Changelog

All notable changes to Kora will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

113 commits since `v0.1.0`. The bulk of this work is hardening: the engine
gained authentication, observability, backups, and a deployment story it did
not have at `v0.1.0`, and a long series of correctness fixes found by running
it rather than by reading it.

The project was renamed from Context0 to Kora. This is a breaking change for
existing deployments; see **Breaking** below.

### Breaking

- Renamed the project from Context0 to Kora. Environment variables moved from
  `CONTEXT0_*` to `KORA_*`, and the engine now refuses to start when it sees an
  old name rather than falling back to a default -- an unset `KORA_API_KEYS`
  disables authentication entirely, so a silent fallback would have served
  every stored memory unauthenticated.
- The Go module path is now `github.com/NarayanaSabari/Kora`, the gRPC package
  is `kora.v1`, and the Helm chart is `charts/kora`.
- The Postgres role and database default to `kora`. Existing deployments must
  either run `scripts/migrate_rename.sh` or set `postgres.user` and
  `postgres.database` back to `context0`. The AGE graph and its schema keep the
  name `context0` deliberately: renaming those is a data migration with no
  functional benefit.
- The published default API key `ctx0_dev_key_1` was removed. Generate one with
  `go run ./cmd/cli keys generate`.

### Added

- An MCP server (`mcp-server/`) exposing memory as tools to Claude Code,
  Cursor, Windsurf, and any other MCP client. Seven tools, covered by 27
  end-to-end tests against a live engine.
- Pluggable embedding providers: Ollama, OpenAI, and Google, alongside the
  built-in bag-of-words default.
- Domain-agnostic contradiction detection.
- Backup and restore (`scripts/backup.sh`), with dump, restore, and verify.
- Structured logging, Prometheus RED metrics per method, pool saturation
  metrics, and split liveness/readiness/startup probes.
- Docker Compose for local development.
- Production-ready Helm chart defaults: pod identity, network isolation, and
  restricted-profile compliance.
- A landing page and documentation site at
  [kora.sabarinarayana.com](https://kora.sabarinarayana.com), deployed to
  Cloudflare Pages and verified in CI.
- Dependabot across all five package ecosystems, and CI actions pinned to
  commit SHAs.

### Security

- API keys are hashed at rest and compared in constant time; authorization is
  deny-by-default rather than exempting non-`/v1/` routes.
- `/v1/health` no longer discloses the version and data volume to unauthenticated
  callers.
- The web UI no longer leaks API keys into URLs and access logs.
- Shipped credentials removed from the chart and from docker-compose.
- The Google embedding API key is no longer written to logs.

### Fixed

Selected from 37 fix commits; each was reproduced before being fixed.

- Hybrid retrieval did not actually affect result order.
- Scoped vector search silently returned fewer results than matched.
- A disconnected client could leave memories unsearchable and fail everyone
  else's health check.
- A backup holding 0.017% of the graph passed verification.
- A consolidation run whose writes all failed reported success, and bad tuning
  values silently deleted memories on defaults.
- A conversation on a single line was extracted as one polluted memory.
- Ending a session twice drove the active-session gauge negative.
- Cypher is parameterized rather than quote-escaped.
- A REST request spent two rate-limit tokens instead of one.

### Performance

- Property indexes on the graph, ending sequential scans.
- Batched edge creation and labelled edge endpoints on the write path.
- Vertex and edge counts read from AGE's base tables rather than through
  Cypher, and cached on `/v1/health`.

### Changed

- The raw Kubernetes manifests in `deploy/` were removed; the Helm chart is the
  single source of deployment truth.
- The MemoryBench provider and `bench/` were removed as a non-building
  fragment.

## [0.1.0] - 2026-04-01

Initial release.

### Added

- Core memory engine with gRPC + REST API (grpc-gateway).
- Apache AGE graph database for relationship-aware memory storage.
- pgvector hybrid search (graph traversal + vector similarity).
- Auto memory extraction from raw conversations.
- User profiles (static facts + dynamic recent events).
- Memory types: semantic, episodic, procedural.
- Relationship types: relates_to, supersedes, caused_by, contains, belongs_to.
- API key authentication with token bucket rate limiting.
- Consolidation pipeline (merge, decay, prune) via Kubernetes CronJob.
- Ranking scorer: recency decay, frequency boost, edge weight, type priority.
- Query parser: keyword extraction, time filtering, Cypher query builder.
- React web UI with React Flow graph visualization.
- Python SDK and a CLI (store, query, connect, delete, graph, stats, sessions).
- Helm chart, and a custom PostgreSQL 18 + Apache AGE + pgvector image.
- Prometheus metrics endpoint.

[Unreleased]: https://github.com/NarayanaSabari/Kora/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/NarayanaSabari/Kora/releases/tag/v0.1.0
