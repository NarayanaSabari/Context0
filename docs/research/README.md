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

| Document | Date | Outcome |
|---|---|---|
| [k8s-production-readiness-2026](k8s-production-readiness-2026.md) | 2026-08 | Largely implemented: split probes, bounded drain, pod identity, network isolation, restricted-profile compliance |
| [observability-2026](observability-2026.md) | 2026-08 | Largely implemented: structured logging, RED metrics per method, pool saturation metrics |
| [resilience-and-durability](resilience-and-durability.md) | 2026-08 | Implemented as `scripts/backup.sh`, which also exposed a restore failure |
| [performance-audit-2026-08](performance-audit-2026-08.md) | 2026-08 | Implemented: property indexes, batched edge creation, counts from AGE base tables |
| [performance-remaining-2026-08](performance-remaining-2026-08.md) | 2026-08 | Partially implemented; the trigram and tsvector work is not done |
| [keyword-search-indexing](keyword-search-indexing.md) | 2026-08 | Findings folded into the ranking fixes; the indexing approach is open |
| [improvement-plan-2026-08](improvement-plan-2026-08.md) | 2026-08 | Items 4-6 implemented (write-time fold, entity nodes, full-text search); items 0-3 are sequenced in the [plan of record](../plan-of-record.md) |

The entries marked open are the honest ones to check before starting new
performance work.
