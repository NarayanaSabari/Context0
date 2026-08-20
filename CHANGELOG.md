# Changelog

All notable changes to Kora will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Core memory engine with gRPC + REST API (grpc-gateway)
- Apache AGE graph database for relationship-aware memory storage
- pgvector hybrid search (graph traversal + vector similarity)
- Auto memory extraction from raw conversations (rule-based + LLM-ready)
- User profiles (static facts + dynamic recent events)
- Memory types: semantic (facts), episodic (events), procedural (how-to)
- Relationship types: relates_to, supersedes, caused_by, contains, belongs_to
- API key authentication with token bucket rate limiting
- Consolidation pipeline (merge, decay, prune) via K8s CronJob
- Ranking scorer: recency decay, frequency boost, edge weight, type priority
- Query parser: keyword extraction, time filtering, Cypher query builder
- React web UI with React Flow graph visualization
- Python SDK (REST-based, no proto codegen needed)
- CLI tool (store, query, connect, delete, graph, stats, sessions)
- Helm chart with local and production value files
- Custom Docker image: PostgreSQL 18 + Apache AGE 1.7.0 + pgvector 0.8.2
- Prometheus metrics endpoint (/metrics)
- Kind cluster deployment scripts (demo.sh, teardown.sh)
- MemoryBench provider for benchmarking against Supermemory, Mem0, Zep
- 37 unit tests + 7 E2E tests
- Competitive analysis vs Supermemory
- Development workflow documentation (branching, CI/CD, releases)
