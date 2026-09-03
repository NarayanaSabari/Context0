# Project description and fact sheet

Reference copy for the buildathon form, a README blurb, or anything else that
needs the project described at a fixed length. Every number was counted from
the repository on 2026-09-02; where a figure comes from a measurement rather
than a count, the source is named.

---

## One line

A memory engine for AI agents, and a receivables-recovery agent built on it
that collects the same money with 77% fewer messages.

## Short (fits a 500-character field)

Kora is a memory engine for AI agents, built on PostgreSQL with Apache AGE and
pgvector. The demo is a B2B receivables chaser working 50 overdue invoices over
21 days. Both configurations recover Rs 633,300; without memory it sends 216
messages, with Kora it sends 49, because it stops re-chasing customers who
already promised to pay or disputed. Every decision is a named rule in an audit
trail, and retrieval quality comes from a deterministic offline benchmark that
makes no model calls.

## One paragraph (for the form)

Kora is a memory engine for AI agents: it stores what an agent learns, extracts
facts, and answers queries by meaning, all in PostgreSQL with Apache AGE for
the graph and pgvector for vectors, behind a gRPC and REST API. To find out
whether that memory was actually worth anything, I built a receivables chaser
on top of it: an agent that works a batch of 50 overdue invoices across 20
customers and 21 days, deciding for each one whether to remind, escalate,
offer a payment link, or hand off to a human. Against a batch that recovers
₹633,300 either way, the agent without memory sends 216 messages and the agent
with Kora sends 49, because it stops re-chasing customers who already promised
to pay, already disputed the invoice, or were already contacted that morning.
Every decision is a named rule in an audit trail; a language model only ever
rewords the message text, never chooses the action.

## Three paragraphs (for a README or a longer field)

**What it is.** Kora is a memory engine for AI agents. Memories live in
PostgreSQL, with Apache AGE providing the graph and pgvector the embeddings, so
graph edges and vectors share one store, one backup, and one trust domain.
Retrieval fuses three signals -- PostgreSQL full-text search, vector
similarity, and an entity match over the graph -- into a single ranked list. It
speaks gRPC and REST, ships as a Helm chart, and has a Python SDK.

**What it does for an agent.** The demo is a B2B receivables chaser: 50 overdue
invoices, 20 customers, 21 simulated days. It reads every cross-tick fact back
out of Kora rather than keeping local state, so its behaviour genuinely depends
on the memory engine. Both configurations recover ₹633,300 from the batch. The
escalation ladder alone needs 216 messages; with Kora it needs 49. The memory
does not recover more money, it recovers the same money without nagging people
who had already answered. Escalation is compliant and bounded: a hard stop at
45 days overdue, an immediate hand-off on any dispute, and one contact per
customer per day.

**How it is verified.** The engine was previously reported at 69% on the LoCoMo
benchmark, which turned out not to be reproducible: the answer, the grade, and
the recall figure had all been produced by a language model over an API. It was
replaced with a deterministic offline benchmark -- the real engine, 200 pinned
questions, scored against the dataset's own evidence annotations, no model
calls, byte-identical output across runs. Failure analysis showed 43 of 44
misses were evidence already retrieved and then buried by a saturated keyword
score; per-query normalisation moved hit@10 from 0.722 to 0.766. The entity
graph, the feature the project is named for, measured +0.005 MRR and was left
in at its measured weight with that written down.

---

## Fact sheet

### Identity

| | |
|---|---|
| Name | Kora |
| Repository | https://github.com/NarayanaSabari/Kora |
| Licence | Apache 2.0 |
| Track | 03 -- AI Revenue Recovery |
| First commit | 2026-04-01 |
| Commits | 250 |

### Stack

| | |
|---|---|
| Language | Go 1.26.1 |
| Storage | PostgreSQL 18.6, Apache AGE 1.8.0 (graph), pgvector 0.8.6 (vectors) |
| API | gRPC with a REST gateway, 12 RPCs |
| Deployment | Helm chart, verified on kind |
| SDK | Python |
| Demo agent | Python |
| Web console | React / TypeScript |

### Size

Counted from tracked files, excluding generated JSON reports.

| | files | lines |
|---|---|---|
| Go | 105 | 36,441 |
| Go, of which tests | | 17,907 |
| Python (SDK, MCP server, demo, scripts) | 35 | 4,938 |
| YAML (chart, CI, compose) | 22 | 5,116 |
| Shell | 9 | 2,138 |
| TypeScript / TSX | 30 | 2,640 |
| Protobuf | 5 | 956 |

### Tests

| | |
|---|---|
| Go test functions | 452 |
| Demo agent tests | 46 |
| CI checks per push | 11 |
| Golden retrieval gate | 36 cases, fails the build |

### Measured results

Retrieval, from `make eval` on the verbatim-turns corpus, 158 answerable
questions, frozen in `eval/results/`:

| metric | before | after |
|---|---|---|
| hit@10 | 0.722 | 0.766 |
| recall@10 | 0.590 | 0.646 |
| MRR@10 | 0.491 | 0.518 |
| query p50 | 24.9 ms | 16.7 ms |
| query p99 | 53.2 ms | 40.4 ms |
| allocations per query | 36,215 | 17,905 |

Benchmark corpus: 5,882 memories, 19,670 entity links, 200 pinned questions,
deterministic to a digest.

Agent, from `make demo` over 50 invoices and 21 days:

| | recovered | messages | promises seen |
|---|---|---|---|
| escalation ladder alone | ₹633,300 | 216 | 9 |
| with Kora as memory | ₹633,300 | 49 | 11 |

### Claims worth making, and the honest caveats

- The retrieval improvement is real and reproducible; the numbers come from a
  benchmark that makes no model calls and prints the same digest every run.
- The agent result is a fixture, not production traffic. It is a fair
  comparison because both arms replay the same scripted world, but it is
  synthetic and should be described that way.
- The entity graph contributes +0.005 MRR, which is nothing. Say so before
  someone asks.
- Extraction still drops the evidence for 36 of 200 questions before retrieval
  sees it. The fix is known and not done.

### Where to look

| | |
|---|---|
| Technical report | `docs/OPTIMIZATION_REPORT.md` |
| Step-by-step log, including reverts | `docs/WORKLOG.md` |
| Benchmark harness | `eval/README.md` |
| Demo agent | `examples/receivables-chaser/README.md` |
| Video script | `docs/VIDEO_SCRIPT.md` |
| Recording runbook | `docs/RECORDING.md` |
| Pitch and Q&A prep | `docs/PITCH.md` |

### Reproduce the headline numbers

```bash
make demo                      # the agent, offline, no memory
KORA_URL=... make demo         # the same agent with Kora
make eval                      # the retrieval benchmark
make test                      # unit tests, no database
```
