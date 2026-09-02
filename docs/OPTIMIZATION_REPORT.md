# Optimization report

Branch `perf/memory-optimization`, 2026-09-02.
Engine before: `a3811d7`. Engine after: the head of the branch.
Every number below was produced by `make eval`, which is deterministic and makes no network or model call; the step-by-step record with hypotheses and reverts is [WORKLOG.md](WORKLOG.md).

## 1. What the 69% was, and why it could not be reproduced

The published figure is run `kora-abl-on` in the memorybench fork: 200 pinned LoCoMo questions, 138 judged correct.
Correctness is an LLM judge's verdict (`z-ai/glm-5.3-flash`, via OpenRouter) on an LLM answer generated from Kora's top 30 memories.
The published "recall@10 0.855" comes from `retrieval-eval.ts`, which asks the same model to label each of the top-10 results relevant or not, and defines recall as "at least one result judged relevant".
The corpus itself was produced by LLM extraction over the same API.

Nothing in that chain can run without an external model, so under the offline constraint the figure is not reproducible, and that is finding #1.
Two further properties make it hard to reproduce even with the API: `Query` increments access counts on every result and ranking weights frequency, so each question's ranking depended on which questions ran before it; and the recency signal is measured against wall-clock time.

The harness was therefore built from scratch against the only fixed ground truth LoCoMo has: the dialogue turns each question was written from.

## 2. The harness

`make eval` (`cmd/eval`, `internal/evalset`, [eval/README.md](../eval/README.md)):

- **Ground truth**: LoCoMo's evidence annotations, per question.
- **Corpora**. `turns`: all 5,882 utterances stored verbatim as the benchmark adapter renders them, exact labels, the primary metric. `extracted`: the 2,095 GLM-extracted memories of the 0.690 run with their original vectors, timestamps and entity links, heuristic labels (66% agreement with the LLM judge), used to reproduce that run's ranked lists and as a secondary metric.
- **Vectors**: a committed fixture of 8,035 nomic-embed-text vectors built once through the engine's own Ollama client and verified bit-identical to the vectors the published run stored. A missing vector fails the run.
- **Determinism**: fixed ranking clock (`retrieval.Engine.SetClock`), zero access counts, deterministic ids, a database recreated empty per run. Two consecutive runs produce identical metrics and an identical digest of all 200 ranked lists.
- **Fidelity**: the `extracted` corpus through today's engine reproduces the published run's top result for 161 of 200 questions (mean top-10 Jaccard 0.87); the remainder is that run's access-count path dependence.
- **Metrics**: hit@k, recall@k, full-evidence@k, MRR@10, nDCG@10 per category; p50/p95/p99 latency of `Retrieve`; allocations per query; every table and index size; per-retriever traces for failure attribution.
- **Licensing**: LoCoMo is CC BY-NC 4.0, so the dataset and anything containing its text stay in the gitignored `eval/data`; ids, labels, vectors and result reports are committed.

A correction that affects every earlier document in `docs/research/`: LoCoMo's integer category codes were mislabelled by the harness (code 1 is multi-hop, 2 temporal, 3 open-domain, 4 single-hop). The eval uses the verified mapping; earlier per-category numbers must be read through the table in WORKLOG.md.

## 3. Baseline vs final

`turns` corpus, frozen nomic vectors, 158 answerable questions (2 carry no evidence), metrics at k = 10.

| metric | baseline `a3811d7` | final | paired test |
|---|---|---|---|
| hit@10 | 0.722 | **0.766** | 11 gained, 4 lost, McNemar p = 0.12 |
| recall@10 | 0.590 | **0.646** | +0.056, 95% CI [+0.016, +0.098] |
| all evidence in top 10 | 0.475 | **0.538** | |
| MRR@10 | 0.491 | **0.518** | +0.026, 95% CI [-0.004, +0.058] |
| nDCG@10 | 0.487 | **0.519** | |
| hit@30 (the adapter's budget) | 0.835 | **0.873** | |
| latency p50 / p95 / p99 | 24.9 / 38.5 / 53.2 ms | **16.7 / 29.0 / 40.4 ms** | |
| allocations per query | 36,215 mallocs, 3.37 MB | **17,905 mallocs, 2.37 MB** | |
| Go CPU per query | ~5 ms | not re-measured | |
| index sizes | unchanged: FTS 2.9 MB, HNSW 23.5 MB, mentions 3.4 MB | | |

`extracted` corpus, 134 scorable answerable questions:

| metric | baseline | final |
|---|---|---|
| hit@10 | 0.791 | **0.806** (4 gained, 2 lost) |
| recall@10 | 0.683 | 0.695 |
| MRR@10 | 0.598 | **0.617** |
| latency p50 | 22.9 ms | **7.0 ms** |
| allocations per query | 18,529 | **13,565** |

Per category, `turns`, final (hit@10 / MRR@10, n = 40 each except open-domain 38): single-hop 0.850 / 0.607, multi-hop 0.725 / 0.393, temporal 0.825 / 0.645, open-domain 0.658 / 0.422, adversarial 0.750 / 0.453.

The install-default embedder (hashed bag-of-words) is the one place the new fusion is not a clear win: hit@10 0.639 to 0.570 with MRR flat (0.412 to 0.415).
Min-max rescaling amplifies a signal that, for that embedder, is token overlap rather than meaning.
The golden regression gate, which runs on that embedder, passes with a higher MRR, so the trade is visible only on LoCoMo; it is listed under what is left on the table.

## 4. Failure analysis, before and after

Method: `cmd/eval -trace` records each retriever's candidate pool and the fused components of every result, and a miss is bucketed by where its best evidence doc stood.
Categories use the corrected labels.

`turns`, answerable questions, before (engine `a3811d7`, min-max off) and after:

| bucket | single | multi | temporal | open | total before | total after |
|---|---|---|---|---|---|---|
| A: evidence in top 10 | 31 → 34 | 28 → 29 | 32 → 33 | 23 → 25 | 114 | **121** |
| B: evidence returned at rank 11-30 | 3 → 2 | 6 → 6 | 2 → 2 | 7 → 7 | 18 | 17 |
| C: evidence in a retriever's pool, fused below the cut | 6 → 4 | 6 → 5 | 6 → 5 | 7 → 5 | 25 | 19 |
| D: evidence in no retriever's pool | 0 | 0 | 0 | 1 | 1 | 1 |

The diagnosis that drove Track A: only one answerable miss was a retrieval miss.
Forty-three were evidence a retriever had found and fusion had buried, and for those the doc at rank 10 beat the evidence on the keyword component (+0.24 in B, +0.49 in C) while the evidence had the better cosine.
The keyword score was a logistic sigmoid that saturates at two matched terms, and cosine similarity from nomic-embed-text arrives raw in a band 0.3 wide; the weighted sum was a keyword ranker with a semantic tie-break.

What remains: B and C are now 36 questions whose evidence has vector rank median 16 and keyword rank median 120, beaten by chit-chat matching two or three common words, mostly in multi-hop and open-domain.
Multi-hop needs several turns in the window (full@10 0.200) and is where a graph should help; see section 6.

`extracted` corpus: before 106 A / 16 B / 11 C / 1 D, after 108 / 14 / 11 / 1, with 26 questions unscorable because extraction dropped every evidence turn.
That loss, 36 of 200 questions with no evidence in the store at all, is the largest single defect on the extraction side and is not reachable from the read path.

## 5. Ablation table

One change at a time, each measured on both corpora and checked against the golden gate; kept changes are cumulative in order. "Δ" is against the row above.

| # | change | turns hit@10 / MRR | extracted hit@10 / MRR | p50 ms | mallocs/query | verdict |
|---|---|---|---|---|---|---|
| 0 | baseline `a3811d7` | 0.722 / 0.491 | 0.791 / 0.598 | 24.9 | 36,215 | |
| A1 | per-query min-max normalisation of keyword and cosine before the weighted sum | 0.753 / 0.499 | 0.821 / 0.595 | 24.0 | 36,215 | keep |
| A1' | reciprocal rank fusion instead (k = 60) | 0.747 / 0.453 | 0.791 / 0.606 | 24.3 | | reject: MRR -0.04 on turns |
| A1'' | reweight only, no normalisation (0.35 / 0.5 / 0.15) | 0.722 / 0.498 | 0.799 / 0.602 | 23.6 | | reject: hit@10 unmoved |
| A2 | weights 0.5 / 0.35 / 0.15 → 0.45 / 0.40 / 0.15 | 0.778 / 0.510 | 0.813 / 0.606 | 22.8 | 36,214 | keep |
| A2' | entity weight 0 / 0.05 / 0.30 | 0.759-0.766 / 0.504-0.514 | 0.806 / 0.604-0.621 | | | inside noise; keep 0.15 |
| B1 | sort candidates by UUID bytes, not strings | same (digest identical) | same | 22.9 | 29,480 | keep |
| B2 | scoped vector search without the transaction | same | same | 22.5 | 29,474 | keep |
| B3 | simple protocol for literal-list Cypher | same | same | 21.0 | 29,442 | keep |
| B4 | count the query's entities per candidate in SQL | same overlap by construction | same | 17.7 | 17,905 | keep |
| A3 | keyword scale: coverage exponent 1 (pure full-match) | 0.766 / 0.513 | 0.806 / 0.615 | | | reject: golden paraphrase MRR 0.679 → 0.538 |
| A3' | coverage exponent 0.5 | 0.766 / 0.513 | 0.806 / 0.618 | | | reject: golden 0.622, at the floor |
| A3 | **coverage exponent 0.25** | 0.766 / 0.518 | 0.806 / 0.617 | 16.7 | 17,905 | keep: golden unchanged, service contract holds |
| C1 | graph signals off, on the final engine | 0.766 / 0.513 | | 18.0 | 12,681 | informational: entity signal buys +0.005 MRR, costs 5,200 allocs and adversarial MRR 0.559 → 0.453 |

Latency rows were taken on a laptop with Docker; the p50 is stable to about ±1 ms between runs, the p99 is not.

## 6. What came from which paper

Read during this work, mechanism extracted, and measured on the frozen set:

| source | mechanism | outcome here |
|---|---|---|
| Bruch, Gai, Ingber, "An Analysis of Fusion Functions for Hybrid Retrieval" (TOIS 2023, arXiv 2210.11934) | convex combination over per-query min-max normalised scores (TM2C2) beats RRF on 9 of 9 BEIR sets | **adopted**: A1. Reproduced: min-max beats RRF on both corpora (turns MRR 0.499 vs 0.453) |
| Cormack, Clarke, Buettcher, "Reciprocal Rank Fusion" (SIGIR 2009) | RRF with k = 60 | measured (A1'), rejected: rank fusion discards the score gap that separates a strong semantic match from a weak one |
| Lysenstøen, "Training-Free Lexical-Dense Fusion for Conversational-Memory Retrieval" (arXiv 2606.04194) | z-scored convex fusion on LoCoMo, dense weight 0.6, cross-encoder rerank *hurts* session-level Hit@1 by 6.9 pp | consistent with A2's sweep (both corpora peak between 0.40 and 0.55 keyword) and with leaving reranking out |
| Hu et al., "Does Memory Need Graphs?" (arXiv 2601.01280); Mem0 (arXiv 2504.19413); An, "Verbatim Chunks Beat Extracted Artifacts" (arXiv 2601.00821) | entity-similarity graphs and 1-hop expansion measure 0 to negative at query time; gains come from what is written | consistent with C1 and with the repo's own ablation: the entity signal is worth +0.005 MRR here |
| Wu et al., LongMemEval (ICLR 2025, arXiv 2410.10813) Table 3 | index verbatim rounds with extracted facts merged into the same key (+0.062 R@5); rank-fusing separate fact and value indexes underperforms | not implemented (needs new vectors, so a new fixture); the strongest candidate for the extraction-loss defect in section 4 |
| HippoRAG 2 (ICML 2025, arXiv 2502.14802) | personalised PageRank over a phrase-passage graph, LLM-free variant costs 0.7 R@5 | not implemented: gain is confined to multi-hop and needs OpenIE at ingest |
| TempRALM (arXiv 2401.13222), TSM (arXiv 2601.07468) | rule-parsed date range as an additive prior; validity-first ordering for superseded facts | not implemented: the turns corpus has no contradictions and the evidence labels cannot score "current state" questions |
| ConvMemory (arXiv 2605.28062) | a small learned reranker over the engine's own features beats a cross-encoder at 12x lower latency | not implemented: needs training labels; the harness now produces them |

Rejected without implementing, on the evidence in the survey: RM3 / vector pseudo-relevance feedback (hurts top-k precision on single-gold questions), cross-encoder reranking (no deterministic local model is available to this Go engine, and the one LoCoMo measurement is negative at session level), LLM routers for supersession (82% → 0% on paraphrased updates in ForgetEval).

## 7. Tried and reverted, and what is left on the table

Reverted or rejected: RRF (A1'), reweighting without normalisation (A1''), pure coverage grading (A3, A3'), and the entity-weight variants (A2').

Left on the table, in order of expected value:

1. **Extraction loss**: 36 of 200 questions have no evidence memory in the extracted store. LongMemEval's key merging (verbatim round plus facts as one indexed key) is the measured fix and needs a fixture rebuild.
2. **Bag-of-words fusion profile**: the install default loses 11 questions of hit@10 under min-max. A per-embedder fusion default (linear for hashed vectors) is a small change; it needs the eval run under `-embedder bow` to gate it.
3. **Lazy hydration**: 500 full memory vertices are JSON-decoded per query (24% of remaining allocations, two of the 2.4 ms hydration statements); ranking needs four fields of them. Fetching a projection for candidates and full rows for the top 30 is the next latency step.
4. **The FTS statement** is 9 ms of the remaining 17: `ts_rank_cd` recomputes `to_tsvector` per matching row per term. A stored tsvector column is the standard fix and is a schema change, which the stop rule reserves for the user.
5. **Parallel retrievers**: keyword, vector and entity queries are independent and sequential; running them on three pool connections would cut wall latency further at the cost of pool headroom under load.
6. **Recency and frequency priors**: unmeasurable here because the corpus timestamps are uniform and access counts are zero. A dated variant of the turns corpus would measure the recency weight.

## 8. Go performance and structure

Profile before any change (1,000 timed queries): the process was CPU-busy 23% of the wall time and 44% of Go CPU was `syscall.read` inside pgx; the read path is a sequence of PostgreSQL round trips.
Server-side statement logging put 20.8 of 21.9 ms per query in PostgreSQL: the FTS statement 8.9 ms, loading candidate entities 4.7 ms, two hydration statements 1.2 ms each, the scoped vector scan 1.9 ms, entity matches 1.1 ms.

Changes, each measured with the digest as the no-behaviour-change check (B1-B4 in section 5): byte-order UUID sorting, one statement instead of a five-round-trip transaction for scoped vector search, the simple protocol for one-off literal-list Cypher (which had been churning pgx's statement cache), and a counted entity-match query in place of loading every entity of every candidate.
Net: p50 24.9 → 16.7 ms, allocations halved.

Structure: the read path already had a narrow `retrieval.Repo` seam and the write path its own; the only interface change was `SearchByText` reporting the query's IDF mass, which is retrieval information the repository owns.
No `sync.Pool`, no SIMD: nothing in the profile justified them, the vector arithmetic lives in pgvector.

## 9. Kubernetes

The chart's settings and their reasons are in [kubernetes.md](kubernetes.md); the decisions, from the profile and the current documentation:

- **GOMAXPROCS**: not set. Go 1.25+ derives it from the cgroup CPU limit and re-checks it once a second; this module's `go` directive is 1.26, so the behaviour is active. `automaxprocs` is not added: it rounds down with a floor of 1 and disables the runtime's live updates.
- **GOMEMLIMIT**: 90% of the container memory limit, emitted as an integer byte count computed in the template, because the runtime rejects `512Mi`, the Downward API cannot apply a percentage, and the GC guide asks for 5-10% headroom. Heap in use during the benchmark was 55-85 MB with 5,882 memories loaded, against a 512Mi limit.
- **CPU**: the API waits on PostgreSQL for 95% of a query; the request is sized from measurement and a limit is kept so GOMAXPROCS and GC threads stay bounded on large nodes, with the throttling trade documented. PostgreSQL keeps no CPU limit, as the chart already argued from a 12x measured throttling loss.
- **Probes and shutdown**: the existing startup (covers InitSchema), readiness (bounded ping plus draining flag) and liveness (process-local) probes are right and are documented rather than changed; the preStop sleep plus the 15 s in-process drain is asserted in the template to fit inside `terminationGracePeriodSeconds`, with the GA `sleep` lifecycle action selectable for clusters at 1.34 or later.
- **State**: the vector index and graph live in PostgreSQL's StatefulSet volume; the API is stateless, so rebuild-on-start does not apply and warm-start is the startup probe.
- **HPA**: optional, on request rate, off by default, with the note that more API replicas do not help when PostgreSQL saturates. CPU-based scaling is deliberately not offered.
- **PDB**: `unhealthyPodEvictionPolicy: AlwaysAllow` so drains are not blocked by crash-looping pods.
- **Singleton work**: consolidation stays a CronJob with `concurrencyPolicy: Forbid` and a starting deadline; no always-on replica does singleton work, so Lease-based leader election is not needed.

## 10. Reproducing this report

```bash
make eval-fixtures                       # once; needs a local Ollama with nomic-embed-text
make eval                                # turns corpus, frozen vectors
make eval EVAL_ARGS="-corpus extracted"  # needs the local snapshot in eval/data
make eval EVAL_ARGS="-graph-signals off"
make eval EVAL_ARGS="-embedder bow"
git checkout a3811d7 -- internal/ranking internal/retrieval internal/graph && make eval   # the baseline engine
```

The frozen reports are `eval/results/baseline-*.json` and `eval/results/final-*.json`; `eval/README.md` explains every field.
