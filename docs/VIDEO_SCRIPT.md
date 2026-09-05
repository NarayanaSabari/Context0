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

## 0:00 - 0:35 Where the idea came from

> "I got the idea for Kora from a simple problem. An AI agent can learn during
> one chat, then forget everything when the next chat starts.
>
> This is serious when an agent asks customers to pay. It may message someone
> who already promised to pay or question a bill that is already under dispute.
>
> I wanted one memory system that many agents could use. That became Kora."

## 0:35 - 1:15 What the Razorpay agent does

> "I built a Razorpay agent to test this idea. It follows up on unpaid invoices.
>
> Each day, it reads invoices from Razorpay. Razorpay is trusted for the amount,
> due date, and payment status.
>
> Simple rules tell the agent to wait, send a reminder, share a payment link,
> or ask a person for help. Kora gives it the customer's past history.
>
> The demo uses fixed data, and the project also supports Razorpay test mode."

## 1:15 - 2:05 The result

[Terminal B. Run `make demo`.]

> "This demo has fifty unpaid invoices, twenty customers, and twenty-one days.
> First, I run it without memory.
>
> It recovers six lakh thirty-three thousand three hundred rupees, but sends
> two hundred and sixteen messages to do it."

[Terminal A. Run `make demo`.]

> "Now I run the same test with Kora. Nothing else changes.
>
> It recovers exactly the same six lakh thirty-three thousand three hundred
> rupees, with forty-nine messages instead of two hundred and sixteen."

[Point at `Recovered`, then the contacts table.]

> "That is one hundred and sixty-seven fewer messages, a seventy-seven percent
> drop. The agent gets the same money without messages that are not needed."

## 2:05 - 2:55 How Kora helps the agent

[Open `site/public/docs/razorpay-agent.md` at "How Kora changes a decision".]

> "Before sending anything, the agent asks Kora: what happened with this
> customer before?
>
> Kora remembers past messages, no replies, promises, disputes, payments, and
> cases already given to a person.
>
> A customer promises to pay on Friday. On Thursday, Razorpay still shows an
> unpaid invoice. But Kora remembers the promise, so the agent waits. Without
> Kora, it sends another message.
>
> I also found a bug. I stored all customers together, so the right promise
> could be hidden by another customer's history. Giving each customer their own
> memory space fixed it."

## 2:55 - 3:25 Safe choices and clear reasons

[Open `examples/receivables-chaser/audit.jsonl`.]

> "Every choice has a name and a clear reason."

[Find a `skip_promise_pending` row.]

> "This customer promised to pay by the eighth, so the agent waits."

[Find a `skip_dispute` row.]

> "This customer says the invoice is wrong, so the agent stops and asks a
> person for help.
>
> Fixed rules make these choices. An AI model may write the message, but it
> cannot choose the action. Every step can be checked later."

## 3:25 - 4:15 Testing Kora with MemoryBench

[Browser, README "Measured, not claimed".]

> "The demo showed how memory helped the agent. I also needed to test how well
> Kora finds old memories.
>
> I connected Kora to Supermemory's open-source MemoryBench. Its LoCoMo test
> asks two hundred questions about old chats. Kora scored sixty-nine percent.
>
> But AI models created the memories, answered the questions, and checked the
> answers. This made the number hard to repeat and the problem hard to find.
>
> I built a second test with fixed answers and no AI calls. In forty-three out
> of forty-four misses, Kora found the right memory but placed it too low.
>
> I fixed how Kora mixes text and vector search. Hit at ten went from 0.72 to
> 0.77. The test showed me what to fix."

## 4:15 - 4:43 Built for Kubernetes

[Open `ARCHITECTURE.md` at "System Overview".]

> "The Razorpay agent uses Python to talk to Kora. Other agents can use REST,
> gRPC, or MCP.
>
> Kora is an enterprise-grade, Kubernetes-native memory engine, not an embedded
> library inside one agent. I built it for Kubernetes from the start. It has a
> Helm chart, access control, health checks, metrics, network rules, backup, and
> restore.
>
> The same flow can help support, coding, research, sales, and many other
> agents: remember, choose, act, and save what happened."

## 4:43 - 5:00 Close

> "Kora is open source and runs in your Kubernetes cluster. The result is
> simple: the same money with one hundred and sixty-seven fewer messages.
>
> I started with an agent that forgot. I ended with one that knows when to act,
> wait, or ask a person for help. Thank you."

---

## Timing check

| section | spoken | budget | cumulative |
|---|---|---|---|
| Origin | 27 s | 35 s | 0:35 |
| Razorpay agent | 29 s | 40 s | 1:15 |
| Measured result | 37 s | 50 s | 2:05 |
| How Kora helps | 36 s | 50 s | 2:55 |
| Safe choices | 25 s | 30 s | 3:25 |
| MemoryBench | 48 s | 50 s | 4:15 |
| Kubernetes and more agents | 30 s | 28 s | 4:43 |
| Close | 17 s | 17 s | 5:00 |

Spoken word count is about 620, which is 4:36 at 135 words per minute.
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
| reusable architecture | 4:15, Python, REST, gRPC, MCP, and Kubernetes |
