# Recording runbook

Everything needed to record the buildathon video, in the order it happens.
Written 2026-09-02 against the state of `perf/memory-optimization`; every
command here was run on this machine first, and the numbers are what they
printed.

The pitch script in [PITCH.md](PITCH.md) is what to *say*. This is what to
*type*, and what should appear when you do.

## Before you hit record

**1. Bring up the engine.**

On a clean machine the repository's own instructions work:

```bash
docker compose up -d
```

On the machine this was built on that command fails, because containers named
`kora-bench-postgres` and `kora-bench-api` are already running from an older
checkout and hold benchmark state that must survive (see
[HANDOVER.md](HANDOVER.md) section 4). Use the demo project instead, which
has its own names and its own ports and touches nothing else:

```bash
docker compose -p kora-demo -f docker-compose.yaml -f docker-compose.demo.yaml up -d
```

That serves the API on **18095**. `docker-compose.demo.yaml` is gitignored and
explains its own reasoning at the top.

**2. Export the credentials.** The key is in the gitignored `.env`:

```bash
export KORA_URL=http://localhost:18095
export KORA_API_KEY=$(grep '^KORA_API_KEYS' .env | cut -d= -f2 | tr -d '"' | cut -d, -f1)
```

**3. Rehearse both runs once.** Roughly 6 seconds each, so this is cheap.
Confirm you get the numbers in the table below before recording, not during.

**4. Check the `Memory:` line.** Every report states which memory it used, in
its fourth line. Before a take, confirm it says what you expect:

```
Memory: Kora at localhost:18095                                  <- live, good
Memory: no memory (KORA_URL / KORA_API_KEY not set)              <- the ladder-alone run
Memory: Kora at localhost:18095 -- UNREACHABLE, ran without memory   <- BAD, stop
```

The third one is the trap this line exists to close: losing the engine is
caught and logged rather than raised, so a degraded run otherwise prints a
report that looks completely healthy, and the one-line warning has scrolled
off the top by the time the report finishes.

## The demo, in two commands

The contrast is the point. Run the same command twice, once with the engine
and once without.

```bash
# Without memory: the escalation ladder alone.
env -u KORA_URL -u KORA_API_KEY make demo

# With Kora as the agent's memory.
make demo
```

What they print, verified twice from clean on 2026-09-02:

| | recovered | messages sent | promises seen |
|---|---|---|---|
| ladder alone | ₹633,300 | 430 | 9 |
| with Kora | ₹633,300 | **49** | 11 |

The line to say over it: same rules, same fixture, same money recovered, and
an order of magnitude fewer messages. The memory engine did not collect more;
it collected the same amount without re-chasing anyone who had already
promised to pay, disputed the invoice, or been contacted that day.

Both runs are reproducible: each starts from an empty memory namespace, so
take three prints exactly what take one did. That is deliberate, and it is
what makes the comparison honest rather than a function of run order.

## Optional beat: memory persisting across runs

If you would rather show accumulation than a clean comparison:

```bash
python3 -m chaser run --recorded --days 21 --merchant demo --resume   # first pass
python3 -m chaser run --recorded --days 21 --merchant demo --resume   # second pass
```

Run from `examples/receivables-chaser/`. The second pass sends almost nothing,
because the agent already remembers who was chased and who promised what.
Note this leaves state behind by design; use a fresh `--merchant` slug to
start over.

## The measurement half

Switch to the README's "Measured, not claimed" table. Nothing to run live:
`make eval` needs Docker and about 40 seconds of corpus loading, which is dead
air on camera. If a judge asks whether it really reproduces, the frozen
reports are committed at `eval/results/baseline-*.json` and `final-*.json`,
and the digests in them are what a rerun reproduces.

If you do want it running in a spare terminal:

```bash
make eval
```

## If something goes wrong on camera

- **The `Memory:` line says UNREACHABLE.** The run was not backed by memory,
  whatever the rest of the report says. Stop the take. The engine is down or
  `KORA_URL` points somewhere wrong; check it is 18095 and not 8080.
- **Report shows 0 promises and far fewer contacts than the table.** The
  engine is up but the agent is not reaching it. Check `KORA_URL` points at
  18095 and not 8080.
- **`unauthorized`.** `KORA_API_KEY` is unset or stale; re-run the export in
  step 2. The header the SDK sends is `X-API-Key`.
- **Two runs print different numbers.** Something passed `--resume`. Without
  it every run starts clean.
- **Compose refuses to start with a name conflict.** You ran plain
  `docker compose up -d` rather than the `-p kora-demo` form. Nothing was
  damaged; use the demo command above.

## After recording

1. Merge PR #100, or submit the branch URL. The README's numbers are on the
   branch, not on `main`.
2. Submit the [form](https://forms.gle/d9r2gvxp8cmoZhon9) before
   **2026-09-05**. Confirmed on 2026-09-02: that date is real, and the event
   is students only.
3. Track: "AI Revenue Recovery". Open is the fallback.
