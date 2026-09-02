# Pitch script (5 minutes) and architecture walkthrough

Recorded against `make demo` in one terminal and the README's table in another.
Times are cumulative.

## 0:00 The problem (40 s)

"A merchant's receivables agent that chases overdue invoices nags, because it has no memory.
It does not know that this customer promised to pay on the 7th, that this invoice is disputed, or that it already sent two reminders this week.
Kora is a memory engine for agents: store conversations, extract facts, query by meaning, run it in your own cluster.
Today I will show it doing that job, and then show you how we measure it, because the interesting part of this project is what we found when we measured."

## 0:40 The demo (90 s)

Run `make demo`. Narrate the first tick: 50 synthetic invoices, the agent queries Kora for each customer, finds nothing, sends gentle reminders.
Tick 3: a customer replies with a promise; the agent stores it. Tick 5: the agent skips that customer because the promise date is in the future, and escalates a disputed invoice to a human instead of chasing it.
Show the final report: amount recovered by rung, promises kept and broken, and the exception list with reasons.

Say out loud: "Every decision here is a rule with a name in the audit log.
The only thing an LLM does is reword the message, and the tests mock it.
Forcing a model into the decision would have made the audit trail unexplainable."

## 2:10 The measurement story (80 s)

Switch to the README table.

"This engine was reported at 69% on the LoCoMo benchmark.
When we tried to reproduce that offline we found it could not be: the answer, the verdict and even the recall number were all produced by a language model over an API.
So we built a deterministic benchmark: the real engine, 200 questions, scored against the dataset's own evidence annotations, no model calls, identical output on every run.

Then we bucketed the misses.
Of 44 failures, one was a real retrieval miss.
Forty-three were evidence the engine had already retrieved and then buried, because the keyword score saturated and outvoted a compressed cosine similarity.
Normalising both signals per query moved hit@10 from 0.72 to 0.77 and recall@10 from 0.59 to 0.65.
And the graph signal the project is named after measured +0.005 MRR.
We left it in at its measured weight and wrote that down."

## 3:30 Performance and production (50 s)

"Profiling showed the query spent 95% of its time waiting on PostgreSQL round trips.
Four changes, each verified to leave every ranked list byte-identical, took the median query from 25 to 17 milliseconds and halved allocations.
The Helm chart sets the Go memory limit at 90% of the container limit, leaves GOMAXPROCS to the runtime because Go 1.26 reads the cgroup itself, and documents why the API is not autoscaled on CPU: more API pods do not help when the database is the bottleneck."

## 4:20 Close (40 s)

"What is left: the extraction step drops the evidence for 36 of 200 questions before retrieval ever sees it, and the fix is measured in the literature.
The repo has the benchmark, the profile, the worklog with every revert, and the chart.
Everything in the README is a number the eval produced."

## Architecture walkthrough: the questions to expect

**Why not an LLM at query time?** The query path is full-text search, vector search and an entity match fused by a weighted sum. An LLM there would add a second of latency and non-determinism to a path we can now measure exactly, and the benchmark shows the misses are a fusion problem, not a comprehension problem.

**Why PostgreSQL with Apache AGE and pgvector rather than a vector database?** One store, one backup, one trust domain, and graph edges beside vectors. The cost is that AGE cannot index Cypher `CONTAINS`, which is why keyword search is SQL full-text over the vertex table, joined back to the graph by id.

**What does the graph actually contribute?** At query time, measured, almost nothing on this benchmark, and the literature agrees: similarity-edge graphs and one-hop expansion score zero to negative. What the graph carries that a vector store cannot is provenance: `mentions` edges to entities, `supersedes` edges between contradicting facts, and the write-time consolidation that folds restatements. The honest claim is "a memory store with a graph of what it wrote", not "graph retrieval".

**How do you know a change is real?** Paired per-question comparison with McNemar on hit@10 and a bootstrap CI on MRR, two corpora that disagree in useful ways, and a 36-case golden gate on the zero-dependency embedder that fails the build. Anything under two points is noise and we say so.

**What broke?** The 69% could not be reproduced. The category codes in the benchmark harness were mislabelled, so every earlier per-category number was wrong. Pure coverage grading of the keyword score fixed a service contract and tripped the golden gate, so it was dialled back to the value both instruments accept. It is all in the worklog.

**What would you do next?** Index verbatim rounds with extracted facts merged into the same key, which LongMemEval measured at +6 points of recall and which addresses the extraction loss directly; a per-embedder fusion profile so the install default does not lose hit@10; a stored tsvector column to take 9 ms out of the FTS statement.
