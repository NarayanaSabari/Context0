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

## Consequences for the plan

The retrieval budget is settled, and it was not the gap. What remains between Kora and open-source mem0's 71.4 has to come from the answering and extraction work, which is where the plan's own failure analysis already pointed: 15 of 17 failures had the right memory retrieved.

Nothing here has yet been measured against an adapter that carries both lineages' fixes, or against the engine as it stands today.
