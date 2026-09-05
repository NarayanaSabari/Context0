# kora

**Memory for AI agents.** Graph-first, self-hosted, Apache 2.0.

Every agent framework has the same hole in it: agents are amnesiac. A session ends and everything the agent learned about you, your project, and your preferences goes with it. The next session starts from nothing.

kora is a memory engine that closes that hole. Feed it conversations, and it extracts what is worth keeping, stores it as a graph of typed memories, and gives it back when it is relevant. It runs in your own cluster, on PostgreSQL, and every dependency is OSI-approved open source.

> **kora is pre-release.** The engine, the API, the Helm chart, and the Python SDK all work today, and the pages here describe what actually ships. The API is not frozen: it may change before 1.0. [Join the waitlist](/) to be told when it does.

## What makes it different

Most memory layers are a vector database with a system prompt in front. That gets you similarity, and nothing else. kora stores relationships as first-class data, which buys three things a flat vector store cannot do:

- **It knows what replaced what.** When you say "we moved off MySQL", the old memory is not overwritten, it is *superseded*. The current answer is correct, and the history stays queryable.
- **It knows why a result is relevant.** Every result comes back with the edges that connect it to its neighbours, so an agent can explain its own recall instead of asserting it.
- **It retrieves by structure as well as by meaning.** Graph traversal and vector similarity run against the same PostgreSQL instance and are ranked together, so a memory one hop away from a strong match still surfaces.

## Where to go next

| If you want to | Read |
| --- | --- |
| See it working in about five minutes | [Quick start](quickstart.md) |
| Run it properly, on Kubernetes | [Installation](installation.md) |
| Understand memories, edges, and superseding | [Concepts](concepts.md) |
| Add long-term memory to an agent | [Agent integration](agent-integration.md) |
| Study a complete Razorpay agent | [Razorpay agent](razorpay-agent.md) |
| Call it from your own code | [API reference](api.md), [SDKs and CLI](clients.md) |
| Know how it works inside | [Architecture](architecture.md) |
| Operate it: tuning, metrics, backups | [Configuration](configuration.md), [Operations](operations.md) |

## At a glance

| Component | Technology | License |
| --- | --- | --- |
| Engine | Go | BSD-3-Clause |
| Graph | Apache AGE (a PostgreSQL extension) | Apache 2.0 |
| Vector search | pgvector | PostgreSQL |
| Database | PostgreSQL 18 | PostgreSQL |
| API | gRPC, with a REST gateway | Apache 2.0 |
| Web UI | React, React Flow, Tailwind | MIT |
| Metrics | Prometheus | Apache 2.0 |

No SSPL, no BSL, no proprietary components, no hosted-only features. What is in the repository is the whole product.
