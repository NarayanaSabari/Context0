# Research

Point-in-time investigations, not current documentation.

Each file here records what was measured or concluded on a particular date, and
the recommendations in them were acted on separately. They are kept because the
reasoning is worth more than the conclusion: the numbers were measured against
a real deployment, and knowing why an approach was rejected saves rediscovering
it.

**Do not read these as a description of how Kora works today.** For that, see
the [README](../../README.md), [ARCHITECTURE.md](../../ARCHITECTURE.md), or the
[docs site](https://kora.sabarinarayana.com/docs/). Where a research document
and the code disagree, the code is right.

Several of these contain runnable SQL that references the `context0` schema.
That is not stale: the AGE graph and its schema deliberately kept the name
`context0` through the rename to Kora, because the graph name is the Postgres
schema holding every deployment's data. See `GraphName` in
`internal/graph/age.go`.

One file here is not a point-in-time investigation and is meant to stay
current: [benchmark-harness](benchmark-harness.md) records where the LoCoMo
harness lives, which fork branch holds which adapter, and which stored run
produced each published number. Read it before quoting a benchmark figure.

| Document | Date | Outcome |
|---|---|---|
| [k8s-production-readiness-2026](k8s-production-readiness-2026.md) | 2026-08 | Largely implemented: split probes, bounded drain, pod identity, network isolation, restricted-profile compliance |
| [observability-2026](observability-2026.md) | 2026-08 | Largely implemented: structured logging, RED metrics per method, pool saturation metrics |
| [resilience-and-durability](resilience-and-durability.md) | 2026-08 | Implemented as `scripts/backup.sh`, which also exposed a restore failure |
| [performance-audit-2026-08](performance-audit-2026-08.md) | 2026-08 | Implemented: property indexes, batched edge creation, counts from AGE base tables |
| [performance-remaining-2026-08](performance-remaining-2026-08.md) | 2026-08 | Partially implemented; the trigram and tsvector work is not done |
| [keyword-search-indexing](keyword-search-indexing.md) | 2026-08 | Findings folded into the ranking fixes; the indexing approach is open |
| [supersedes-demotion-sizing](supersedes-demotion-sizing.md) | 2026-08 | Measured the supersedes inversion rate (50% where both pair members co-occur; 60 inversions over 200 queries) and sized the demotion factor: 0.6 flips every observed inversion |
| [failure-buckets-two-judge](failure-buckets-two-judge.md) | 2026-08 | Re-audit of the 200-question failures with two independent judges; answering dominates (45 of 51 high-confidence failures had evidence retrieved, 28 at rank 1) and 19.5% of the benchmark is judge-disputed |
| [improvement-plan-2026-08](improvement-plan-2026-08.md) | 2026-08 | Items 4-6 implemented (write-time fold, entity nodes, full-text search); item 0 was run and bought one question of 40, see [benchmark-harness](benchmark-harness.md); items 1-3 are sequenced in the [plan of record](../plan-of-record.md) |
| [benchmark-harness](benchmark-harness.md) | 2026-08, current | Harness custody and run provenance. Kept current rather than point-in-time |

The entries marked open are the honest ones to check before starting new
performance work.
