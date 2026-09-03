# Offline retrieval evaluation

`make eval` measures the retrieval engine against the LoCoMo benchmark without a network, a model server, or a judge.
It exists because every number this project had published was produced by language models over an API - the answer, the verdict, and even the "recall@10" - and a number that cannot be re-run cannot arbitrate a change.
The background is in [docs/WORKLOG.md](../docs/WORKLOG.md), Phase 0.

## What it measures

The 200 pinned questions (`fixtures/locomo/pinned.json`, 40 per category, the same set every published run used) are run through `retrieval.Engine.Retrieve` with the benchmark adapter's budget of 30 results.
Each ranked list is scored against LoCoMo's own evidence annotations: the dialogue turns the question was written from.

| metric | meaning |
|---|---|
| hit@k | any evidence turn in the top k |
| rec@10 | share of the question's evidence turns in the top 10 |
| full@10 | every evidence turn in the top 10; the closest offline proxy for "the answering model saw what it needed" |
| MRR@10, nDCG@10 | rank quality of the evidence, binary relevance |
| n/a | questions the corpus holds no evidence for; excluded from the means and counted instead |

Metrics are reported per category, for the 160 answerable questions, and for all 200.
Adversarial questions have no answer in the corpus; their evidence names the turn the question was built to resemble, so their row measures whether that turn is surfaced, not whether the engine abstains.

Alongside: p50/p95/p99 latency of `Retrieve` over the timing passes, allocations per query, and the size of every table and index.

## The two corpora

**turns** (default) stores each of the 5,882 utterances verbatim, rendered exactly as the adapter renders them (`On 1:56 pm on 8 May, 2023, Caroline said that ...`).
Ground truth is exact: a turn is evidence for itself.
This isolates retrieval and ranking from extraction, and it is the primary number.

**extracted** (`-corpus extracted`) is a snapshot of the 2,095 memories GLM-5.3-flash extracted for run `kora-abl-on`, the 0.690 run, with their original ids, timestamps, entity links and nomic-embed-text vectors.
Ground truth is a heuristic alignment of each memory to its source turn (`labels-extracted.json`), calibrated against the LLM judge's labels at 66% agreement; 39 of the 200 questions have no evidence turn represented in the snapshot at all and are reported as n/a.
Treat its numbers as a secondary check, and its ranked lists as the reproduction of the published run.
The snapshot is a local file (`data/corpus-extracted.json`) that cannot be rebuilt offline; `scripts/eval_fixtures.py` documents where it came from.

## Determinism

Everything the engine sees is fixed: the corpus, its ids, its timestamps, the query and corpus vectors (from `fixtures/locomo/embeddings.bin`), the ranking clock (`retrieval.Engine.SetClock`), and access counts (zero, because the eval calls `Retrieve` rather than `Query`, which increments them).
The database is recreated empty for every run.
Two runs print the same numbers and the same `digest`, a hash of every ranked list.
A run that needs a vector the fixture does not hold fails; it never falls back to a model.

Recency and frequency are the two signals that make production results depend on when and what was asked before.
The eval holds both still, so a difference between two runs is a difference in the engine.

## Files

| path | committed | what |
|---|---|---|
| `fixtures/locomo/pinned.json` | yes | the 200 question ids |
| `fixtures/locomo/locomo10.sha256` | yes | checksum of the dataset file a run must match |
| `fixtures/locomo/embeddings.bin` | yes | 8,035 float32 vectors keyed by sha256(text): the questions, the turns, and the snapshot memories |
| `fixtures/locomo/labels-extracted.json` | yes | snapshot memory id to source turns |
| `data/locomo10.json` | no | the dataset, fetched once by `make eval-fixtures` |
| `data/corpus-extracted.json` | no | the snapshot |
| `results/baseline-*.json` | yes | the frozen baseline reports |

The dataset and anything containing its text stay out of git: LoCoMo is CC BY-NC 4.0 and this repository is Apache-2.0.
Vectors and ids carry none of the text.

## Running it

```bash
make eval-fixtures                     # once: fetch the dataset, embed through a local Ollama
make eval                              # turns corpus, frozen vectors
make eval EVAL_ARGS="-corpus extracted"
make eval EVAL_ARGS="-embedder bow"    # the zero-dependency install default
make eval EVAL_ARGS="-graph-signals off"
make eval-db-down
```

`make eval-fixtures` is the only target that talks to a model server, and only for texts the fixture does not already hold.
It needs `nomic-embed-text` on an Ollama at `EVAL_OLLAMA_URL`.
