# Optimization worklog

Branch `perf/memory-optimization`, started 2026-09-02.
Every entry: hypothesis, change, measured result, keep or revert.
Newest at the bottom.

## Phase 0: what "69%" is

**Metric.** `kora-abl-on`, run 2026-08-30 in the memorybench fork (`~/Developer/narayana/memorybench`, branch `feat/reconcile-kora-adapter`).
200 pinned LoCoMo questions (`data/pinned/locomo-200.json`, 40 per category).
Accuracy is an LLM judge's verdict (`z-ai/glm-5.3-flash` via OpenRouter) on an LLM answer (same model) generated from Kora's top-30 memories.
138 of 200 judged correct.

**Corpus.** The ten LoCoMo conversations, one Kora project each, ingested through `POST /v1/memories/extract` with `KORA_EXTRACTION_PROVIDER=llm` (GLM-5.3-flash over OpenRouter) and `KORA_EMBEDDING_PROVIDER=ollama` (`nomic-embed-text`, 768d, local container `kora-ollama`).
Counted from the live database `kora-abl-pg`: 2,095 memories, 438 entities, 18,639 `mentions` edges (all projects), engine image `kora-api:abl` at `d5c70ee`.
`docs/research/ablation-baseline.md` says 2,537 memories; the database says 2,095 for the ten `kora-abl-on` projects.
The larger figure is not reproducible from what is stored.

**Retrieval numbers are LLM-judged too.** memorybench's `hitAtK` / `recallAtK` / `mrr` / `ndcg` come from `src/orchestrator/phases/retrieval-eval.ts`, which asks the judge model to label each of the top-10 results relevant or not.
`recallAtK` is defined as `relevantRetrieved > 0 ? 1 : 0`, so "recall@10 0.855" is hit@10 under an LLM's relevance opinion, not a match against LoCoMo's evidence annotations.
None of the published retrieval figures are deterministic either.

**Reproducibility offline: none.** Answering, judging, and the retrieval metrics all require an external model.
The corpus itself required GLM extraction.
Finding #1 is therefore: the 69% cannot be reproduced offline, and a harness is built from scratch below.

**Two more properties of the run that matter for any reproduction:**
- `Query` increments `access_count` on every returned memory, and ranking weights frequency at 0.10.
  Each question's ranking therefore depended on which questions ran before it.
  The stored database now carries counts of up to 28 per memory after four runs.
- Ranking's recency signal is measured against wall-clock `time.Now()`, so the same corpus ranks differently on different days when scores are close.

## Phase 0: the offline harness

**Built from scratch** (`cmd/eval`, `internal/evalset`, `eval/`, `make eval`), because nothing published was reproducible offline.
Design in [eval/README.md](../eval/README.md). The short version:

- Ground truth is LoCoMo's evidence annotations (the dialogue turns each question was written from), which are fixed data, not a judge.
- Two corpora. `turns`: every utterance stored verbatim as the adapter renders it, exact labels, the primary metric.
  `extracted`: a snapshot of the 2,095 GLM-extracted memories from the 0.690 run with their original nomic vectors, heuristic labels (66% agreement with the LLM judge; 36 of 200 questions have no evidence in it at all), used to reproduce that run's ranked lists and as a secondary metric.
- Vectors come from a committed fixture built once through the engine's own Ollama client (`eval/fixtures/locomo/embeddings.bin`, 8,035 vectors).
  A missing vector fails the run; nothing falls back to a model.
- The clock is fixed (`retrieval.Engine.SetClock`, `ranking.RankResultsAt`), access counts are zero, the database is recreated empty per run.
- LoCoMo is CC BY-NC 4.0, so the dataset and the snapshot stay in the gitignored `eval/data`; ids, labels and vectors are committed.

**Reproducibility, measured.** Two consecutive `turns` runs on fresh databases: identical metrics, identical digest over all 200 ranked lists (`277c4c2d...`).

**Fidelity to the 0.690 run.** The `extracted` corpus through today's engine against the stored `kora-abl-on` top-30 lists: identical top-1 for 161 of 200 questions, identical top-10 for 52, mean top-10 Jaccard 0.870.
The remainder is the original run's own path dependence: access counts rose during the run and ranking weights frequency at 0.10.
Under identical inputs the engine is deterministic, so this is as close as the published run can be reproduced.

### Baseline (engine at `a3811d7`, top-k 30, metrics at k = 10)

`turns` corpus, frozen nomic-embed-text vectors, all graph signals on:

| category | n | hit@1 | hit@5 | hit@10 | hit@30 | rec@10 | full@10 | MRR@10 | nDCG@10 |
|---|---|---|---|---|---|---|---|---|---|
| single-hop | 40 | 0.200 | 0.575 | 0.700 | 0.850 | 0.415 | 0.175 | 0.357 | 0.316 |
| multi-hop | 40 | 0.525 | 0.700 | 0.800 | 0.850 | 0.742 | 0.675 | 0.615 | 0.636 |
| temporal | 38 | 0.263 | 0.474 | 0.605 | 0.789 | 0.454 | 0.342 | 0.360 | 0.341 |
| world-knowledge | 40 | 0.525 | 0.775 | 0.775 | 0.850 | 0.742 | 0.700 | 0.628 | 0.648 |
| adversarial | 40 | 0.200 | 0.475 | 0.725 | 0.900 | 0.713 | 0.700 | 0.338 | 0.424 |
| **answerable** | 158 | 0.380 | 0.633 | **0.722** | 0.835 | 0.590 | 0.475 | **0.491** | 0.487 |
| all | 198 | 0.343 | 0.601 | 0.722 | 0.848 | 0.614 | 0.520 | 0.460 | 0.474 |

Two temporal questions carry no evidence annotation and are excluded.

Performance, same run (`Retrieve` only, warm, 400 timed queries, Postgres in Docker on this laptop):

| | value |
|---|---|
| latency p50 / p95 / p99 / max | 24.9 / 38.5 / 53.2 / 72.4 ms |
| allocations per query | 36,215 mallocs, 3.37 MB |
| corpus | 5,882 memories, 19,670 entity links, 41 s to load |
| FTS index / HNSW index / mentions edges | 2.9 MB / 23.5 MB / 3.4 MB |

Variants, `answerable` row:

| variant | hit@10 | MRR@10 | nDCG@10 | adversarial MRR | p50 ms | mallocs/query |
|---|---|---|---|---|---|---|
| graph signals on (above) | 0.722 | 0.491 | 0.487 | 0.338 | 24.9 | 36,215 |
| graph signals off | 0.722 | 0.476 | 0.478 | **0.547** | **15.2** | 18,475 |
| bag-of-words embedder (install default) | 0.639 | 0.412 | 0.420 | 0.307 | 22.6 | 35,955 |
| `extracted` corpus (134 scorable) | 0.791 | 0.598 | 0.588 | 0.366 | 22.9 | 18,529 |

Three things the baseline already says:

1. The entity signal costs 10 ms and half the allocations per query, buys +0.015 MRR on answerable questions, and costs 0.21 MRR on adversarial ones.
   The published ablation found the same shape on accuracy; this harness can see it on ranking.
2. The install default (hashed bag-of-words) is 8 points of hit@10 behind a real embedder.
3. Extraction helps ranking where it keeps the fact (hit@10 0.79 vs 0.72) and loses the fact outright for 36 of 200 questions.
