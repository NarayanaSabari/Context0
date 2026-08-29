# Kora — Vision

Designs that are **not implemented**. Nothing here describes running code.

This file exists because [ARCHITECTURE.md](ARCHITECTURE.md) used to hold both,
at the same visual weight, with a banner on some sections and not others. A
reader learned the data model from a diagram of node types the engine does not
have, and per-section banners had already failed twice to prevent that.

The rule now: ARCHITECTURE.md describes what ships and can be checked against
the code. Anything aspirational lives here, and moving something out of this
file is part of building it.

None of this is scheduled. Where a design was actively retired, that is stated.

---

## Retired: Kubernetes Operator and CRDs

**Status: retired, 2026-08-29.**

Helm plus the consolidation CronJob covers what this was drawn to do. An
operator earns its cost when many Kora instances need lifecycle management,
which is a problem no user has had yet, and keeping it on the roadmap made the
project look further from its own goals than it is.

Kept for the reasoning, not as a plan.

### The design as drawn

> **Status: design sketch, not implemented.**
> No CRDs and no operator exist in this repo today.
> Deployment is via the Helm chart in `charts/kora/`.
> Everything below describes intended future shape, not current behaviour.

How Kora is managed as Kubernetes-native resources.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                   CUSTOM RESOURCE DEFINITIONS                            │
│                                                                          │
│  apiVersion: kora.io/v1alpha1                                        │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  Kind: MemoryStore                                                 │  │
│  │                                                                    │  │
│  │  The core resource. One per tenant/project.                        │  │
│  │                                                                    │  │
│  │  spec:                                                             │  │
│  │    graphDB:                                                        │  │
│  │      engine: apache-age          # fully open source (Apache 2.0)                         │  │
│  │      replicas: 3                                                   │  │
│  │      storage: 50Gi                                                 │  │
│  │      resources:                                                    │  │
│  │        memory: 4Gi                                                 │  │
│  │        cpu: 2                                                      │  │
│  │    api:                                                            │  │
│  │      replicas: 2                                                   │  │
│  │      grpc: true                                                    │  │
│  │      rest: true                                                    │  │
│  │    embedding:                                                      │  │
│  │      enabled: true                                                 │  │
│  │      model: "bge-small-en-v1.5"                                    │  │
│  │                                                                    │  │
│  │  status:                                                           │  │
│  │    phase: Running                                                  │  │
│  │    nodeCount: 12,847                                               │  │
│  │    edgeCount: 45,231                                               │  │
│  │    lastConsolidation: "2024-03-28T06:00:00Z"                       │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  Kind: MemoryPolicy                                                │  │
│  │                                                                    │  │
│  │  Retention, access, and isolation rules.                           │  │
│  │                                                                    │  │
│  │  spec:                                                             │  │
│  │    retention:                                                      │  │
│  │      episodic: 90d          # auto-archive after 90 days          │  │
│  │      stale: 30d             # delete stale nodes after 30 days    │  │
│  │      orphan: 7d             # delete orphans after 7 days         │  │
│  │    isolation:                                                      │  │
│  │      level: project         # project | user | agent              │  │
│  │      networkPolicy: true    # enforce K8s NetworkPolicy           │  │
│  │    access:                                                         │  │
│  │      maxTraversalDepth: 5                                          │  │
│  │      maxResultsPerQuery: 20                                        │  │
│  │      rateLimitPerMinute: 100                                       │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │  Kind: ConsolidationSchedule                                       │  │
│  │                                                                    │  │
│  │  Controls the "sleep" cycle — when and how memory is consolidated. │  │
│  │                                                                    │  │
│  │  spec:                                                             │  │
│  │    schedule: "0 */6 * * *"  # every 6 hours                       │  │
│  │    phases:                                                         │  │
│  │      merge:                                                        │  │
│  │        enabled: true                                               │  │
│  │        similarityThreshold: 0.85                                   │  │
│  │      promote:                                                      │  │
│  │        enabled: true                                               │  │
│  │        minOccurrences: 3    # promote after 3 similar episodes    │  │
│  │      decay:                                                        │  │
│  │        enabled: true                                               │  │
│  │        staleThreshold: 0.2                                         │  │
│  │        halfLifeDays: 30     # decay halves every 30 days          │  │
│  │      prune:                                                        │  │
│  │        enabled: true                                               │  │
│  │        dryRun: false                                               │  │
│  │    llm:                                                            │  │
│  │      provider: anthropic                                           │  │
│  │      model: claude-haiku-4-5                                       │  │
│  │      budgetPerRun: "$0.50"                                         │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘


┌─────────────────────────────────────────────────────────────────────────┐
│                   KORA OPERATOR — Reconciliation Loop                │
│                                                                          │
│      ┌──────────────┐                                                    │
│      │  Watch CRDs  │◀─────────────────────────────────────┐            │
│      └──────┬───────┘                                      │            │
│             │                                               │            │
│             ▼                                               │            │
│      ┌──────────────┐     ┌───────────────┐     ┌─────────┴──────┐     │
│      │ MemoryStore  │────▶│ Deploy/Scale  │────▶│ Update Status  │     │
│      │ changed?     │     │ • PG + AGE    │     │ • phase        │     │
│      │              │     │   StatefulSet │     │ • nodeCount    │     │
│      │              │     │ • API Server  │     │ • health       │     │
│      │              │     │   Deployment  │     │                │     │
│      └──────────────┘     │ • PVCs        │     └────────────────┘     │
│                           │ • Services    │                             │
│      ┌──────────────┐     └───────────────┘                             │
│      │ MemoryPolicy │────▶ Apply NetworkPolicy, update rate limits      │
│      │ changed?     │                                                    │
│      └──────────────┘                                                    │
│                                                                          │
│      ┌──────────────┐                                                    │
│      │ Consolidation│────▶ Create/Update CronJob with schedule          │
│      │ Schedule     │                                                    │
│      │ changed?     │                                                    │
│      └──────────────┘                                                    │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

---

## Not implemented: multi-scope shared memory

**Status: designed, not built.**

The engine has one scope: `project_id`. There is no agent scope, no global
scope, and no promotion between them. Queries are answered within a project or,
when it is omitted, across all of them -- see
[ADR 0002](docs/adr/0002-one-deployment-is-one-trust-domain.md), which settles
why an API key is not bound to a project.

What follows is the layered model that was drawn before that decision was made.
It would need key-to-project binding and a scope-aware query path to mean
anything.

### The design as drawn

How multiple agents share and isolate memory within the graph.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        MEMORY SCOPING                                    │
│                                                                          │
│  Three levels of memory visibility:                                      │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐  │
│  │                                                                    │  │
│  │  GLOBAL SCOPE (shared across all projects in a tenant)             │  │
│  │  ┌─────────────────────────────────────────────────────────────┐  │  │
│  │  │  • User preferences ("prefers TypeScript", "concise replies") │  │  │
│  │  │  • Organization standards ("use conventional commits")       │  │  │
│  │  │  • Cross-project patterns                                    │  │  │
│  │  └─────────────────────────────────────────────────────────────┘  │  │
│  │                                                                    │  │
│  │  ┌──────────────────────────┐  ┌──────────────────────────┐      │  │
│  │  │  PROJECT SCOPE           │  │  PROJECT SCOPE            │      │  │
│  │  │  "backend-api"           │  │  "mobile-app"             │      │  │
│  │  │                          │  │                           │      │  │
│  │  │  • Project facts         │  │  • Project facts          │      │  │
│  │  │  • Architecture decisions│  │  • Architecture decisions │      │  │
│  │  │  • Team patterns         │  │  • Team patterns          │      │  │
│  │  │                          │  │                           │      │  │
│  │  │  ┌──────────┐ ┌────────┐│  │  ┌──────────┐ ┌────────┐│      │  │
│  │  │  │ AGENT    │ │ AGENT  ││  │  │ AGENT    │ │ AGENT  ││      │  │
│  │  │  │ SCOPE    │ │ SCOPE  ││  │  │ SCOPE    │ │ SCOPE  ││      │  │
│  │  │  │ Claude   │ │ Cursor ││  │  │ CrewAI   │ │ Claude ││      │  │
│  │  │  │ Code     │ │        ││  │  │ worker   │ │ Code   ││      │  │
│  │  │  │          │ │        ││  │  │          │ │        ││      │  │
│  │  │  │• Session │ │• Session││  │  │• Session │ │• Session││      │  │
│  │  │  │  history │ │  history││  │  │  history │ │  history││      │  │
│  │  │  │• Agent-  │ │• Agent- ││  │  │• Agent-  │ │• Agent- ││      │  │
│  │  │  │  specific│ │  specific││  │  │  specific│ │  specific││      │  │
│  │  │  │  context │ │  context││  │  │  context │ │  context││      │  │
│  │  │  └──────────┘ └────────┘│  │  └──────────┘ └────────┘│      │  │
│  │  │                          │  │                           │      │  │
│  │  └──────────────────────────┘  └──────────────────────────┘      │  │
│  │                                                                    │  │
│  └───────────────────────────────────────────────────────────────────┘  │
│                                                                          │
│  Query resolution order:                                                 │
│  1. Agent scope   → check agent's own memories first                     │
│  2. Project scope → then project-wide shared memories                    │
│  3. Global scope  → finally tenant-wide memories                         │
│                                                                          │
│  Write visibility:                                                       │
│  • Agent writes default to PROJECT scope (shared)                        │
│  • Agent can explicitly write to AGENT scope (private)                   │
│  • Only admins/operators can write to GLOBAL scope                       │
│  • Consolidation can promote agent → project → global                    │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

---

---

## Not implemented: production deployment topology

**Status: target topology.**

What ships is a single-replica API, a Postgres StatefulSet, and a consolidation
CronJob, all in `charts/kora`. The multi-zone, HA, autoscaled topology below is
where that would go if demand asked for it.

### The topology as drawn

> **Status: target topology, not implemented.**
> The shipped chart runs a single-replica Postgres StatefulSet, one API
> Deployment, a consolidation CronJob, and the web UI. CloudNativePG,
> read replicas, HPA, the sidecar cache, the embedding worker, and the
> OTel collector below are all future work.

```
┌─── Region: us-east-1 ────────────────────────────────────────────────────┐
│                                                                           │
│  ┌─── K8s Cluster ─────────────────────────────────────────────────────┐ │
│  │                                                                      │ │
│  │  Namespace: kora-system                                          │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ kora-operator│  │ kora-api-0   │  │ kora-api-1   │  │ │
│  │  │ (Deployment 1/1) │  │ (Deployment 2/2) │  │ (HPA: 2-10)     │  │ │
│  │  │                  │  │                  │  │                  │  │ │
│  │  │ Watches CRDs,    │  │ gRPC + REST      │  │ gRPC + REST      │  │ │
│  │  │ reconciles state │  │ :50051 / :8080   │  │ :50051 / :8080   │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ pg-age-1         │  │ pg-age-2         │  │ pg-age-3         │  │ │
│  │  │ (CloudNativePG   │  │ (read replica)   │  │ (read replica)   │  │ │
│  │  │  Cluster 3/3)    │  │                  │  │                  │  │ │
│  │  │ Primary (writes) │  │ Replica (reads)  │  │ Replica (reads)  │  │ │
│  │  │ PVC: 100Gi SSD   │  │ PVC: 100Gi SSD   │  │ PVC: 100Gi SSD   │  │ │
│  │  │ Extensions:      │  │                  │  │                  │  │ │
│  │  │  apache_age      │  │                  │  │                  │  │ │
│  │  │  pgvector (v0.2) │  │                  │  │                  │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  │  ┌──────────────────┐  ┌──────────────────┐                        │ │
│  │  │ consolidation    │  │ embedding-worker │                        │ │
│  │  │ (CronJob 0/6h)  │  │ (Deployment 1/1) │                        │ │
│  │  │                  │  │                  │                        │ │
│  │  │ Runs merge,      │  │ Generates node   │                        │ │
│  │  │ promote, decay,  │  │ embeddings async │                        │ │
│  │  │ prune phases     │  │ (optional)       │                        │ │
│  │  └──────────────────┘  └──────────────────┘                        │ │
│  │                                                                      │ │
│  │  Namespace: monitoring                                               │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ prometheus       │  │ grafana          │  │ otel-collector   │  │ │
│  │  │                  │  │                  │  │                  │  │ │
│  │  │ Scrapes metrics  │  │ Dashboards:      │  │ Traces + logs    │  │ │
│  │  │ via              │  │ • Graph health   │  │ collection       │  │ │
│  │  │ ServiceMonitor   │  │ • Query latency  │  │                  │  │ │
│  │  │                  │  │ • Consolidation  │  │                  │  │ │
│  │  │                  │  │ • Memory growth  │  │                  │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  │  Namespace: agent-workloads                                          │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │ │
│  │  │ agent-pod-1      │  │ agent-pod-2      │  │ agent-pod-3      │  │ │
│  │  │ ┌──────────────┐ │  │ ┌──────────────┐ │  │                  │  │ │
│  │  │ │ agent        │ │  │ │ agent        │ │  │  agent           │  │ │
│  │  │ │ container    │ │  │ │ container    │ │  │  (no sidecar,    │  │ │
│  │  │ ├──────────────┤ │  │ ├──────────────┤ │  │   uses ClusterIP │  │ │
│  │  │ │ kora     │ │  │ │ kora     │ │  │   service        │  │ │
│  │  │ │ sidecar      │ │  │ │ sidecar      │ │  │   directly)      │  │ │
│  │  │ │ (local cache)│ │  │ │ (local cache)│ │  │                  │  │ │
│  │  │ └──────────────┘ │  │ └──────────────┘ │  │                  │  │ │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘  │ │
│  │                                                                      │ │
│  └──────────────────────────────────────────────────────────────────────┘ │
│                                                                           │
│  ┌─── External Services ─────────────────────────────────────────────┐   │
│  │  • S3/MinIO: kora-backups (volume snapshots, graph exports)     │   │
│  │  • LLM API: Anthropic or self-hosted Ollama (for consolidation)    │   │
│  │  • Embedding: self-hosted via Ollama or sentence-transformers      │   │
│  └────────────────────────────────────────────────────────────────────┘   │
│                                                                           │
└───────────────────────────────────────────────────────────────────────────┘
```

---
