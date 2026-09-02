# Handover: the optimization branch and the buildathon submission

Written 2026-09-02 for whoever picks this up.
Everything below is verifiable from the repository; where something is a belief rather than a fact, it says so.

## 1. What this is, in one paragraph

Kora is a Go memory engine for AI agents: memories live in PostgreSQL with Apache AGE (graph) and pgvector (vectors), behind a gRPC/REST API, deployed by a Helm chart.
The branch `perf/memory-optimization` (PR [#100](https://github.com/NarayanaSabari/Kora/pull/100), 11 commits on top of `main`) did four things: built an offline benchmark because the published 69% accuracy could not be reproduced, used that benchmark to fix retrieval, profiled and sped up the read path, and tuned the Kubernetes chart.
On top of that, the repository is being submitted to the [Razorpay AI Buildathon](https://razorpay.com/buildathon/), track "AI Revenue Recovery", with a demo agent in `examples/receivables-chaser/`.

## 2. Where things are

| what | where |
|---|---|
| the full technical report (baseline vs final, ablation table, failure analysis, papers, K8s rationale) | `docs/OPTIMIZATION_REPORT.md` |
| the step-by-step log with every hypothesis, measurement and revert | `docs/WORKLOG.md` |
| the offline benchmark and how to run it | `eval/README.md`, `make eval` |
| the chart settings and their reasons | `docs/kubernetes.md` |
| the demo agent | `examples/receivables-chaser/README.md`, `make demo`, `make demo-test` |
| the 5-minute pitch script and the panel questions | `docs/PITCH.md` |
| the judge-facing summary | top of `README.md`, section "Measured, not claimed" |
| frozen benchmark reports | `eval/results/baseline-*.json` (engine before), `eval/results/final-*.json` (engine after) |

## 3. The story the submission tells

1. The reported 69% was an LLM judge grading an LLM answer, with the "recall@10" also LLM-labelled. It cannot be re-run without an API.
2. `make eval` replaces it: the real engine, 200 pinned LoCoMo questions, scored against the dataset's own evidence annotations, frozen vectors, fixed clock, identical output every run.
3. Of 44 answerable misses, 43 were evidence the engine had already retrieved and then buried by a saturated keyword score. Per-query normalisation fixed most of it: hit@10 0.722 to 0.766, recall@10 0.590 to 0.646, MRR@10 0.491 to 0.518.
4. Profiling showed 95% of a query waiting on PostgreSQL round trips. Four changes, each leaving every ranked list byte-identical, took p50 from 24.9 to 16.7 ms and halved allocations.
5. The entity graph signal the project is named for measured +0.005 MRR. It was left in at its measured weight and written down, not hidden.

Every number in the README and the report was produced by `make eval`. Do not add a number from anywhere else.

## 4. Current state, exactly

- Branch `perf/memory-optimization` is pushed; PR #100 is open against `main`, not merged.
- CI on the head commit `af0ce15`: Go Security (gosec) passes, Unit Tests pass, Retrieval Regression passes, Dependency Review passes; Lint, Build, Integration Tests, Container Scan were still running when this was written. All of them passed on earlier commits of the branch, so a failure would be new.
- The working tree is clean.
- Local throwaway databases were torn down. Two Docker containers must stay: `kora-abl-pg` (port 55440) holds the corpus behind the 69% run and is the only source of `eval/data/corpus-extracted.json`; `kora-ollama` (port 11435) serves `nomic-embed-text` for rebuilding the embedding fixture. `kora-abl-api` (port 18092) is the benchmark engine the demo was verified against.
- The Trivy container scan reports pre-existing CVEs in the base images; they predate this branch and were not touched.

## 5. What the person taking over must do

**Before the video (tomorrow, 2026-09-03):**
1. Run `make demo` once and read the report it prints; the pitch script narrates it tick by tick.
2. For the live beat, start the engine (`docker compose up -d`, credentials in `.env` per the README), export `KORA_URL` and `KORA_API_KEY`, and run `make demo` again. Use `--days 14` if the live run's ~2 minutes is too long on camera; the SDK opens one HTTP connection per call, which is the cause and is outside the demo's scope.
3. Read `docs/PITCH.md` end to end and have the "why not an LLM here" answers ready.

**Before submitting:**
4. Confirm the form deadline and the student-only eligibility on the [application form](https://forms.gle/d9r2gvxp8cmoZhon9). A third-party post says applications close 2026-09-05; the official page shows no date.
5. Decide the track: Revenue Recovery (recommended, the demo fits its stated bar) or Open.
6. Razorpay test-mode keys go in a local `.env` only if you want the live Razorpay path on camera; the recorded fixture is what the demo uses otherwise. Never commit `.env`.
7. Merge PR #100 when CI is green, or submit the branch URL; the README's numbers are on the branch, not on `main`.

## 6. Decisions already made, so they are not re-litigated

- Fusion is per-query min-max with weights 0.45 / 0.40 / 0.15 and a keyword coverage exponent of 0.25. Each value was swept; the reasons, including why 0.40 / 0.45 was rejected, are in the worklog (Track A changes 1 to 3).
- Reciprocal rank fusion was measured and is worse here (MRR 0.453 vs 0.499). It stays selectable for ablation only.
- The graph entity signal stays at weight 0.15 because nothing measured says to change it, not because it helps.
- LoCoMo's numeric category codes are 1 multi-hop, 2 temporal, 3 open-domain, 4 single-hop, 5 adversarial. The memorybench harness and every `docs/research/` file before this branch used the wrong labels; the translation table is in the worklog.
- LoCoMo is CC BY-NC 4.0, so its text and the extracted snapshot stay in the gitignored `eval/data`; only ids, labels, vectors and result reports are committed.
- The storage format was not changed; every persisted memory, edge and embedding stays readable by the previous engine.

## 7. Known trade-offs and what is left on the table

- Under the install-default bag-of-words embedder, the new fusion drops LoCoMo hit@10 from 0.639 to 0.570 with MRR flat. The golden gate on that embedder still passes with a higher MRR. A per-embedder fusion default (linear for hashed vectors) is the fix and needs its own gated measurement.
- Extraction drops every evidence memory for 36 of 200 questions before retrieval sees them. LongMemEval's key merging (verbatim round plus facts indexed as one key) is the measured remedy; it needs a fixture rebuild through Ollama.
- The FTS statement is 9 ms of the remaining 17 ms per query. A stored tsvector column is the standard fix and is a schema change, which was reserved for the owner's decision.
- Hydration of 500 candidates per query and the sequential retrievers are the next two latency steps; both are described in the report.
- `charts/kora/templates/networkpolicy.yaml` says kind's default CNI ignores NetworkPolicy; on kind v0.31.0 it enforces it. Stale comment, not fixed.

## 8. How to run the things

```bash
make test                                  # unit tests, no database
make eval                                  # offline benchmark; needs Docker and eval/data/locomo10.json
make eval EVAL_ARGS="-corpus extracted"    # needs the local snapshot (see section 4)
make eval EVAL_ARGS="-embedder bow"        # the zero-dependency default
make eval EVAL_ARGS="-graph-signals off"   # the RAG ablation
make eval-fixtures                         # only to add vectors for new texts; needs Ollama
make demo && make demo-test                # the agent and its 40 tests
make test-golden                           # the CI regression gate, needs docker compose
helm lint charts/kora && helm template kora charts/kora --set postgres.password=x --set auth.apiKeys=k
```

Gotchas that cost time: Go tests that bind ports (`cmd/server`, `internal/embedding`, `internal/extraction`) and anything touching Docker fail inside a sandboxed shell; `make eval` refuses a database that already holds a corpus unless `-reuse-db` is passed; `KORA_EVAL_DATABASE_URL` must be set (`scripts/eval_db.sh up` prints it, `make eval` does this for you).

## 9. If you have one hour

Read `docs/OPTIMIZATION_REPORT.md` sections 1, 3 and 5, run `make demo`, read `docs/PITCH.md`. That is enough to present it.
