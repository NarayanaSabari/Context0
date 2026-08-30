# Sizing the supersedes demotion from the bench corpus

**Date:** 2026-08-30.
**Corpus:** the Gemini-era bench corpus (25,568 memories, 2,322 supersedes edges after the final soak) and the stored search results of the pinned 200-question run `kora-locomo-200` (k=30 per query, retrieval metrics at k=10).
**Feeds:** the supersedes-demotion change, first PR of the graph read-path programme (#86).

## The defect, measured

A supersedes edge records that a new memory replaces an old one, and retrieval ignores it: both rank purely on relevance, and near-paraphrases score near-identically.

| observation | value |
|---|---|
| queries whose k=30 results contain at least one superseded memory | 189 of 200 (94%) |
| superseded memories surfaced across all queries | 741 |
| result lists holding **both** members of a pair | 119 |
| of those, stale ranked **above** its successor (inversions) | 60 (50%) |
| inversions inside the top-10 the answering model sees | 31 |
| high-confidence failures (both judges wrong) whose results contain an inversion | 13 of 51 |

The coin-flip inversion rate is exactly what "the edge is written and never read" predicts.

## Sizing the factor

For each inversion, the score ratio successor/stale says how much demotion flips it:
median 0.983, p90 0.998 - the pairs score almost identically, so a small nudge fixes most, and nothing short of a real demotion fixes the tail.

Multiplying the composite score of a superseded memory by factor f flips the observed inversions at:

| factor | inversions flipped |
|---|---|
| 0.9 | 45/60 (75%) |
| 0.8 | 53/60 (88%) |
| 0.7 | 56/60 (93%) |
| **0.6** | **60/60 (100%)** |

## Decision inputs for the PR

- **0.6 covers every observed inversion**; anything below buys nothing this corpus can demonstrate.
- Surfaced stale memories carry edge weights of mean 0.82, min 0.50.
  A weight-scaled demotion (weaker edges demote less) is defensible but adds a knob; the observed distribution does not obviously require it, since even min-weight edges sat in real inversions.
- Demotion, not filtering: the memory stays in the candidate set and the result list, so history questions ("where did X use to live") can still surface it.
  The golden current-truth group must pin both directions: successor above stale, and stale still reachable.
- These are old-corpus numbers.
  The mechanism is corpus-independent, but the acceptance run happens on the rebuilt GLM+Ollama corpus under the standard protocol (pinned 200, both judges, McNemar).

## Caveat

The score ratios come from the composite score the harness stored, which already includes recency/frequency/type terms.
Applying the factor to the relevance signal instead of the composite changes the arithmetic slightly; the implementation should re-verify the flip rate at its chosen application point before claiming the 100% figure.
