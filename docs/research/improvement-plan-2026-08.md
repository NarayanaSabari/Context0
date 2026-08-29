# Improvement plan: closing the gap to Mem0 and Zep

Status: **largely superseded, 2026-08-29.** Read
[benchmark-harness](benchmark-harness.md) first.

Original status: proposed, 2026-08-27
Original baseline: 57.5% on LoCoMo (40 questions), MRR 0.865, recall@10 0.95

## What has happened since

Every number in this document comes from 40 questions of a single
conversation. Measured again on 200 questions stratified across all ten
LoCoMo conversations, the failure profile is a different shape, and three of
this document's conclusions do not survive:

- **Item 0, the retrieval budget, was already run.** `kora-step0` raised it from
  10 to 30 and bought one question of 40. This document expected it to "close
  part of the gap on its own"; it did not.
- **Item 1, verbosity, was 7 of 17 failures here and is roughly 1 of 12 on the
  wider sample.** A prompt change targeting it was tried and reverted: 5 gained,
  9 lost, and the adversarial control regressed. See issue #55.
- **Item 3, resolving relative dates, addresses almost nothing.** The temporal
  category's failures on the wider sample are inference and hedging, not date
  arithmetic, and the engine already stores absolute dates. Issue #57 is closed
  on that basis.

What does survive is the central claim, and it survives harder: at n=200, 64 of
71 failures had the evidence retrieved. The bottleneck is what happens after
retrieval, not retrieval itself.

The current failure decomposition, and what replaced these items, is in
[benchmark-harness](benchmark-harness.md). This file is kept because the
reasoning behind items 4 to 6 -- which shipped, and one of which moved the score
by ten points -- is still worth reading.

## Where we actually stand

Mem0 reports 92.5 on LoCoMo and Zep/Graphiti is the other serious open-source
graph memory engine. Two caveats before treating that as the target:

- 92.5 is Mem0's **managed platform**, which their README says "includes
  proprietary optimizations not available in the open-source SDK". Their
  open-source number is 71.4.
- They evaluate at a **top-200 retrieval budget**. memorybench gives us top-10.

So the honest gap is roughly 57.5 against 71.4, on a tighter retrieval budget.

## Step 0: we are not benchmarking on equal terms

Found while reading how the other providers are wired into the same harness.
Every provider sets its own retrieval budget in its memorybench adapter:

| Provider | top_k in adapter |
|----------|------------------|
| supermemory | 30 |
| mem0 | 30 |
| zep | 20 |
| rag / filesystem | 10 |
| **kora** | **10** |

We ask for 10 where mem0 asks for 30. Worse, the engine then silently clamps
anything above 20 in `ParseQuery`, and `memory.proto` documents `top_k` as
"Maximum number of results to return" with no mention of a cap. A caller asking
for 50 gets 20 and is never told.

That makes every comparison so far unequal in two independent ways, and the
undocumented clamp is a genuine API bug regardless of benchmarking.

Two changes, both small:

1. Raise the adapter's default to 30, matching mem0 and supermemory.
2. Either raise the engine's clamp and document it, or return an error when the
   caller exceeds it. Silently returning fewer results than requested is the
   one behaviour that cannot be right.

This has to happen **before** anything below, because it changes the baseline
every later measurement is compared against. It may also close part of the gap
on its own: recall@10 is already 0.95, so the ceiling is what reaches the
answering model, and that is exactly what top_k controls.

## What the failures actually are

All 17 failures from the 40-question run, categorised by hand:

| Count | Category | Example |
|-------|----------|---------|
| 7 | Answer too verbose | truth `The week before 9 June 2023`, got that plus two unrelated dates |
| 5 | Answered "I don't know" | 3 are `Would X...?` inference questions |
| 2 | Date format / arithmetic | truth `The sunday before 25 May 2023`, got `May 20, 2023` |
| 2 | Retrieval genuinely missed | hit@10 = 0 |
| 1 | Dropped a qualifier | truth `counseling for Transgender people`, got `counseling` |

**15 of 17 failures had the right memory retrieved.** Recall@10 is 0.95.
Retrieval is not the bottleneck. The bottleneck is what happens after it.

### A correction to an earlier claim

I previously said 89% of stored memories are duplicates and that this was
crowding out retrieval. The first half is true: 6,010 stored, 573 unique,
90.5% redundant. The second half is not. Measured on the actual retrieved
results, only **1% of retrieval slots are exact duplicates and 2% are near
duplicates**. Ranking already suppresses them.

Deduplication is therefore a **storage cost and write-amplification** problem
(6,010 rows and 6,925 edges where ~600 would do), not an accuracy problem. It
should be scheduled on that basis, not as an accuracy fix.

Similarly, precision@10 of 0.42 is not a redundancy signal here: LoCoMo
questions have a mean of 4.2 relevant sessions, so returning 10 memories caps
precision structurally.

## The work, in order of measured value

### 1. Answer prompt: answer only what was asked

**Targets 7 failures.** Our prompt says "answer in as few words as the question
allows", which the model ignores when it has several candidate facts. It
concatenates them, and LoCoMo's judge marks the extra content wrong.

Change the provider answer prompt to demand a minimal answer: the specific
value asked for and nothing else, no restatement of the question, no additional
facts however relevant they seem.

- Effort: one prompt, one benchmark run.
- Risk: low. Retrieval and storage untouched.
- Falsifiable: rerun the same 40 questions, compare per-question labels.

### 2. Let the model infer when the question asks it to

**Targets 3 failures.** `Would Caroline pursue writing as a career?` has truth
`Likely no`. Our prompt forbids going beyond the memories, so the model
correctly refuses, and is marked wrong.

Add a rule: when the question asks what someone would likely do, reason from
the stored facts and commit to an answer, while still refusing when no relevant
memory exists. The distinction is between inference and fabrication.

- Effort: one prompt rule.
- Risk: medium. Could turn correct abstentions into hallucinations; the
  benchmark has an abstention category that will show it.

### 3. Resolve relative dates at extraction time

**Targets 2-4 failures.** The extractor currently stores
`Caroline gave a talk at a school event last week (from 9 June 2023)`. It keeps
the relative phrase and appends the anchor. LoCoMo's truth is
`The week before 9 June 2023`, and questions ask for a resolved date.

Because retrieval returns one memory alone, an unresolved `last week` is
unanswerable without the anchor the reader does not have. The prompt already
demands specifics survive; it needs the same for temporal references.

- Effort: prompt rule plus a test, same shape as the proper-noun fix in a7b387c.
- Risk: low.
- Note: this is the same class of bug as "her home country" for "Sweden", which
  suggests the extraction prompt needs a general rule about resolving anything
  that depends on context the reader will not have.

### 4. Consolidation on write

**Targets storage and write amplification, not accuracy.** 6,010 memories for
573 distinct facts. `Caroline is transgender.` is stored 25 times, and
`Caroline is a transgender person.` another 16 times as a separate row.

Two mechanisms, cheapest first:

- **Exact-content dedup** within a project. A content hash lookup before insert.
  Removes the 90.5%.
- **Semantic dedup**, the Mem0 approach: compare a new fact against existing
  ones and UPDATE rather than ADD when they express the same thing. Kora already
  has `detectAndSupersede` for exactly this, and `Extract` never calls it, only
  `Store` does. That gap is the immediate finding.

Expected effect: ~10x fewer rows and edges, proportionally cheaper ingest,
cheaper consolidation, smaller vector index. No accuracy claim.

- Effort: small for exact dedup; medium for wiring `detectAndSupersede` into
  `Extract` and paying its 50-candidate query per memory.
- Risk: medium. Superseding is destructive to ranking order; needs the same
  both-directions testing as the ranking weights in c7e9309.

### 5. Entity extraction and linking

**Targets multi-hop, currently 65%.** Our graph links memory to memory by
embedding similarity, which clusters paraphrases of the same fact rather than
connecting `Caroline` to `Biscuit` to `thunderstorms`.

Mem0's OSS implementation is worth copying closely, because it is cheaper than
it sounds. `mem0/utils/entity_extraction.py` uses **spaCy, not an LLM**: it
takes named entities from spaCy's NER (keeping PERSON, ORG, GPE, LOC, PRODUCT,
WORK_OF_ART and rejecting DATE, TIME, CARDINAL), quoted strings, and noun
compounds, then filters them against a list of heads too generic to be useful
(`thing`, `way`, `time`, `topic`). Entities are embedded and stored in a
**separate collection**, and `_upsert_entity` links each one to the memories it
appears in via `linked_memory_ids`, deduplicating by exact normalized text
first and semantic similarity at 0.95 second.

At retrieval, a memory whose entities match the query gets a boost. No LLM call
on the write path, which matters because our extraction call already costs 8.5s
per session.

Go has no spaCy. The realistic options are a CGo binding (rejected: the project
is deliberately dependency-light), an LLM pass to name entities as part of the
existing extraction call (free, since we already make it, and the prompt
already asks for tags), or a small POS-free heuristic over capitalised spans
and quoted strings. The middle option is closest to our existing design: extend
the extraction prompt to return `entities` alongside `content` and `tags`.

- Effort: medium if entities come from the extraction call we already make.
- Risk: medium. New node type, retrieval changes.

### 6. Real BM25 instead of `CONTAINS`, with fusion

Our keyword retriever uses Cypher `CONTAINS`, which is substring matching with
no term weighting: `the` counts as much as `zqxjklmw`. Postgres ships
`ts_rank_cd`, so this needs no new dependency.

Mem0's fusion is simple enough to copy directly (`mem0/utils/scoring.py`):

```
combined = (semantic + bm25 + entity_boost) / max_possible
```

where `max_possible` adapts to which signals are present (1.0 semantic only,
2.0 with BM25, 2.5 with entity at `ENTITY_BOOST_WEIGHT = 0.5`), the semantic
threshold gates candidates *before* combining, and raw BM25 is squashed into
[0,1] by a logistic sigmoid whose midpoint and steepness vary with query length
(5.0/0.7 for <=3 terms up to 12.0/0.5 for >15).

Two things stand out against our current `RelevanceTier`. Theirs is additive
where ours is strictly tiered: we rank any lexical match above any
non-match, which is a stronger claim than the evidence supports. And they
normalise per query length, which is exactly the calibration problem we hit
with `relatedStdDevs`.

- Effort: medium.
- Risk: medium, changes the candidate pool for every query.
- Expected value is lower than it looks, because recall@10 is already 0.95;
  this mostly helps precision and ordering.

## Sequencing

**Step 0 first**: equalise `top_k` and fix the undocumented clamp. Until that is
done, every measured comparison against mem0 is unequal, and the baseline that
items 1-6 are judged against is wrong.

Items 1-3 are prompt changes targeting 12 of 17 failures, and are worth doing
next purely on effort-to-evidence ratio. Item 4 is a real engineering problem
with no accuracy payoff, so it should be justified by cost rather than score.
Items 5-6 are architecture, and should wait until the prompt ceiling is known.

Each step gets the same treatment: rerun the same 40 questions, compare
per-question labels rather than the aggregate, and keep the change only if the
discordant pairs favour it.

## Reaching 90+

The 90+ target is a separate question from parity, and worth stating plainly:
Mem0's 92.5 is their managed platform at a top-200 budget, and their own OSS
number is 71.4. Parity with OSS is the reachable goal from here. Beating it
means either the same techniques executed better, or accepting the same
trade-off they made, which is a much larger retrieval budget and more tokens
sent to the answering model.

Nothing in items 0-6 requires a bigger model or more spend, which is the right
constraint to hold until the cheap wins are exhausted.


## Measurement caveat

At n=40, a 7.5-point difference was not statistically significant (McNemar
p=0.55 on the rule-vs-LLM comparison). The judge is also non-deterministic: it
graded the identical answer `Counseling and mental health` as incorrect in one
run and correct in another. Any single-step improvement smaller than about 10
points cannot be distinguished from noise at this sample size. Either accept
that, or move to the full 200-question LoCoMo set for the decisions that matter.
