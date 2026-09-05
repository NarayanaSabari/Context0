# Video script: 5 minutes, word for word

For the Razorpay AI Buildathon, track 03 (AI Revenue Recovery). The official
requirement is a 5 minute pitch video showing the repo, the working thing, and
the architecture.

This is the script to *read*. [PITCH.md](PITCH.md) is the same argument in
prose with the Q&A prep; [RECORDING.md](RECORDING.md) is the setup and the
failure modes. Use this one while the camera is rolling.

Everything in brackets is a screen action, not something to say. Times are
cumulative and assume an unhurried pace. Every number below was produced by
the commands in RECORDING.md and is reproducible on a clean clone.

---

## Before you start

Two terminals, both in the repo root, font large enough to read on a phone.

- **Terminal A**: engine up per RECORDING.md, `KORA_URL` and `KORA_API_KEY`
  exported. This is where the demo runs.
- **Terminal B**: nothing exported. This is the no-memory comparison.

Have the README open in a browser tab, scrolled to "Measured, not claimed".

Rehearse both `make demo` runs once. Check the `Memory:` line on each before
you record.

---

## 0:00 - 0:40 Where the idea came from

> "The idea for Kora came from a contradiction I kept seeing in AI agents.
> An agent can reason, learn what a user wants, and make a useful decision.
> Then the session ends, and the next one starts as if none of it happened.
>
> At first this looks like a context-window problem. It isn't. A longer prompt
> still does not know which facts matter, which fact replaced an older one, or
> when something should be forgotten.
>
> I wanted memory to be infrastructure: one engine any agent could call, with
> data the user could inspect, measure, and self-host. That became Kora."

## 0:40 - 1:10 Why a receivables agent

> "Once memory is separate from the agent, the use cases multiply. Support can
> remember resolutions, coding agents can remember design decisions, and sales
> agents can remember objections and commitments.
>
> But possible use cases prove nothing. I needed one where forgetting has a
> visible cost. In receivables, forgetting a promise or dispute means contacting
> the wrong customer again. So I built a Razorpay agent on Kora."

## 1:10 - 2:05 The proof

[Terminal B. Run `make demo`.]

> "This is fifty overdue invoices, twenty customers, and twenty-one days.
> Without memory, the agent follows its escalation ladder: gentle reminder,
> firm reminder, payment link, then human hand-off.
>
> It recovers six lakh thirty-three thousand three hundred rupees, but sends
> two hundred and sixteen messages to do it."

[Terminal A. Run `make demo`.]

> "Now the same fixture and rules, with Kora as memory. The report identifies
> the live connection so a degraded run cannot look healthy.
>
> It recovers exactly the same six lakh thirty-three thousand three hundred
> rupees, with forty-nine messages instead of two hundred and sixteen."

[Point at `Recovered`, then the contacts table.]

> "Kora did not create more payments in this fixture. It removed one hundred
> and sixty-seven unnecessary contacts, a seventy-seven percent reduction,
> by remembering promises, disputes, and same-day contact. That gives memory a
> measurable value."

## 2:05 - 2:40 The decisions behind the number

[Open `examples/receivables-chaser/audit.jsonl`.]

> "Every decision has a name and a reason."

[Find a `skip_promise_pending` row.]

> "This customer promised to pay by the eighth, so the agent waits."

[Find a `skip_dispute` row.]

> "This invoice is disputed, so the agent stops and hands it to a human. There
> is also a forty-five-day hard stop.
>
> These decisions are deterministic. An optional language model can rewrite a
> message, but it never chooses the action. The merchant can audit every step."

## 2:40 - 3:40 MemoryBench and the measurement problem

[Browser, README "Measured, not claimed".]

> "To prove the engine worked, I integrated Kora with Supermemory's open-source
> MemoryBench and ran its two-hundred-question LoCoMo evaluation. The end-to-end
> run reported sixty-nine percent.
>
> But I could not reproduce it offline. Extraction, answering, judging, and the
> retrieval verdict depended on language models over an API. MemoryBench tested
> the full system, but I needed a stable retrieval instrument.
>
> I built a deterministic companion around the same questions: five thousand
> eight hundred and eighty-two memories, one hundred and fifty-eight answerable
> questions, evidence-based scoring, and no model calls.
>
> Forty-three of forty-four misses already had the correct evidence. Fusion was
> burying it. Normalising keyword and vector scores moved hit-at-ten from 0.72
> to 0.77.
>
> The graph added only 0.005 MRR. I published that too. The benchmark mattered
> because it showed me what was actually broken."

## 3:40 - 4:25 The engine and the problems behind it

[Open `ARCHITECTURE.md` at "System Overview".]

> "Agents reach Kora through REST, gRPC, Python, or MCP. Writes create memories,
> embeddings, and entity links. Queries fuse full-text, vector, and graph
> signals, all stored in PostgreSQL with Apache AGE and pgvector.
>
> Building it exposed repeated memories, contradictions, extraction loss, and
> database bottlenecks. Ninety-five percent of query time was PostgreSQL round
> trips. Reducing them took median latency from twenty-five milliseconds to
> seventeen without changing a ranked list."

## 4:25 - 4:55 What Kora became

> "The Razorpay agent is one proof, not Kora's limit. The same memory loop can
> support customer service, coding, research, sales, or any workflow that must
> remember what happened and what changed.
>
> Kora is open source and self-hosted. The agent comparison, MemoryBench
> integration, deterministic benchmark, performance profile, and unresolved
> failures are all in the repository.
>
> I started with a question about why agents forget. I ended with an engine
> that can make remembering measurable. Thank you."

---

## Timing check

| section | spoken | budget | cumulative |
|---|---|---|---|
| Origin | 40 s | 40 s | 0:40 |
| Use cases | 26 s | 30 s | 1:10 |
| Proof | 50 s | 55 s | 2:05 |
| Decisions | 26 s | 35 s | 2:40 |
| MemoryBench | 53 s | 60 s | 3:40 |
| Architecture and problems | 27 s | 45 s | 4:25 |
| Close | 30 s | 30 s | 4:55 |

Spoken word count is 628, which is 4:11 at 150 words per minute and 4:29 at 140.
The remaining time covers the two `make demo` runs, screen changes, and pauses.

The origin, the 216-to-49 proof, and the MemoryBench lesson are the three beats
a judge should remember. Both `make demo` runs finish in about six seconds, so
let each report sit on screen while the number lands.

## If you fluff a line

Stop, pause two seconds, and say the sentence again from its start. Cutting on
a clean pause is trivial; cutting mid-sentence is not.

## What each track requirement maps to

Track 03's stated bar, and where the video satisfies it:

| the bar | where |
|---|---|
| measured money recovered across a batch | 1:10, both runs, 50 invoices |
| compliant escalation | 2:05, the 45-day human hand-off |
| stopping rules | 2:05, promise-pending and dispute skips |
| an audit trail | 2:05, `audit.jsonl` with named reasons |
| working architecture | 3:40, the shipped system overview |
| repository and reproducibility | 4:25, the benchmark and agent commands |
