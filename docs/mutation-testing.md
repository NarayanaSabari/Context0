# Mutation testing

`scripts/mutate.py` checks whether the test suite fails when the code is wrong.

## Why

Coverage says a line executed. It says nothing about whether anything was
asserted. In this repository that gap was not hypothetical: ten checks passed
while measuring nothing, and each was found by hand-breaking the thing it
claimed to guard, never by watching it go green.

Some of what was passing:

- an awk comparison of `"55.2ms"` against `150` that compared them
  lexically, and reported a pass as a failure
- absent pool metrics defaulting to `0`, producing a "healthy pool" that had
  not been measured
- a documentation check that matched the comment explaining the removal
- `verify_perf.sh` asserting query latency in CI against a database holding
  zero vertices, while printing "6 passed"
- a scoped-search regression test that passed with the fix reverted, because
  it seeded 2,400 rows into a 147k-row table whose statistics were fresh

Every one of those was green. Mutation testing automates the check that caught
them: change the source in a way a correct test must notice, run the tests, and
report anything the suite sleeps through.

## Usage

```sh
scripts/mutate.py                    # the default package set
scripts/mutate.py internal/service   # one package
scripts/mutate.py --list             # the mutation operators
```

Packages with database-backed tests need `KORA_TEST_DATABASE_URL`. Without
it those tests skip, and a skipped test kills no mutants -- `internal/service`
currently reports 12 of 24 killed with no database and 24 of 24 with one. The script
skips any package whose tests are already failing, since a red suite says
nothing about mutations.

## Reading the output

A surviving mutation is a statement about the tests, not the code. It means
that behaviour is unprotected: the source was changed and nothing objected.

Not every survivor is a gap. An **equivalent mutant** changes the text without
changing behaviour, and no test can kill it:

- `if v < 0` widened to `if v <= 0` where both branches return the same value
  at `v == 0`
- a guard that clamps to the same value a second guard downstream also clamps
- `if oldestKey != ""` at a call site that cannot be reached with an empty map

Decide which you have by reading the code, not by adding a test that pins the
mutant's behaviour. The useful response to a real survivor is a test that
fails when the guard is removed and passes when it is restored -- verify both
directions before believing it.

## What it found

Working through the survivors surfaced these defects, each fixed with a test
confirmed to fail against the unfixed code:

| Defect | Consequence |
|---|---|
| Ending a session twice succeeded | `kora_active_sessions` reached -2 on the live API; anything alerting on it was reading an impossible number |
| An elaborated fact was reported as a contradiction | "The cache uses Redis for sessions" superseded "The cache uses Redis" at 0.85 confidence, retiring a fact it only expanded on |
| Half of genuine negation contradictions were missed | The negation's own words counted against Jaccard similarity, so the clearest contradictions scored lowest and both facts stayed live |
| The Google API key was logged | It travels as a query parameter, and Go's transport errors embed the URL, which `StoreMemory` logs at Error on every embedding failure |
| Embedding requests had no timeout | `http.DefaultClient` has none; a stalled provider pinned a database connection and drained the pool |
| `frequencyFactor` produced NaN | A negative access count reached `math.Log1p`; every comparison against NaN is false, so one NaN scrambles the result ordering |

The harness needed two fixes of its own along the way, both of which had made
it under-report:

- its patterns only matched conditions containing `==`, `!=`, `<` or `>`, so
  guards like `if !p.started.Load()` and `if err := f(); err != nil` were never
  mutated. `internal/server` is built almost entirely from those forms and
  reported one mutant across the package.
- it mutated inside comments and string literals. A comment reading
  `acquired == max` was mutated to `!=`, survived (it is prose), and was
  reported as unprotected behaviour.

A blind spot in the harness reads exactly like an absence of gaps.

## Where it fits

Mutation testing is a periodic audit, not a CI gate: a full run rebuilds and
re-tests once per mutation, which is far too slow for a pull request. Run it
when adding a subsystem, when a test looks suspiciously easy to satisfy, or
when a bug reaches production through code that was already covered.
