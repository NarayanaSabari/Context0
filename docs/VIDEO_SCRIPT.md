# Kora pitch video script

Target runtime: 4 minutes 55 seconds.

Read the quoted text as narration.
Text in square brackets is a screen direction.

## 0:00-0:20 - Result first

1. **Single point:** “An agent followed up on 50 overdue invoices for 21 days.
Without memory, it sent 216 messages.
With Kora, it sent 49.
Both recovered ₹633,300.”
2. **Evidence on screen:** [Show two large result cards: `WITHOUT MEMORY: 216 messages | ₹633,300 recovered` and `WITH KORA: 49 messages | ₹633,300 recovered`.
Animate `77% fewer messages` between them.]
3. **Why it matters:** “Kora did not change the recovery result.
It removed 77 percent of the unnecessary contact.”
4. **Transition:** “To see how, look at what the agent forgets.”

## 0:20-0:50 - The problem

1. **Single point:** “Most agents treat every interaction as a fresh start.
They forget that a customer promised to pay, raised a dispute, or was already contacted.”
2. **Evidence on screen:** [Show one broken timeline: `Day 1: reminder sent` -> `Customer: I will pay by 8 September` -> `Day 2: reminder sent again`.
Repeat the reminder until the timeline becomes visibly noisy.]
3. **Why it matters:** “That missing context becomes duplicate work, avoidable cost, and damaged trust.
The agent is active, but it is not behaving responsibly.”
4. **Transition:** “Kora gives that agent durable memory between interactions.”

## 0:50-1:10 - Introduce Kora

1. **Single point:** “Kora is an open-source memory layer that lets agents store experience, retrieve the right context, and use it in the next decision.”
2. **Evidence on screen:** [Reveal `KORA` above one line: `Store -> Retrieve -> Decide`, then show `Agent -> Kora -> Postgres`.]
3. **Why it matters:** “The application keeps its business rules.
Kora supplies the missing history, with provenance, through one API.”
4. **Transition:** “Here is one complete interaction, live.”

## 1:10-2:40 - Live demo

1. **Single point:** “A remembered promise changes the next action.”
2. **Evidence on screen:** [Show the real Kora health check, then run the receivables chaser against the live API.
Follow Asha Traders through one complete workflow.
First show the 2 September contact for invoice `inv_001` and the stored promise to pay by 8 September.
Query Kora for Asha Traders and show the returned memory: `Asha Traders promised to pay invoice inv_001 by 2026-09-08`.
Then move to the next day and freeze on the resulting audit row: `skip_promise_pending` and `promised to pay by 2026-09-08, not due yet`.
Finish on the live report: `49 messages`, `11 promises`, and `₹633,300 recovered`.]
3. **Why it matters:** “The agent stores what happened after the first interaction.
In the next interaction, Kora retrieves that promise as customer context.
The policy sees that the date has not arrived and chooses not to contact the customer.
The model can reword an approved message, but it cannot invent the action.
One memory has changed a real decision, and the reason is visible.”
4. **Transition:** “Now compare that behavior with the same world and no memory.”

## 2:40-3:10 - Evidence

1. **Single point:** “The difference comes from memory, not from changing the invoices, customers, payment behavior, or policy.”
2. **Evidence on screen:** [Place the two terminal reports side by side.
Highlight `216 -> 49 messages`, `₹633,300 -> ₹633,300 recovered`, and the passing headline benchmark test.
Label the input `fixed 50-invoice fixture, 21 simulated days`.]
3. **Why it matters:** “The fixture is deterministic and the expected output is regression-tested.
That makes the result reproducible, not a hand-picked demo run.”
4. **Transition:** “The result is simple, but the system behind it is deliberate.”

## 3:10-4:05 - Architecture

1. **Single point:** “Kora keeps memory retrieval operationally simple without reducing memory to a vector lookup.”
2. **Evidence on screen:** [Build the diagram one step at a time: `Agent -> gRPC or REST -> Go service -> Postgres`.
Inside Postgres, reveal `Apache AGE: entities and relationships`, `pgvector: semantic similarity`, and `relational tables: metadata and audit history`.
End by placing the stack inside `Kubernetes via Helm`.]
3. **Why it matters:** “Go gives Kora a small, concurrent service that is straightforward to operate.
gRPC provides a typed contract, while the REST gateway keeps integration accessible.
Postgres keeps metadata, vectors, and graph relationships in one transactional system and one backup boundary.
AGE represents who and what a memory is connected to.
pgvector finds semantically related memories.
Helm makes the same deployment repeatable across Kubernetes environments.”
4. **Transition:** “That design matters most when a decision must be explained.”

## 4:05-4:40 - Trust and auditability

1. **Single point:** “Trust comes from tracing remembered evidence all the way to the action.”
2. **Evidence on screen:** [Arrange four linked panes: the Kora query result containing the promise, policy rule `skip_promise_pending`, the message stage marked `not invoked: no contact approved`, and the `audit.jsonl` row containing `contacted: false` with its reason.]
3. **Why it matters:** “Kora returns the memory as inspectable data.
The application applies a named rule and records the decision.
The language model runs only after a contact action is approved.
In sensitive workflows, this audit trail is the product difference: memory is useful only when its influence can be examined.”
4. **Transition:** “That is the standard Kora is built around.”

## 4:40-4:55 - Close

1. **Single point:** “Agents should not merely remember more.
They should make better decisions that people can verify.”
2. **Evidence on screen:** [Show the Kora repository, the README quick start, and `make demo`.
Hold on `Open source | Go | Postgres | Kubernetes`.]
3. **Why it matters:** “Kora turns memory into fewer repeated actions, consistent policy, and an audit trail.”
4. **Transition:** “Give your agent memory you can trust.”
