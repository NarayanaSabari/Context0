# Benchmark harness: where it lives and what it has measured

Date: 2026-08-29. Unlike its neighbours in this directory, this file is meant to stay current: it is the provenance record for every LoCoMo number Kora publishes.

## The harness

Kora is measured with [supermemory's memorybench](https://github.com/supermemoryai/memorybench), which takes a provider adapter per engine.

| | |
|---|---|
| Fork | <https://github.com/NarayanaSabari/memorybench> |
| Pinned upstream | `94e2af5` |
| Local checkout | `~/Developer/narayana/memorybench` |

Until 2026-08-29 the adapter existed only in one laptop's working tree, in two copies that had diverged in both directions, and every number below came from one of them.
Neither copy was a superset, so both are preserved on the fork rather than one being chosen by accident.

| Fork branch | Adapter | Has | Lacks |
|---|---|---|---|
| `main` (`0f86511`) | committed lineage | session date folded into memory content, UUID guard on `session_id`, `extract`/`turns` ingest switch | project-id hashing, edge-aware context, `top_k` default of 30 |
| `variant/bench-worktree-adapter` (`118a70a`) | recovered from an uncommitted tree | container-tag to project-id hashing, graph-edge-aware answer context, `top_k` default of 30 | the three fixes on `main` |

**Every run in the table below used the variant, not `main`.** Reconciling the two is a decision about which adapter produces Kora's published numbers, tracked separately.

## What has been measured

Runs live in `~/Developer/narayana/memorybench/data/runs/` (gitignored: 29MB of per-question transcripts). Judge and answering model were `gemini-2.5-flash` throughout.

| when (2026-08-27 UTC) | run | n | accuracy | retrieved | MRR | recall@10 |
|---|---|---|---|---|---|---|
| 08:56 | `kora-scored` | 10 | **0.000** | 10 | 0.114 | 0.400 |
| 09:04 | `kora-turns3` | 10 | **0.100** | 10 | 0.367 | 0.700 |
| 09:15 | `kora-dated` | 10 | **0.200** | 10 | 0.363 | 0.800 |
| 09:28 | `kora-relevance` | 10 | **0.200** | 10 | 0.323 | 0.700 |
| 09:42 | `kora-smoke-bench` | 3 | **0.000** | 10 | 0.150 | 0.667 |
| 09:47 | `kora-smoke2` | 3 | **0.000** | 10 | 0.111 | 0.333 |
| 10:20 | `kora-fixed` | 10 | **0.700** | 10 | 0.820 | 1.000 |
| 10:48 | `kora-graph` | 10 | **0.800** | 10 | 0.870 | 1.000 |
| 11:27 | `kora-llm` | 10 | **0.700** | 10 | 0.850 | 1.000 |
| 12:57 | `kora-llm-40` | 40 | **0.575** | 10 | 0.865 | 0.950 |
| 13:26 | `kora-rule-40` | 40 | **0.500** | 10 | 0.916 | 1.000 |
| 18:32 | `kora-step0` | 40 | **0.600** | 30 | 0.869 | 1.000 |

And the rebaseline, 2026-08-29, on the reconciled adapter and the engine at `f4fe0d1`:

| run | n | accuracy | retrieved | MRR | recall@10 | search latency |
|---|---|---|---|---|---|---|
| `kora-rebase-0829` | 40 | **0.700** | 30 | 0.902 | 0.975 | 841ms median |

Three of these carry the weight of everything written since.

`kora-llm-40` is the 57.5% that [improvement-plan-2026-08](improvement-plan-2026-08.md) calls the baseline, and its MRR 0.865 and recall@10 0.95 are that document's other two figures.

`kora-rule-40` is the rule-based extractor at 50.0%, the comparison behind that plan's remark that a 7.5-point difference was not significant at n=40.

`kora-step0` is the experiment the plan proposes as its step 0 - raise the retrieval budget from 10 to 30, matching mem0's adapter. **It was already run.** Verified from the stored results rather than the run name: each question retrieved 30 memories against `kora-llm-40`'s 10.

## What step 0 actually bought

One question. 24 of 40 against 23 of 40, at three times the retrieval budget and 2.3 times the context tokens (4,958 against 2,180 per question).

The plan expected more: "it may close part of the gap on its own: recall@10 is already 0.95, so the ceiling is what reaches the answering model, and that is exactly what top_k controls."
The measurement does not support that.
Recall was already 1.000 in this run at k=10, so a larger budget could only help by giving the answering model more to work with, and it did not.

The per-type split is worth keeping in view, because the aggregate hides a trade:

| question type | `kora-llm-40` (k=10) | `kora-step0` (k=30) |
|---|---|---|
| single-hop | 0.533 | **0.667** |
| multi-hop | 0.650 | 0.650 |
| temporal | 0.400 | **0.200** |

Temporal halved. At these counts that is a handful of questions either way and could easily be noise, but "raise the budget" is not free of side effects, and a run that only reports 57.5 to 60.0 would not show it.

## The rebaseline: 70.0%

`kora-rebase-0829` reuses `kora-step0`'s ingested corpus rather than re-ingesting it, so the two runs differ in exactly two things: the engine, and the adapter's search path. Same 13,844 memories, same retrieval budget of 30, same judge and answering model, same context size of 4,958 tokens per question.

| | `kora-step0` | `kora-rebase-0829` |
|---|---|---|
| accuracy | 0.600 | **0.700** |
| MRR | 0.869 | **0.902** |
| single-hop / multi-hop / temporal | 0.667 / 0.650 / 0.200 | 0.600 / **0.800** / **0.600** |
| search latency (memscore) | 2,525ms | **889ms** |

The engine gained IDF-weighted full-text search (`3d562e7`), the fix that tells an unsearchable query from one that found nothing (`2d72fec`), and the query-plan fix that took keyword search off its 20x latency regression (`f4fe0d1`). `kora-step0` ran at 18:32 on 2026-08-27, before the first two landed.

Per-question, against the same 40: **5 gained, 1 lost.** McNemar exact, two-sided, over 6 discordant pairs: **p = 0.22**. So the direction is consistent and the size is not established - which is what n=40 buys, and is the same caution the improvement plan already recorded for the rule-versus-LLM comparison.

Two things follow. Full-text search earns its place on quality, not only on the indexability argument that motivated it; and temporal recovered from the 0.200 that step 0 had dropped it to, which suggests that drop was noise rather than a cost of the larger budget.

For context, mem0's open-source SDK reports 71.4 on LoCoMo. This run is 70.0 at the same retrieval budget of 30, on 40 of the 200 questions. Parity is the honest reading; a claim needs the full set.

## The 200-question run

`kora-locomo-200`, 2026-08-29, engine `f4fe0d1`, adapter `feat/reconcile-kora-adapter`. Forty questions from each of LoCoMo's five categories, drawn at random across all ten conversations - not the first 200 of a flattened list, which is one conversation and three of the five categories.

**64.5% (129/200), MRR 0.653, recall@10 0.825, 1,079ms median search.**

That is below the 70.0% measured on 40 questions, and the two are not comparable: the 40 came from one conversation and omitted the world-knowledge and adversarial categories entirely. This number is the one to quote, because it is the only one drawn from the whole benchmark.

| category | n | accuracy | failures | of which retrieval missed |
|---|---|---|---|---|
| adversarial | 40 | **0.925** | 3 | 1 |
| multi-hop | 40 | **0.700** | 12 | 2 |
| world-knowledge | 40 | **0.700** | 12 | 1 |
| temporal | 40 | **0.450** | 22 | 1 |
| single-hop | 40 | **0.450** | 22 | 2 |
| **total** | **200** | **0.645** | **71** | **7** |

**Of 71 failures, 7 are retrieval misses.** The engine put the evidence in front of the answering model in 90% of the cases it got wrong. The improvement plan's central claim - that retrieval is not the bottleneck, what happens after it is - holds on the full benchmark, not just on the conversation it was derived from.

### What the two weak categories actually are

Reading the failures rather than the category names:

**temporal** is mostly inference, not dates. `Would Caroline be considered religious?` with truth `Somewhat, but not extremely religious` answered `No.`; `Would Melanie go on another roadtrip soon?` with truth `Likely no; since this one went badly` answered `No.`. The engine reaches the right conclusion and states it flatly where the truth hedges, or commits in the wrong direction. This is calibration, and it is not what item 3 of the improvement plan proposed to fix.

**single-hop** is mostly list shape. `What activities does Melanie partake in?` with truth `pottery, camping, painting, swimming` answered `camping, exploring forests, hiking, roasting...`; `What kind of art does Caroline make?` with truth `abstract art` answered `Painting, drawing, stained glass, and murals.` The answer is drawn from more memories than the question wanted and is scored wrong for the surplus.

**adversarial at 92.5%** is worth stating plainly: refusing to answer what the corpus does not support is this engine's strongest category.

## Consequences for the plan

The retrieval budget is settled, and it was not the gap. At n=200 the same shape holds harder: 64 of 71 failures had the evidence retrieved.

What changed is which answering work matters. The plan's three items were sized on 17 failures from one conversation, where verbosity was the largest category. On the full benchmark the two levers are answer shape for list questions and hedging on inference questions, worth roughly 15 and 10 questions respectively. Relative-date resolution, item 3, addresses almost nothing here.

The one measurement that was missing - both lineages' fixes on the engine as it stands - is `kora-rebase-0829` above.

What has still never been measured: the engine on its install defaults. Every number here uses Gemini embeddings and LLM extraction. A default `helm install` gets hashed bag-of-words and the rule-based extractor, and `kora-rule-40` at 0.500 is the closest thing to evidence for what that costs.
