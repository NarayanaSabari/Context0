# Receivables Chaser

A deterministic collections agent that chases overdue invoices and uses
[Kora](../../README.md) as its long-term memory of every customer it has
ever contacted -- what was promised, what was disputed, and who has already
been reminded today.

Run it offline against a scripted 21-day simulation in under a second, or
point it at a real Kora and a real (test-mode) Razorpay account.

## What it does

Each daily tick:

1. Fetches every invoice from Razorpay (recorded or live).
2. For each open invoice, queries Kora for what it remembers about that
   customer.
3. Runs a fixed decision table (below) to decide: skip, remind, or escalate
   to a human.
4. Drafts the message (a template, optionally reworded by an LLM for tone
   only) and sends it.
5. Writes what happened -- and the customer's reply, if any -- back to Kora
   as a new memory, and to `audit.jsonl`.

At the end of the run, `report.py` prints a summary and writes `report.json`:
invoices processed, contacts sent by rung, money outstanding and recovered
(broken down by which reminder got the payment), promises made/kept/broken,
disputes escalated, and an honest exception list of every invoice that was
**not** recovered, with the reason and the last action taken.

## The rules

| # | Rule | Outcome |
|---|---|---|
| 1 | Invoice already paid | skip |
| 2 | Unresolved dispute on file | skip, exception: "dispute open, escalated to human" |
| 3 | Already escalated to a human | skip (stop-after-escalation) |
| 4 | Promise-to-pay with a future date | skip, wait for it |
| 5 | Promise's date has passed, still unpaid | mark broken, bump one rung |
| 6 | Days overdue: 1-7 / 8-21 / 22-45 / >45 | gentle reminder / firm reminder / payment-link offer / escalate to human |
| 6b | 3 contacts with no response at all | escalate to human, regardless of days overdue |
| 7 | Already contacted this invoice 3 times in the last 14 days | skip, rate-limited |
| 8 | Customer already contacted (any invoice) today | skip, one contact per customer per day |

Every decision returns the rule that fired and a human-readable reason
(`chaser/policy.py`); that pair is what ends up in `audit.jsonl` and in the
exception list, so nothing in the report needs to be taken on faith.

## Why deterministic here, why an LLM only for wording

Whether to contact a customer, which rung to use, and whether a promise was
kept are all facts you can check: a due date, a memory of what was said, a
count of contacts. Routing that through a model buys nothing but variance
and cost, and it makes the decision harder to audit, not easier -- "the
model thought it was a good time to escalate" is not an answer a collections
team, or a judge, can act on.

Wording is different: two reminders that say the same thing in different
words are still the same decision. `LLMDrafter` (optional, off unless
`OPENAI_BASE_URL`/`OPENAI_API_KEY` are set) rewords the deterministic
template for tone, never the facts, and falls back to the template verbatim
on any error. The test suite never calls it -- it doesn't need to, because
it cannot change what the agent decided.

## Run it offline

```bash
cd examples/receivables-chaser
python3 make_world.py                       # regenerate fixtures/world.json (already committed)
python3 -m chaser run --recorded --days 21  # prints the report, writes audit.jsonl + report.json
```

No network, no credentials, no Kora required -- `NullMemory` steps in and
the chaser runs on the escalation ladder alone (see below for what that
costs). Runs in well under a second.

## Run it against a real Kora

```bash
docker compose up -d          # from the repository root
# find a key: grep KORA_API_KEYS .env  (or generate one -- see docker-compose.yaml)

cd examples/receivables-chaser
KORA_URL=http://localhost:8080 KORA_API_KEY=<your key> \
  python3 -m chaser run --recorded --days 21
```

With Kora in the loop, the second and later ticks change behaviour visibly:
a customer who promised to pay is not re-contacted before their date, a
disputed invoice is never chased again, and a customer contacted earlier
today is not contacted twice. Without Kora (`NullMemory`), none of that
state exists, so the ladder alone decides every day from scratch --
functional, but a customer who has already promised gets reminded daily
until the promise is due, since there is nowhere to remember that they
already answered.

That difference is the whole point of the example, and it is measurable on
the committed fixture: both configurations recover the same ₹633,300, but
the ladder alone sends 430 messages to do it and the memory-backed run sends
49. Same money, an order of magnitude less nagging.

Two details matter if you compare runs yourself:

- **Each run starts from an empty store.** Kora persists, so without this a
  second run would read the first run's contacts and skip customers it had
  not actually contacted yet -- two identical commands would print different
  numbers. Pass `--resume` to deliberately carry history across runs; a
  second `--resume` run sends far fewer messages, because it already knows
  who was chased and who promised what.
- **Each customer gets their own project** (`receivables-{merchant}-{id}`).
  Sharing one project across customers means a `top_k` recall for one
  customer comes back mostly about others, and their own promises fall off
  the end of the ranked list.

## Run it live (Razorpay test mode)

```bash
cp .env.example .env   # fill in KORA_URL, KORA_API_KEY, RAZORPAY_KEY_ID, RAZORPAY_KEY_SECRET
set -a && source .env && set +a
python3 -m chaser run --live
```

`LiveRazorpay` lists open/partially-paid invoices and sends reminders
through Razorpay's notify endpoint. A live run is a single tick: a real
customer's reply is asynchronous (a webhook or a support call, not this
call's return value), so promise/dispute parsing from a synchronous reply
only happens in `--recorded` mode. See `chaser/razorpay.py` for exactly
which endpoints are used, and note that only the read paths have been
exercised against real data -- there is no test-mode account reachable from
this environment to verify `contact` against.

## Tests

```bash
cd examples/receivables-chaser
pytest
```

Fully offline: `RecordedRazorpay` plus an in-memory Kora fake. Covers every
rule in the table above, promise-date parsing, the report's arithmetic
(recovered amount is exactly the sum of invoices that flipped from open to
paid), and determinism (two runs of the same world produce byte-identical
audit logs).

## Files

| File | What |
|---|---|
| `chaser/policy.py` | the decision table |
| `chaser/facts.py` | turns Kora's text back into structured facts the policy reads |
| `chaser/notes.py` | the sentences written to Kora, and the regexes that read them back |
| `chaser/replies.py` | classifies a customer reply (promise date, dispute) without an LLM |
| `chaser/razorpay.py` | `RecordedRazorpay` (scripted) and `LiveRazorpay` (real test-mode API) |
| `chaser/memory.py` | the Kora client, plus `NullMemory`/`SafeMemory` fallbacks |
| `chaser/drafter.py` | `TemplateDrafter` (deterministic) and `LLMDrafter` (wording only) |
| `chaser/agent.py` | the daily-tick loop that ties it together |
| `chaser/report.py` | the markdown report and `report.json` |
| `make_world.py` | generates `fixtures/world.json` |
| `fixtures/world.json` | 50 invoices, 20 customers, 6 scripted behaviours, seeded |
