# Build a Razorpay receivables agent

Kora includes a complete sample agent that follows up on overdue Razorpay invoices.
It recalls each customer's payment history before deciding whether to remind them, wait for a promise, offer a payment link, or hand the case to a human.

The source is in [`examples/receivables-chaser`](https://github.com/NarayanaSabari/Kora/tree/main/examples/receivables-chaser).

## What this example proves

The same deterministic policy runs with and without Kora against a committed 21-day workload.
Both configurations recover ₹633,300, but the memory-backed agent sends 49 messages instead of 216.

| Configuration | Recovered | Messages sent |
| --- | ---: | ---: |
| Escalation ladder alone | ₹633,300 | 216 |
| With Kora memory | ₹633,300 | 49 |

Kora does not choose the collection action and a language model does not control it.
The policy makes decisions from invoice state and recalled history.
An optional model can only reword the selected message and cannot change its facts or action.

## Architecture

```text
Razorpay invoice state -----+
                            |
Kora customer history ------+--> deterministic policy --> contact or handoff
                            |                                 |
date and contact limits ----+                                 +--> audit.jsonl
                                                              +--> Kora outcome memory
```

The boundary is deliberate:

| System | Owns |
| --- | --- |
| Razorpay | Invoice amount, due date, and paid status |
| Kora | Contacts, promises, disputes, and escalations |
| Policy | Whether to skip, remind, offer a link, or hand off |
| Audit log | The rule and reason used for every decision |

The agent never keeps a separate payment ledger, and Kora is not treated as the source of truth for whether money arrived.

## Run the recorded agent first

Recorded mode needs no credentials, network, model, or running Kora.
It replays 50 invoices across 20 scripted customers and writes `audit.jsonl` and `report.json`.

```bash
git clone https://github.com/NarayanaSabari/Kora.git
cd Kora/examples/receivables-chaser
env -u KORA_URL -u KORA_API_KEY python3 -m chaser run --recorded --days 21
```

This first run uses `NullMemory`, so it shows the 216-message escalation-ladder baseline.

## Run the same agent with Kora

From the repository root, generate local credentials and start Kora:

```bash
cd ../..

cat > .env <<EOF
POSTGRES_PASSWORD=$(openssl rand -hex 16)
KORA_API_KEYS=$(go run ./cmd/cli keys generate)
EOF

docker compose up -d
export KORA_API_KEY=$(grep KORA_API_KEYS .env | cut -d= -f2)
```

Run the identical workload with memory enabled:

```bash
cd examples/receivables-chaser
KORA_URL=http://localhost:8080 \
KORA_API_KEY="$KORA_API_KEY" \
  python3 -m chaser run --recorded --days 21
```

The report should identify the memory backend as Kora and show 49 messages sent.
Each recorded run gets a fresh project namespace so a replay does not inherit contacts from an earlier replay.
Pass `--resume` when you deliberately want the next recorded run to recall the previous one.

## How Kora changes a decision

Before evaluating a customer's invoices, the agent recalls payment-related history:

```python
project_id = f"receivables-{merchant}-{customer_id}"
query = f"customer {customer.name} invoice payment promise dispute contact"
memories = memory.recall(project_id, query, top_k=50)
customer_facts = parse_facts(memories)
```

The project is scoped to one merchant and one customer.
This prevents one customer's notes from crowding another customer's promise or dispute out of a ranked result set.

The policy then applies its rules in order:

1. Skip paid invoices.
2. Hand unresolved disputes to a human and stop contacting the customer.
3. Stop after a human escalation.
4. Wait for a future promise-to-pay date.
5. Increase the reminder level after a broken promise.
6. Choose a reminder level from the number of days overdue.
7. Escalate after three contacts with no response.
8. Enforce invoice and customer contact limits.

After an action succeeds, the agent writes an explicit event back to Kora:

```text
On 2026-09-05, sent a firm reminder to Acme for invoice inv_42 (₹18,500, 12 days overdue).
On 2026-09-05, Acme promised to pay invoice inv_42 by 2026-09-10.
```

Those statements contain the date, customer, invoice, action, and amount needed by a later run.
The next decision is therefore based on durable memory rather than process-local state.

## Inspect the evidence

`audit.jsonl` contains one record per invoice decision, including the policy rung, reason, overdue days, and whether a contact was sent.
`report.json` is the structured source of truth for totals.

```bash
python3 -c "import json; d=json.load(open('report.json')); print(sum(d['contacts_by_rung'].values()), d['amount_recovered'])"
```

The recorded workload is generated from a fixed seed and uses dates from the fixture rather than the wall clock.
The test suite pins the fixture digest and checks that repeated runs produce byte-identical audit logs.

```bash
python3 -m pytest
```

## Run against Razorpay test mode

Copy the example environment template and fill in test-mode credentials locally:

```bash
cd examples/receivables-chaser
cp .env.example .env
```

Do not commit this file.
It contains the following integration settings:

```dotenv
KORA_URL=http://localhost:8080
KORA_API_KEY=
RAZORPAY_KEY_ID=
RAZORPAY_KEY_SECRET=
```

Load the values and run one daily tick:

```bash
set -a && source .env && set +a
python3 -m chaser run --live --merchant your-merchant-id
```

Live mode lists `issued` and `partially_paid` invoices, fetches their customers, and uses Razorpay's invoice email notification endpoint when the policy selects a reminder.
It preserves the merchant project names across daily runs so yesterday's contacts remain visible today.

The live adapter has an important limit: its invoice reads are exercised, but there is no Razorpay test account in this project's automated environment to verify the notification write end to end.
Razorpay's notification endpoint also uses Razorpay's template rather than the agent's drafted custom message.
A real customer reply arrives asynchronously, so production use still needs a webhook or support-system adapter that writes promises and disputes into Kora.

## Turn the sample into a production agent

Keep the example's core boundaries, then replace its demo edges:

- Schedule `python3 -m chaser run --live` once per day with one stable `--merchant` value.
- Ingest customer replies from a verified webhook or support workflow.
- Replace `SafeMemory`'s continue-without-memory policy with a human handoff or closed failure when duplicate contact is unacceptable.
- Confirm notification behavior in your own Razorpay test-mode account before contacting a real customer.
- Store Kora and Razorpay credentials in your secret manager, not in the repository or image.
- Route disputes and exhausted contact attempts into a real human queue.
- Monitor the audit log, Kora request failures, and the difference between selected actions and successful notifications.

The complete implementation and its policy tests are documented in the [example README](https://github.com/NarayanaSabari/Kora/tree/main/examples/receivables-chaser).
For the reusable memory pattern, return to [Integrate Kora with an agent](agent-integration.md).
