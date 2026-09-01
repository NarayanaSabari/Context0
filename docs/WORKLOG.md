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

## Track A, step 1: where the 44 answerable misses go

Method: `cmd/eval -trace` records every retriever's candidate pool and the fused components of each result.
A miss is bucketed by where its best evidence doc stood. `turns` corpus, 158 scorable answerable questions:

| bucket | single | multi | temporal | world | total |
|---|---|---|---|---|---|
| A: evidence in top 10 | 28 | 32 | 23 | 31 | 114 |
| B: evidence returned at rank 11-30 | 6 | 2 | 7 | 3 | 18 |
| C: evidence in a retriever's pool, fused below the cut | 6 | 6 | 7 | 6 | 25 |
| D: evidence in no retriever's pool | 0 | 0 | 1 | 0 | 1 |

Retrieval misses proper (D) are one question. **43 of 44 failures are fusion and ranking**, not recall: the evidence was a candidate.

What outranked it. For every B and C question, the components of the doc at rank 10 minus the components of the best evidence doc:

| bucket | keyword | cosine | entity |
|---|---|---|---|
| B (n=18) | **+0.239** | -0.033 | -0.056 |
| C (n=25) | **+0.488** | +0.106 | +0.613 |

The evidence typically had the *better* cosine and lost on the keyword signal.
In B its keyword rank was median 48 against vector rank median 16; in C, keyword rank median 186 against vector rank median 55.

Why the keyword signal wins: `NormalizeBM25` is a logistic sigmoid whose midpoint is 0.4 of the query's term count, so a memory matching two or three of a question's terms saturates at 0.95-1.00 while a memory matching one sits at 0.10-0.19.
The signal is near-binary.
Cosine similarity from nomic-embed-text, by contrast, arrives raw and lives in a band of roughly 0.55-0.85, so its 0.35 weight moves the fused score by a tenth of what the keyword signal's 0.5 does.
The fusion is a keyword ranker with a small semantic tie-break, and the keyword ranker is saturated by the speaker's name (present in every turn via the "Melanie said that" prefix) plus one common word.

Example (`conv-26-q19`, "What do Melanie's kids like?"): the evidence "They were stoked for the dinosaur exhibit! They love learning about animals" is vector rank 3, keyword rank 256 (it matches only "melanie"), fused out of the top 30.
Rank 1 is "Giving a home to needy kids is such a loving way to build a family", which matches "melanie", "kids" and "like" and scores keyword 1.00.

The same shape on the `extracted` corpus (134 scorable): 106 A, 16 B, 11 C, 1 D; rank-10 minus evidence keyword +0.132 (B) and +0.491 (C).

Fix order, by bucket size: fusion first (B + C = 43), then the entity signal (the C-bucket's +0.613 entity gap says the entity boost is lifting non-evidence, as the adversarial baseline already showed), then recall (D = 1, nothing to gain).

## Track A, change 1: normalise the signals before fusing them

**Hypothesis.** The misses are a fusion problem: the keyword signal is saturated by the sigmoid and the cosine signal is compressed, so the weighted sum is a keyword ranker.
Rescaling each signal per query (keyword by the pool's best raw score, cosine by the pool's min-max range) should let the semantic evidence compete.
Reciprocal rank fusion is the other standard answer and is scale-free, so it is measured beside it.

**Change.** `retrieval.Fusion` with three modes, selected by `SetFusion`: `linear` (the original), `minmax`, `rrf` (k = 60, entity overlap added as a share of a rank-1 contribution).
`cmd/eval -fusion/-weights/-rrf-k`, and `-reuse-db` so a sweep loads the corpus once.
Weights are the engine's 0.5 / 0.35 / 0.15 unless stated.

**Measured**, answerable questions, metrics at k = 10:

| corpus | fusion | hit@10 | rec@10 | full@10 | MRR | nDCG | adversarial MRR |
|---|---|---|---|---|---|---|---|
| turns (158) | linear (baseline) | 0.722 | 0.590 | 0.475 | 0.491 | 0.487 | 0.338 |
| turns | **minmax** | 0.753 | 0.624 | 0.506 | 0.499 | 0.501 | 0.551 |
| turns | rrf 1/1/0.3 | 0.747 | 0.611 | 0.494 | 0.453 | 0.468 | 0.264 |
| turns | rrf 1/1/0 | 0.734 | 0.603 | 0.494 | 0.441 | 0.458 | 0.387 |
| turns | linear, weights 0.35/0.5/0.15 | 0.722 | 0.587 | 0.475 | 0.498 | 0.490 | 0.274 |
| extracted (134) | linear (baseline) | 0.791 | 0.683 | 0.582 | 0.598 | 0.588 | 0.366 |
| extracted | **minmax** | 0.821 | 0.712 | 0.612 | 0.595 | 0.591 | 0.629 |
| extracted | rrf 1/1/0.3 | 0.791 | 0.687 | 0.582 | 0.606 | 0.589 | 0.358 |

Paired, linear to minmax: turns hit@10 gained 7 questions and lost 2 (McNemar p = 0.18), MRR +0.008 (95% CI -0.03 to +0.05); extracted gained 5 and lost 1.
The direction is the same on both corpora and on every metric, and adversarial MRR rises 0.2 on both, which reweighting alone (row 5) does not do.

RRF is worse than min-max on MRR on both corpora (0.453 vs 0.499 on turns): a rank-based fusion throws away how far apart two candidates were, and on this corpus the keyword ranking's top positions are mostly noise, so RRF rewards them anyway.

**Verdict: keep min-max normalisation.** Weights are chosen in the next step, one axis at a time.

## Track A, change 2: the fusion weights

**Hypothesis.** With both signals on one scale, the 0.5 / 0.35 split that favoured keywords is no longer justified by the saturation it was compensating for.

**Change.** None to the code beyond the constants; a sweep through `cmd/eval -weights` on the loaded corpora. Keyword weight from 0.30 to 0.60 with semantic = 0.85 - keyword and entity fixed at 0.15; then entity from 0 to 0.30 at 0.40 / 0.45.

**Measured**, answerable MRR@10 (hit@10 in brackets):

| keyword / semantic | turns (158) | extracted (134) |
|---|---|---|
| 0.30 / 0.55 | 0.499 (0.753) | **0.620** (0.828) |
| 0.35 / 0.50 | 0.503 (0.759) | 0.617 (0.813) |
| **0.40 / 0.45** | 0.509 (0.766) | 0.614 (0.806) |
| 0.45 / 0.40 | **0.510** (0.778) | 0.606 (0.813) |
| 0.50 / 0.35 (change 1) | 0.499 (0.753) | 0.595 (0.821) |
| 0.55 / 0.30 | 0.502 (0.741) | 0.589 (0.799) |
| 0.60 / 0.25 | 0.499 (0.734) | 0.588 (0.799) |

The two corpora peak in different places (verbatim turns want more keyword, extracted facts want more semantic), and every difference between neighbouring rows is inside the paired 95% CI of about +/- 0.02.
0.40 / 0.45 is the point neither corpus objects to.

Entity weight at 0.40 / 0.45:

| entity | turns MRR | turns adversarial MRR | extracted MRR | extracted adversarial MRR |
|---|---|---|---|---|
| 0 | 0.504 | 0.525 | 0.604 | 0.643 |
| 0.05 | 0.507 | 0.506 | 0.612 | 0.638 |
| 0.15 | 0.509 | 0.439 | 0.614 | 0.608 |
| 0.30 | 0.514 | 0.353 | 0.621 | 0.451 |

The entity signal buys under 0.01 of answerable MRR per 0.15 of weight and costs adversarial MRR steeply.
Nothing here is significant, so the weight stays at 0.15 and the entity signal gets its own experiment.

**Verdict: 0.45 / 0.40 / 0.15 becomes the default**, with min-max fusion.
0.40 / 0.45 and 0.45 / 0.40 are indistinguishable on the harness (turns MRR 0.509 vs 0.510, extracted 0.614 vs 0.606, both inside the CI), and the second has a property the harness cannot see: with the keyword weight at or above the semantic one, the best lexical match for a query can never be outranked by a candidate with no lexical evidence.
That is the guarantee behind `TestExactKeywordMatchOutranksVectorOnlyResult`, a soak-run bug where a write became unreadable by a rare token in its own content, and 0.40 / 0.45 broke that test.
Two other tests pinned the old fusion's arithmetic and were rewritten to state the new contract (`TestMergeResults_CarriesRelevanceForward`, `TestFuseRelevance_AStrongKeywordMatchStillBeatsSemanticSimilarityAlone`); the golden suite passes unchanged (overall recall 0.917, MRR 0.856).

Against the original engine, turns corpus: hit@10 0.722 to 0.778, rec@10 0.590 to 0.653, full@10 0.475 to 0.544, MRR 0.491 to 0.510, nDCG 0.487 to 0.514; extracted: hit@10 0.791 to 0.813, MRR 0.598 to 0.606.
