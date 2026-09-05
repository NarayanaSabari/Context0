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

## 0:00 - 0:35 Where the idea became real

> "I started Kora because agents can reason through one conversation, then
> begin the next one as if nothing happened.
>
> That became concrete in revenue recovery. A forgetful receivables agent can
> chase someone who promised to pay, contact a disputed invoice, or send two
> reminders on the same day. Kora became the memory this agent was missing."

## 0:35 - 1:15 What the Razorpay agent does

> "Each day, the agent reads open and partially paid invoices from Razorpay,
> which remains the source of truth for amounts, due dates, and payments.
> The demo replays that behavior from a fixed fixture so the comparison is
> repeatable, and the agent also includes a Razorpay test-mode adapter.
>
> A deterministic policy chooses whether to wait, remind, offer a payment link,
> or hand the case to a human. Customer history comes from Kora.
>
> Kora never invents payment state, and a language model never decides who gets
> chased."

## 1:15 - 2:05 The measured result

[Terminal B. Run `make demo`.]

> "This is fifty overdue invoices, twenty customers, and twenty-one days.
> Without memory, the agent follows its escalation ladder: gentle reminder,
> firm reminder, payment link, then human hand-off.
>
> It recovers six lakh thirty-three thousand three hundred rupees, but sends
> two hundred and sixteen messages to do it."

[Terminal A. Run `make demo`.]

> "Now the same fixture and rules, with Kora as memory.
>
> It recovers exactly the same six lakh thirty-three thousand three hundred
> rupees, with forty-nine messages instead of two hundred and sixteen."

[Point at `Recovered`, then the contacts table.]

> "Kora removed one hundred and sixty-seven unnecessary contacts, a
> seventy-seven percent reduction, by remembering promises, disputes, and
> same-day contact. That gives memory a measurable value."

## 2:05 - 2:55 How Kora changes the agent

[Open `site/public/docs/razorpay-agent.md` at "How Kora changes a decision".]

> "Before deciding, the agent asks Kora for that customer's contacts, promises,
> disputes, payments, and escalations.
>
> If a customer promises to pay on Friday, the agent writes that event to Kora.
> Tomorrow, Razorpay still shows an unpaid invoice, but Kora remembers the
> promise, so the agent waits. Without Kora, tomorrow looks like day one again.
>
> My first version stored all customers together. Forty-four of the top fifty
> results could be about somebody else, hiding the right promise. Giving each
> customer a separate memory scope fixed that."

## 2:55 - 3:25 Safety and auditability

[Open `examples/receivables-chaser/audit.jsonl`.]

> "Every decision has a name and a reason."

[Find a `skip_promise_pending` row.]

> "This customer promised to pay by the eighth, so the agent waits."

[Find a `skip_dispute` row.]

> "This invoice is disputed, so the agent stops and hands it to a human.
>
> These decisions are deterministic. An optional model can rewrite a message,
> but the merchant can audit every action and reason."

## 3:25 - 4:10 MemoryBench and the problems it revealed

[Browser, README "Measured, not claimed".]

> "The agent measured the business effect. To test retrieval, I integrated Kora
> with Supermemory's MemoryBench and its two-hundred-question LoCoMo evaluation.
> It reported sixty-nine percent.
>
> But extraction, answering, judging, and the retrieval verdict depended on
> language models over an API. It tested the system, but could not reliably
> diagnose it.
>
> I built a deterministic companion using LoCoMo's evidence annotations and no
> model calls.
>
> Forty-three of forty-four misses already contained the right evidence. Kora's
> fusion layer buried it. Normalising the scores moved hit-at-ten from 0.72 to
> 0.77.
>
> The graph added only 0.005 MRR. The useful benchmark showed me what to fix."

## 4:10 - 4:40 Kubernetes-native, reusable memory

[Open `ARCHITECTURE.md` at "System Overview".]

> "This agent uses Kora through Python, while other agents can use REST, gRPC,
> or MCP.
>
> Kora is an enterprise-grade, Kubernetes-native memory engine, not an embedded
> library. I built it for Kubernetes from the start, with Helm deployment,
> authentication, health probes, Prometheus metrics, network policies, and
> tested backup and restore.
>
> The loop stays the same: recall history, decide, act, and remember the outcome.
> It can support customer service, coding, research, sales, or any long-running
> agent."

## 4:40 - 5:00 Close

> "Kora is open source and self-hosted, but the result I care about is the agent:
> the same money recovered with one hundred and sixty-seven fewer customer
> contacts.
>
> I started with an agent that forgot. I ended with one that knows when not to
> act. Thank you."

---

## Timing check

| section | spoken | budget | cumulative |
|---|---|---|---|
| Origin | 25 s | 35 s | 0:35 |
| Razorpay agent | 37 s | 40 s | 1:15 |
| Measured result | 45 s | 50 s | 2:05 |
| How Kora helps | 38 s | 50 s | 2:55 |
| Safety and audit | 24 s | 30 s | 3:25 |
| MemoryBench | 45 s | 45 s | 4:10 |
| Kubernetes and more agents | 30 s | 30 s | 4:40 |
| Close | 20 s | 20 s | 5:00 |

Spoken word count is about 605, which is 4:29 at 135 words per minute.
The remaining time covers the two `make demo` runs, screen changes, and pauses.

The Razorpay workflow, the 216-to-49 proof, and the promise-to-pay example are
the three beats a judge should remember. MemoryBench supports that story by
showing how the engine was measured and improved. Both `make demo` runs finish
in about six seconds, so let each report sit on screen while the number lands.

## If you fluff a line

Stop, pause two seconds, and say the sentence again from its start. Cutting on
a clean pause is trivial; cutting mid-sentence is not.

## What each track requirement maps to

Track 03's stated bar, and where the video satisfies it:

| the bar | where |
|---|---|
| Razorpay integration | 0:35, invoice source of truth and the test-mode adapter |
| measured money recovered across a batch | 1:15, both runs, 50 invoices |
| Kora's effect on the agent | 2:05, the promise-to-pay memory loop |
| compliant escalation and stopping rules | 2:55, promise and dispute skips |
| an audit trail | 2:55, `audit.jsonl` with named reasons |
| reproducible benchmark | 3:25, MemoryBench and the deterministic companion |
| reusable architecture | 4:10, Python, REST, gRPC, and MCP |
