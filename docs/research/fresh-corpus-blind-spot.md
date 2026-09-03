# The fresh benchmark corpus cannot see accumulation defects

> **CORRECTION, 2026-09-01: the central measurements in this document are wrong.**
> The comparison scripts read `searchResults` from result files whose key is `results`, so they compared empty lists and reported vacuous equality.
> Re-measured with the correct key: entity IDF changed 198 of 200 top-10s (not zero), supersedes demotion changed 199 of 200 (not zero), and the fresh corpus had superseded memories in 151 of 200 top-10s before demotion (not zero).
> The "noise floor" derived from the IDF arm is also invalid, because its retrieval inputs were not identical after all.
> The structural explanations below (single-entity rescaling, accumulation blindness) were built to explain measurements that did not exist; they are retained as a record, struck through in spirit, and superseded by [read-path-neutrality](read-path-neutrality.md).
> What survives: the golden-suite guards, the sizing measurements, and the ablation baseline (whose comparison used a correct reader).

**Date:** 2026-08-31.
**Context:** the first two Phase 1 read-path signals (entity IDF, PR #92; supersedes demotion, PR #97) both measured Δ=0 on the pinned 200-question LoCoMo protocol, for the same structural reason.

## What was measured

| signal | golden suite | pinned-200 protocol |
|---|---|---|
| entity IDF | guard case rank 2→5 without it; floor trips | top-10 identical on 200/200 queries |
| supersedes demotion | stale fact retakes rank 1 without it; floor trips at MRR 0.500 | top-10 identical on 200/200 queries |

Neither is a measurement artefact.
Both mechanisms fire on the benchmark corpus - the demotion counter recorded ~30 demotions per query - and change nothing the answering model sees.

## Why

**Entity IDF** reorders only mixed-entity candidate sets.
LoCoMo queries name one person; when every candidate matches the same single entity, discounting that entity rescales all candidates equally, and order is preserved.

**Supersedes demotion** reorders only where a stale fact outranks its successor inside the visible top-K.
On the fresh corpus, zero of the 344 superseded memories appear in any top-10 (they sit in the deep candidate pool).
On the old bench corpus - 25,568 memories accumulated over 18 ingestion runs - superseded memories appeared 741 times in k=30 results with 60 inversions, 31 inside top-10, and stale beat successor in a coin flip.

The difference is corpus age, not corpus quality.
A single-pass ingest of clean extractions has few contradiction pairs and ranks them correctly by accident of recency and phrasing.
A store that lives - re-statements, updates, weeks of writes - accumulates exactly the stale-outranks-current defect the demotion fixes.
The old soak corpus modelled a long-lived store; the fresh corpus models day one.

## Implication for the merge bar

Issue #86's bar - "graph-on must beat graph-off on the pinned set" - implicitly assumes the pinned set can detect the fix.
For write-accumulation defects it cannot: **a benchmark that re-ingests its corpus from scratch each time structurally cannot reward fixes to problems that only accumulation creates.**

Options, in increasing cost:

1. **Merge on golden + non-regression.** The golden suite reproduces each defect shape deterministically and guards it; the protocol run proves no harm. The LoCoMo delta is then evidence about the benchmark, not the fix.
2. **A soak arm**: re-ingest the benchmark conversations several times into one project (simulating a long-lived store), then run the pinned questions against it. The old corpus's inversion counts predict the demotion becomes visible immediately. Cost: a few hours of GLM extraction, ~$1.
3. Hold everything until a real longitudinal workload exists. Cost: indefinite delay on measured fixes.

## Status

- `kora-demote-on` finishes as a third noise-floor sample (identical retrieval inputs).
- Both PRs held pending this decision.
