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

## 0:00 - 0:40 The problem

> "A receivables agent that chases overdue invoices has one job: get the money
> in without burning the customer relationship. Most of them burn it, because
> they have no memory.
>
> It doesn't know this customer promised to pay on the seventh, that the
> invoice is disputed, or that it already emailed them this morning. So it
> sends the reminder again, and the merchant looks incompetent to their own
> customer.
>
> I built Kora, a memory engine for agents, then built a receivables agent on
> it to find out whether the memory was worth anything. I measured it, and the
> answer was not what I expected."

## 0:40 - 1:50 The demo

[Terminal B. Run `make demo`.]

> "Fifty overdue invoices, twenty customers, twenty-one days of chasing. This
> run has no memory: the agent decides from the escalation ladder alone --
> gentle, firm, payment link, human.
>
> It recovers six lakh thirty-three thousand three hundred rupees."

[Point at the `Recovered` row, then at the contacts table.]

> "It sends two hundred and sixteen messages to do it."

[Terminal A. Run `make demo`.]

> "Same fixture. Same rules. The only difference is that this agent can
> remember, and it's talking to a real Kora -- that's the `Memory:` line, which
> every report prints so you can never mistake a degraded run for a healthy one.
>
> Same six lakh thirty-three thousand three hundred recovered."

[Point at `Recovered`, then the contacts table.]

> "Forty-nine messages instead of two hundred and sixteen.
>
> That's the result. The memory engine did not recover more money. It recovered
> exactly the same money sending 77% fewer messages, because
> it stopped re-chasing people who had already promised to pay, already
> disputed, or had already been contacted that morning. If you're a merchant,
> that difference is your customer relationships."

## 1:50 - 2:40 The audit trail and the stopping rules

[Open `examples/receivables-chaser/audit.jsonl`.]

> "Every decision is a row here, with a name and a reason. Not a model's
> opinion, a rule."

[Scroll to a `skip_promise_pending` row.]

> "This one: skip, because the customer promised to pay by the eighth and that
> date hasn't arrived. Fifty-five decisions in this run were that rule."

[Scroll to a `skip_dispute` row.]

> "This one: skip and escalate, because the invoice is disputed. Ninety-seven
> decisions. Chasing a disputed invoice turns a billing error into a lost
> customer, so it hands off to a human and stops. There's a hard stop at
> forty-five days overdue too.
>
> The only thing a language model does here is reword the message text. It
> never decides. That was deliberate: the moment a model picks the action, you
> can't explain to a merchant why their customer got a fourth email."

## 2:40 - 3:45 The measurement, and what I got wrong

[Browser, README "Measured, not claimed".]

> "Now the part that matters.
>
> This engine was reported at sixty-nine percent on the LoCoMo benchmark. I
> couldn't reproduce it offline -- because the answer, the grade, and even the
> recall number had all been produced by a language model over an API. It
> wasn't a measurement. It was a model's opinion of itself.
>
> So I built one that is: the real engine, two hundred questions, scored
> against the dataset's own evidence annotations, no model calls, identical
> output on every run.
>
> Then I bucketed the failures. Of forty-four misses, exactly one was retrieval
> genuinely not finding the evidence. Forty-three were cases where the engine
> had already found the right memory and buried it, because the keyword score
> saturated and outvoted the cosine similarity. Normalising both per query
> moved hit-at-ten from 0.72 to 0.77.
>
> And the uncomfortable one. The entity graph -- the feature this project is
> named after -- measured plus 0.005 MRR. Statistically nothing. I left it in
> at its measured weight and wrote that down, because a benchmark you only
> publish when it flatters you isn't a benchmark."

## 3:45 - 4:20 Performance and production

> "Profiling showed ninety-five percent of a query was spent waiting on
> Postgres round trips, not computing. Four changes, each verified to leave
> every ranked list byte-for-byte identical, took the median query from
> twenty-five milliseconds to seventeen.
>
> It ships as a Helm chart, with every setting's reason written next to it. And
> it's Postgres with Apache AGE and pgvector, so graph edges and vectors live
> in one store, with one backup and one trust domain."

## 4:20 - 4:55 Close

> "What's still broken: extraction drops the evidence for thirty-six of two
> hundred questions before retrieval ever sees them. I know the fix, it's
> measured in the literature, and it isn't done.
>
> Everything I've claimed is in the repo: the benchmark you can rerun, the
> worklog with every hypothesis I reverted, the profile, the chart, and the
> failure analysis including the parts that didn't go my way.
>
> Two commands reproduce the number I opened with. Thank you."

---

## Timing check

| section | spoken | budget | cumulative |
|---|---|---|---|
| Problem | 41 s | 40 s | 0:40 |
| Demo | 63 s | 70 s | 1:50 |
| Audit trail | 49 s | 50 s | 2:40 |
| Measurement | 72 s | 65 s | 3:45 |
| Production | 29 s | 35 s | 4:20 |
| Close | 30 s | 35 s | 4:55 |

Spoken word count is 715, which is 4:46 at a normal 150 words per minute and
5:06 at a slower 140. The budgets above are deliberately looser than the
spoken times to leave room for the two `make demo` runs and for pauses.

The demo and the measurement are the two sections a judge remembers. If you
overrun, cut from Production, not from either of those. Both `make demo` runs
finish in about six seconds, so the demo section is nearly all narration -- do
not fill the silence, let the report sit on screen while you talk.

## If you fluff a line

Stop, pause two seconds, and say the sentence again from its start. Cutting on
a clean pause is trivial; cutting mid-sentence is not.

## What each track requirement maps to

Track 03's stated bar, and where the video satisfies it:

| the bar | where |
|---|---|
| measured money recovered across a batch | 0:40, both runs, 50 invoices |
| compliant escalation | 1:50, the 45-day human hand-off |
| stopping rules | 1:50, promise-pending and dispute skips |
| an audit trail | 1:50, `audit.jsonl` with named reasons |
