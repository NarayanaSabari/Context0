# Plan of record

Agreed 2026-08-29, against engine commit `2d72fec`.

The goal this plan serves: publish a defensible LoCoMo number for Kora from the [memorybench](https://github.com/supermemoryai/memorybench) harness.
Everything below either makes that number trustworthy or removes something that would embarrass it.

Work items live as GitHub issues once filed.
This file records the sequence and the gates, which an issue list cannot express.

## Decisions this plan assumes

Vocabulary is in [CONTEXT.md](../CONTEXT.md).
Two decisions are recorded as ADRs: [Apache AGE is load-bearing](adr/0001-apache-age-is-load-bearing.md) and [one deployment is one trust domain](adr/0002-one-deployment-is-one-trust-domain.md).

Three more decisions carry no ADR because nothing about them is surprising, but the plan depends on them:

- Install defaults stay zero-dependency (`bag-of-words` embeddings, `rule` extraction).
  The engine must say so at startup and in health output rather than degrading silently.
- The Kubernetes Operator is retired as a goal.
  Helm plus the consolidation CronJob is the deployment story.
- The repository and working directory are renamed to Kora.
  The AGE graph name `context0`, the `ctx0_` key prefix, and the Cloudflare Pages project keep their names, because each is data-bearing or credential-bearing.

## Phase A: measurement before movement

Nothing in Phase B may start until A1 lands, and no number may be published until A2 and A3 land.

**A1. In-repo retrieval regression suite.**
30 to 50 fixed cases of (corpus, query, expected memory), asserting recall@k and MRR against committed thresholds.
Runs as a fourth CI job on the compose Postgres+AGE stack the integration job already starts.
This gates every PR and may fail a build.
It exists because the last five commits changed retrieval and nothing in this repository could have caught a regression in them.

**A2. Custody of the benchmark harness.**
Two divergent copies of the harness exist locally, both with `origin` pointing at upstream, one four commits ahead and unpushed.
Fork `supermemoryai/memorybench` under `NarayanaSabari`, push those commits, diff `~/Developer/narayana/_bench/memorybench` for anything worth keeping and then delete it.
Record the fork URL, the adapter commit, and the pinned upstream commit in [research/README.md](research/README.md).
Open the provider upstream as a PR once the numbers stabilise.

**A3. Equalise the retrieval budget, then rebaseline.**
The adapter still sends `options.limit || 10` where mem0's sends 30, and the engine's clamp was raised to 200 and documented in `2d72fec`, so only the adapter side is left.
Set 30, rerun the same 40 questions, and treat that result as the baseline.
57.5% is retired at this point: it was measured at a third of the retrieval budget of the engine it was compared against.

## Phase B: resume the improvement plan

The three unstarted items from [improvement-plan-2026-08](research/improvement-plan-2026-08.md), which together target 12 of 17 known failures.
Items 4 to 6 of that plan already shipped, ahead of these.

**B1. Minimal answers.** Adapter prompt: answer the question asked, nothing else.
**B2. Permit inference.** Adapter prompt: reason from stored memories when a question asks what someone would likely do, while still abstaining when no relevant memory exists.
**B3. Resolve relative dates at extraction.** Engine-side extraction prompt: store a resolved date, not `last week` plus an anchor.

The fairness rule for B1 and B2, which are harness-side: an adapter prompt may shape answer format and abstention policy only.
It may never carry domain knowledge or per-question hints.
Run one control with the harness's shared prompt so the report can separate engine points from prompt points.
If that gap is large, the fix belongs in the engine's output shape instead.

Each item is judged on per-question labels, not the aggregate.

## Phase C: hygiene, not gated on anything

**C1.** Degraded-mode visibility: startup warning naming the embedding and extraction providers in use, the same in `/v1/health`, and a documented quality values file for the chart pointing at Ollama.
**C2.** Split `ARCHITECTURE.md` into shipped architecture and `docs/vision.md`; the aspirational data model, scoping model, operator and topology sections move.
Fix the README's reference to `internal/llm/`, which does not exist, and drop the operator from the roadmap.
**C3.** Rename repository and directory to Kora, and document why the three `context0` remnants stay.
**C4.** Document the trust boundary in `SECURITY.md` and on `project_id` in `memory.proto`.
**C5.** `Profile` takes a result cap and a recency window as request parameters, defaulting to today's hardcoded 200 and 7 days.

## Phase D: structure

**D1.** Extract the read path into `internal/retrieval`: candidate pools, entity matching, merge, fusion.
Then the write path into `internal/ingest`: extraction, fold, supersede detection, embedding.
`internal/service` is left doing protocol translation.
A narrow repository interface at that boundary is a test seam, not a portability claim.

Requires A1: this is a refactor of the code the golden set covers, and doing it without the net is the wrong order.

## Phase E: the benchmark run

Two labelled runs, not one: the quality configuration as the headline, and one default-configuration run beside it, each stating embedding provider, extraction provider, engine commit, and adapter commit.

memorybench never gates a build.
Results land in `docs/research/` with per-question labels so runs stay comparable.

At n=40, with a judge that has graded the same answer both ways in different runs, no improvement smaller than about 10 points is claimed.
Move to the full 200-question set before any decision that depends on a smaller difference.

## Phase F: the graph read path (spec: issue #86)

Settled 2026-08-30 after the two-judge re-audit ([failure-buckets-two-judge](research/failure-buckets-two-judge.md)).

The claim "graph memory engine, not RAG" becomes falsifiable: an ablation switch collapses retrieval to FTS+vector, and graph-on against graph-off on the pinned 200-question set is measured continuously.
Four read-path changes connect structure the engine already writes: supersedes demotion, entity IDF, entity linking, `relates_to` expansion.
One PR per change; every weight measured, never chosen; accept on McNemar p < 0.05 with both judges agreeing in direction; two consecutive failures trigger reassessment.

The re-audit resizes expectations, not the design: answering failures dominate LoCoMo (45 of 51 high-confidence failures had the evidence retrieved), so Phase F's payoff is measured by the ablation gap and the capability suites, not by the headline accuracy.
The answering bucket belongs to the harness-prompt control and hedging work, which follow it.
