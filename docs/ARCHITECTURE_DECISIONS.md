# Architecture decisions

Five decisions a reviewer is entitled to challenge, and the reasoning behind
each. Where a decision was settled by a measurement rather than an opinion, the
measurement is quoted and the file it came from is named, so the claim can be
checked rather than taken on trust.

[ARCHITECTURE.md](../ARCHITECTURE.md) describes how the system works. This
describes why it is shaped that way. Two decisions were consequential enough to
have their own records in [docs/adr/](adr/); those are linked rather than
repeated.

| Decision | The short answer |
|---|---|
| One PostgreSQL, not a graph DB plus a vector DB | One backup, one trust domain, and a keyword query that joins graph data to a text index in one planner: 489ms to 9.9ms |
| Go | 95% of a query is spent waiting on PostgreSQL, so the job is to wait cheaply and predictably, not to compute fast |
| Rules decide, the LLM only rewords | Every action is a named rule in an audit trail; the policy is a pure function with 13 tests and no model in the loop |
| gRPC with a REST gateway | One protobuf definition generates both, so curl works and the API cannot drift from its documentation |
| At 10M nodes | Stored tsvector, partition vectors by project, project candidates before hydrating, and keep consolidation honest |

---

## 1. One PostgreSQL with AGE and pgvector, not a graph database plus a vector database

**The decision.** Memories, their graph edges, and their embeddings live in a
single PostgreSQL instance. Apache AGE provides the graph, pgvector the vector
index. There is no Neo4j, no Qdrant, no Pinecone.

**Why.** The obvious architecture is a graph database beside a vector database,
each best in class at its job. It loses on four counts that matter more here
than raw per-store performance.

*Recovery from a partial write is local.* Storing a memory writes a vertex,
then an embedding, then entity edges. These are deliberately **not** one
transaction: embedding calls an external provider, and holding a transaction
open across a network call to a model server would tie row locks to someone
else's latency. `CreateMemory` commits, and the rest runs on a context detached
from the caller's with its own deadline, because on the caller's context a
client that hung up mid-write left the memory stored with zero embedding rows,
permanently absent from vector search, while `Store` still returned success
(`internal/ingest/ingest.go`).

What matters here is the failure that remains. In one database, that memory is
present, keyword-findable, missing from vector search, and detectable with a
`LEFT JOIN`. Across two stores it is a row in Postgres and a missing document
in a vector service, with no join to find it and no shared log to reconcile
against. Splitting the stores does not remove the failure; it removes your
ability to see it.

*A backup is one artifact.* This was not theoretical. `scripts/backup.sh`
exists because the project had no backup path at all, and building it surfaced
a failure that only appears when you actually restore: `pg_restore` rebuilds the
pgvector HNSW index, that build wanted 131MB of shared memory in one allocation
at 94k embeddings, Kubernetes' default `/dev/shm` is 64Mi, and the index
silently did not come back. The data restored fine, vector search fell back to
scanning every row, and the API treats a failed index build as fatal at startup,
so the deployment could not come up on its own recovered data. One store made
that a single script to fix and a single check to assert. Two stores make it two
backup schedules whose restore points must agree.

*Retrieval joins across both.* Keyword search runs as PostgreSQL full-text
search over the vertex table and joins back to the graph by id. The measurement
that made this concrete: materialising two scalar CTEs took a three-term query
on 4,000 memories from 489ms to 9.9ms, and to 2.9ms once the project filter had
its index (`internal/graph/fts.go`). That query is a join between graph data and
a text index in one planner. Across a network boundary it is two round trips and
a merge in application code, and the planner can no longer help.

*One trust domain.* Credentials, network policy, and audit surface are declared
once. See [ADR 0002](adr/0002-one-deployment-is-one-trust-domain.md).

**What it costs, honestly.** AGE cannot index a substring predicate, which is
why keyword search is SQL full-text rather than Cypher `CONTAINS`. AGE also
cannot always bind parameters, so identifiers and content hashes are validated
and inlined as Cypher literals. These constraints have spread far enough into
the read path that AGE is no longer swappable:
[ADR 0001](adr/0001-apache-age-is-load-bearing.md) records that changing graph
stores is a rewrite, not an interface swap. That ADR exists because the original
plan claimed AGE was pluggable and that claim had quietly stopped being true.

**When this would be the wrong call.** If the graph workload were deep traversal
rather than one-hop provenance lookups, AGE's Cypher planner would become the
bottleneck and a dedicated graph engine would earn its operational cost. Kora's
graph reads are shallow, so it does not.

---

## 2. Go

**The decision.** The engine is Go 1.26. Not Python, which the ML tooling would
have favoured; not Rust, which would be faster still.

**Why.** The profile settles it. Before any optimisation, the process was
CPU-busy 23% of wall time, and 44% of Go CPU was `syscall.read` inside pgx
(`docs/OPTIMIZATION_REPORT.md`). Server-side statement logging put 20.8 of 21.9
ms per query inside PostgreSQL. **This is not a compute-bound workload.** It is
a service that waits on a database, and the language's job is to wait cheaply,
concurrently, and predictably.

Go is a good fit for exactly that shape:

- Goroutines make per-request concurrency cheap without an async runtime split
  into coloured functions.
- A static binary in a distroless image is a small attack surface and a fast
  cold start, which matters because the deployment target is Kubernetes.
- The runtime reads its own cgroup limits as of Go 1.25, so `GOMAXPROCS` needs
  no tuning in the chart, and setting it would actively hurt: it pins the value
  at process start and disables the cgroup check (`docs/kubernetes.md`).
- Predictable allocation behaviour makes memory a thing you can measure and
  reduce. Allocations per query went from 36,215 to 17,905 and heap in use from
  80.0MB to 60.1MB, verified to leave every ranked list byte-identical.

**Why not Python.** The retrieval path is not model inference; it is SQL and
ranking arithmetic. Python would add interpreter overhead to the 5% that is not
database wait, and would trade a single static binary for a dependency tree.
Where Python genuinely fits (the SDK, the MCP server, the demo agent) it is
used.

**Why not Rust.** It would win on the 5%, and lose more than it wins: this
service's cost is I/O, and the borrow checker buys little against a workload
where the interesting failures are query plans and index recall rather than
memory safety.

---

## 3. Rules decide, the LLM only rewords

**The decision.** In the receivables agent, every action is chosen by a pure
function over remembered facts. The language model is handed an
already-drafted message and asked to improve its wording. It never selects an
action, an amount, a recipient, or an escalation.

**Why.** This is a money workflow. Three properties follow from the constraint
and would be lost without it.

*Every decision is explainable by name.* `policy.decide()` returns an `Action`
carrying a rung and a reason, and both land in `audit.jsonl`. A run produces
rows like `skip_promise_pending / promised to pay by 2026-09-08, not due yet`
and `skip_dispute / dispute open, escalated to human`. When a merchant asks why
their customer received a fourth email, the answer is a rule name, not an
inference about a sampled token.

*It is testable.* The policy is a pure function, so its 13 tests assert
decisions directly with no model in the loop. Not one of the 46 tests
instantiates `LLMDrafter`, because nothing it does can change an outcome. A
system where the model decides cannot be tested this way; it can only be
sampled.

*It degrades safely.* `LLMDrafter` wraps the deterministic `TemplateDrafter`
and falls back to it untouched on any error. A dead model server produces
plainer English, not a missed escalation.

**The constraint is enforced, not just intended.** The model receives the
finished template and a system instruction to keep every fact, date, amount and
invoice id exactly as given. It cannot introduce a decision because it is never
shown the decision space.

**The honest limit.** A rules engine only handles the cases someone anticipated.
The escalation ladder, the 45-day human hand-off, the same-day contact
suppression and the dispute stop were all written by hand, and a situation
outside them gets the nearest rule rather than judgement. The defensible place
for a model here is triage of the exception list a human already reviews, not
the action itself.

---

## 4. gRPC with a REST gateway

**The decision.** The service is defined in protobuf and served over gRPC on
50051, with grpc-gateway generating a REST interface on 8080 from the same
definitions. Both are always on.

**Why not gRPC alone.** A judge, a curl user, or a browser-based console should
not need `grpcurl` and a descriptor set to see whether the thing works. The
README's examples are curl commands for this reason. The web UI talks REST
because browsers do not speak gRPC without a proxy, and adding grpc-web would be
a third protocol to keep consistent.

**Why not REST alone.** The proto files are the single source of truth for the
API: 12 RPCs across three services, generating Go server stubs, the gateway,
and the OpenAPI description. Hand-written REST handlers drift from their
documentation because nothing forces agreement. Here a field added to a message
appears in both transports or the build fails.

**Why both, rather than choosing.** They cost one definition and one generation
step. Streaming and strict typing are available for agent SDKs that want them;
curl works for everyone else.

**Worth stating plainly:** the Python SDK currently speaks REST, despite taking
a `localhost:50051` endpoint and rewriting it to `:8080` internally. That is a
wart. The gateway makes it harmless, but it means the SDK does not yet exercise
the gRPC path, and the argument above is about the design rather than about
every client living up to it today.

---

## 5. What I would do differently at 10M nodes

The engine is measured at roughly 5,900 memories with 19,670 entity links, and
the demo instance runs a few thousand. Three orders of magnitude up, four things
break in a known order. The order matters: they are listed by when they bite,
not by how interesting they are.

**First: full-text search, at roughly 100k memories.** The FTS statement is
already 9 of the remaining 17 ms per query, because `ts_rank_cd` recomputes
`to_tsvector` for every matching row for every term. The fix is standard and
uncontroversial: a stored, indexed `tsvector` column maintained by a trigger.
It is not done because it is a schema migration, and the stop rule for that
optimisation pass reserved schema changes for the owner's decision, not because
anything about it is hard.

**Second: scoped vector search, at roughly 500 projects.** This one has already
been reproduced deterministically at small scale. With 40,000 embeddings across
500 projects, the planner drove a scoped query from the HNSW index and applied
the project filter afterwards, so the scan budget was spent on other projects'
vectors: a two-row project returned one row. Raising `hnsw.ef_search` to 1000
and `max_scan_tuples` tenfold did not fix it. A sequential scan returned both
rows, which is what proved the loss was the index rather than the data. The
current answer is an index on `project_id` so the planner filters first, plus an
eager `ANALYZE` because with no statistics it estimates one row and reverts to
the failing plan. At 10M nodes that stops being enough and the real answer is
partitioning by project, so each project's vectors are physically separate and
"filter first" is structural rather than a plan the optimiser might abandon.

**Third: hydration, on every query regardless of size.** 500 full memory
vertices are JSON-decoded per query, which is 24% of remaining allocations,
while ranking reads four fields of them. The fix is to fetch a narrow projection
for candidates and full rows only for the top 30. This is pure waste at any
scale and simply becomes unaffordable at 10M.

**Fourth: consolidation, which is a data-shape problem rather than a
performance one.** A corpus was once found holding 6,010 memories for 573
distinct facts. Write-time consolidation folds restatements, and
`kora_memories_consolidated_total` exists to make the ratio observable, because
the only other way to notice it drifting is counting rows by hand. At 10M nodes
an un-consolidating store is mostly duplicates, and every retrieval metric
degrades for a reason no index can fix.

**What I would not change.** The single-database decision survives this scale;
PostgreSQL partitioning and read replicas are well-understood, and the
consistency argument in section 1 gets stronger with volume, not weaker. What
would change is that the three retrievers, currently sequential, would run
concurrently, and the entity signal would need the IDF weighting described in
[issue #86](https://github.com/NarayanaSabari/Kora/issues/86) before it is worth
its cost.

**One thing I would revisit on principle.** The entity graph measures +0.005
MRR on answerable questions and costs 0.106 MRR on adversarial ones. At 10M
nodes I would not carry a signal with that profile on the default read path
without the IDF fix; I would put it behind the ablation flag that already
exists and turn it on only where it earns its keep.
