# Failure buckets, re-audited with two judges

**Date:** 2026-08-30.
**Method:** the 200-question pinned LoCoMo set (`memorybench data/pinned/locomo-200.json`), scored independently by two judge models from different vendors: `gemini-2.5-flash` (run `kora-locomo-200`) and `z-ai/glm-5.3-flash` (run `glm-baseline-200`, same answers re-generated and re-judged end to end by GLM).
**Supersedes** the single-judge bucket split (20 answering / 22 stored-but-not-retrieved / 29 absent) previously quoted from `kora-locomo-200` alone.

## Headline

| classification | n | share |
|---|---|---|
| both judges correct | 110 | 55% |
| both judges incorrect (high-confidence failures) | 51 | 25.5% |
| judges disagree | 39 | **19.5%** |

Nearly one question in five is judge-disputed.
The disputes concentrate in single-hop (13) and adversarial (12), which explains how the two judges produced near-identical totals (0.645 vs 0.650) while disagreeing by 30 points on the adversarial category.
Any single-judge category number carries roughly that much noise; per the plan of record, no change is accepted unless both judges move in the same direction.

## The 51 high-confidence failures

By question type: temporal 15, single-hop 15, multi-hop 9, world-knowledge 9, adversarial 3.

By where the evidence ranked in the retrieved top-10 (harness `hitAtK`/`mrr` against LoCoMo evidence annotations):

| evidence position | n | reading |
|---|---|---|
| rank 1 | 28 | pure answering failure: the engine put the evidence first and the answer was still wrong under both judges |
| rank 2-3 | 11 | answering failure; ranking nearly ideal |
| rank 4-10 | 6 | answering failure; better ranking might help at the margin |
| not in top-10 | 6 | retrieval failure or absent from store |

## What this changes

1. **Answering, not retrieval, dominates the high-confidence failures: 45 of 51 had the evidence retrieved, 28 at rank 1.**
   The earlier single-judge audit overstated the retrieval-side buckets because judge-disputed questions and heuristic evidence matching were folded into them.
2. **The realistic yield from retrieval-side work on this benchmark is small: at most ~6 questions (3 points) from the not-in-top-10 bucket, plus margins on the 17 ranked 2-10.**
   The graph read-path programme (#86) keeps its own justification - the ablation gate, supersedes correctness, capability classes RAG cannot serve - but LoCoMo accuracy is not where its payoff will show first.
3. **The answering bucket splits along the engine/harness boundary.**
   The harness's shared answer prompt (#58 control) and hedging/inference behaviour (#56) own most of it; extraction phrasing (#74) can still matter because *how* a memory is worded decides whether a rank-1 hit is answerable.
4. **The judge-disputed 39 are not an engine work item at all.**
   They are grader variance; the two-judge protocol exists precisely to keep them out of accept/revert decisions.

## Caveats

- `hitAtK` is the harness's fuzzy match between LoCoMo evidence annotations and retrieved text; it can over-credit retrieval when a paraphrase matches without carrying the answer.
  The 28 rank-1 failures were not hand-verified individually.
- Both runs answered with different models (Gemini vs GLM), so "both judges incorrect" conflates answering-model variance with grading variance for a handful of questions.
  The direction of the headline survives this: even under the more favourable single-run reading, evidence was retrieved for the large majority of failures.
