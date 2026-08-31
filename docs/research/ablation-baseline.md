# Ablation baseline: the graph contributes zero

**Date:** 2026-08-30.
**Method:** two runs on one corpus, differing only in `KORA_GRAPH_SIGNALS`. Engine at `d5c70ee`, corpus rebuilt end-to-end without Gemini: GLM-5.3-flash extraction (via OpenRouter), Ollama `nomic-embed-text` embeddings (768d), 2,537 memories from the 272 LoCoMo sessions behind the pinned 200-question set (`data/pinned/locomo-200.json` in the memorybench fork). Both arms answered and judged by GLM-5.3-flash. Runs: `kora-abl-on`, `kora-abl-off`.

## Result

| arm | accuracy | recall@10 | MRR |
|---|---|---|---|
| graph signals on | 0.690 (138/200) | 0.855 | 0.740 |
| graph signals off (FTS + vector only) | **0.690 (138/200)** | **0.870** | **0.751** |

Identical accuracy.
Retrieval metrics marginally *better* with the graph off.
Per question: 11 helped by the graph, 11 hurt, McNemar p = 1.000.
Every top-10 differed between arms (mean Jaccard 0.77), so the signal is active and reshaping results - it just reshapes them to no net effect.

**Today's engine retrieves exactly as well as the RAG ablation of itself.
The graph's query-time contribution, measured, is zero.**

## Where the graph actively hurts

The hurt side is not noise: 6 of the 11 losses are adversarial questions (helped: 1).
The entity signal pulls in generic entity-matched memories, and that extra topical-but-irrelevant context baits the answering model into answering questions whose correct answer is "no answer".
This is the retrieval-side face of the known discrimination defect: an entity mentioned by most of a project's memories carries no information, and boosting on it adds noise precisely when the right response is restraint.

## What this establishes

1. **The falsifiability gate works, and its first reading is honest: the "graph memory engine" claim is currently false at query time.**
   This is the number the ablation flag exists to move.
2. **Phase 1 (#86) has its baseline: the gap to beat is 0.000.**
   Each read-path signal (supersedes demotion, entity IDF, entity linking, expansion) must show graph-on above graph-off under the standard protocol, or it does not merge.
3. **Entity IDF is the obvious first candidate**: the adversarial losses are exactly what inverse-mention-frequency weighting attacks.
4. The rebuilt corpus outscores the Gemini-era one (0.690 vs 0.645-0.650) with better single-hop (0.55 vs 0.45-0.475).
   Suggestive for the extraction-phrasing hypothesis (#74) - GLM naturally extracts kind-statements like "Caroline makes abstract art" - but confounded: extractor, embedder and judge all changed together.
   The #74 A/B on this corpus is what separates them.

## Provenance

- 5 of 272 sessions fell back to rule-based extraction (GLM response exceeded the extraction client timeout), visible via `kora_extraction_fallbacks_total{reason="error"}` - the counter shipped the same morning.
  ~2% contamination, identical in both arms.
- Both arms share the identical ingest; arm OFF re-ran search only, from the arm ON checkpoint at the search phase.
- Old-corpus numbers (0.645 Gemini-judged / 0.650 GLM-judged) are not comparable to these: different extractor, embedder, and store.
  Within-corpus comparisons only.
