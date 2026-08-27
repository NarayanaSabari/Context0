# Improvement plan: closing the gap to Mem0 and Zep

Status: proposed, 2026-08-27
Baseline: 57.5% on LoCoMo (40 questions), MRR 0.865, recall@10 0.95

## Where we actually stand

Mem0 reports 92.5 on LoCoMo and Zep/Graphiti is the other serious open-source
graph memory engine. Two caveats before treating that as the target:

- 92.5 is Mem0's **managed platform**, which their README says "includes
  proprietary optimizations not available in the open-source SDK". Their
  open-source number is 71.4.
- They evaluate at a **top-200 retrieval budget**. memorybench gives us top-10.

So the honest gap is roughly 57.5 against 71.4, on a tighter retrieval budget.

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

**Targets multi-hop, currently 65%.** Both Mem0 and Zep extract entities as
first-class objects and link memories through them. Our graph links memory to
memory by embedding similarity, which clusters paraphrases of the same fact
rather than connecting `Caroline` to `Biscuit` to `thunderstorms`.

Mem0's April 2026 release lists entity linking as a headline change. Zep models
entities as nodes with summaries that evolve.

- Effort: large. New node type, extraction changes, retrieval changes.
- Risk: high. Touches the schema.
- Do this only after 1-4, and only if multi-hop is still the weakest category.

### 6. Real BM25 instead of `CONTAINS`

Our keyword retriever uses Cypher `CONTAINS`, which is substring matching with
no term weighting: `the` counts as much as `zqxjklmw`. Postgres ships
`ts_rank_cd`, so this is a query change rather than a new dependency.

Mem0 fuses semantic, BM25 and entity signals in parallel. We have two of the
three signals and the keyword one is weak.

- Effort: medium.
- Risk: medium, changes the candidate pool for every query.
- Expected value here is lower than it looks, because recall@10 is already 0.95.

## Sequencing

Items 1-3 are prompt changes targeting 12 of 17 failures, and are worth doing
first purely on effort-to-evidence ratio. Item 4 is a real engineering problem
with no accuracy payoff, so it should be justified by cost rather than score.
Items 5-6 are architecture, and should wait until the prompt ceiling is known.

Each step gets the same treatment: rerun the same 40 questions, compare
per-question labels rather than the aggregate, and keep the change only if the
discordant pairs favour it.

## Measurement caveat

At n=40, a 7.5-point difference was not statistically significant (McNemar
p=0.55 on the rule-vs-LLM comparison). The judge is also non-deterministic: it
graded the identical answer `Counseling and mental health` as incorrect in one
run and correct in another. Any single-step improvement smaller than about 10
points cannot be distinguished from noise at this sample size. Either accept
that, or move to the full 200-question LoCoMo set for the decisions that matter.
