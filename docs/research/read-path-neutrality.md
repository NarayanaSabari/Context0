# Read-path changes are accuracy-neutral on LoCoMo-200

**Date:** 2026-09-01.
**Supersedes** the conclusions of [fresh-corpus-blind-spot](fresh-corpus-blind-spot.md), whose measurements were invalidated by a script bug (wrong JSON key; see the correction note there).

## The corrected measurements

All comparisons against `kora-abl-on` (graph on, fresh GLM+Ollama corpus, 0.690), correct reader:

| arm | top-10s changed | score | discordant (±) | McNemar p |
|---|---|---|---|---|
| graph off (`kora-abl-off`) | 200/200, Jaccard 0.77 | 0.690 | 11 / 11 | 1.00 |
| entity IDF (`kora-idf-on`) | 198/200 | 0.685 | 17 / 18 | 1.00 |
| supersedes demotion (`kora-demote-on`, 144 evaluated) | 199/200 | 0.701 vs 0.701 on subset | 12 / 12 | 1.00 |

Before demotion, superseded memories sat in **151 of 200** top-10s (305 entries); demotion removes most of them.
The retrieval surface genuinely changes under every variant.
The score does not move under any of them.

## The conclusion that survives every correction

**On this benchmark, answer correctness is not retrieval-bound.**
Recall@10 sits at 0.855-0.870 under every variant: the evidence is nearly always retrieved, and what decides correctness is what the answering model and judge do with it.
This is the same conclusion the two-judge bucket re-audit reached from the failure side (45 of 51 high-confidence failures had the evidence retrieved, 28 at rank 1), now confirmed from the intervention side: three different retrieval configurations, three identical scores.

Implications:

1. **LoCoMo-200 accuracy cannot arbitrate read-path changes.** Every balanced 11-18 discordant-pair result is what this pipeline produces regardless of whether retrieval changed. The golden suite - deterministic, rank-asserting - is the instrument that can, and it is already a CI gate.
2. **The #86 merge bar needs restating.** "Graph-on must beat graph-off on the pinned set" is unfalsifiable in practice for changes of this size. The honest bar: golden-guarded improvement + retrieval-metric non-regression + LoCoMo non-regression.
3. **Accuracy movement lives in the answering bucket** (#56, #58, #74), as the re-audit already said.

## Process note

The bug (reading `searchResults` where files hold `results`) produced silently-empty comparisons that confirmed a plausible hypothesis, and one full soak-corpus detour was built on top of it before a length-zero check exposed it.
Two rules adopted: every comparison script asserts non-empty inputs before comparing, and every "surprising null result" gets its reader verified against one raw file first.
