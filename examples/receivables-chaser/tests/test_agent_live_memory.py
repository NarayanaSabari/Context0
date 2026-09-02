"""The agent must keep working when memory is a real engine: one that ranks
by relevance and truncates at top_k, rather than handing back everything
ever written to a project.

These tests exist because a run against a real Kora silently lost almost
everything the agent had learned. The agent wrote all 20 customers' notes
into a single project and then asked for the 50 best matches for one
customer; the engine answered honestly, and 44 of those 50 belonged to other
customers. Promises and disputes fell off the end of the list, so the policy
never saw them: recovery dropped from Rs 633,300 to Rs 221,200, and promises
recorded went from 9 to 0.

The existing suite could not see any of this, because its FakeMemory ignores
the query and returns the whole project. Both tests below fail against the
shared-project design and pass once each customer's memories are scoped to
their own project.
"""

from __future__ import annotations

from pathlib import Path

from chaser import agent
from chaser.drafter import TemplateDrafter
from chaser.razorpay import RecordedRazorpay

WORLD = Path(__file__).resolve().parent.parent / "fixtures" / "world.json"


def test_a_ranking_engine_produces_the_same_run_as_a_perfect_one(
    ranking_fake_memory, fake_memory,
) -> None:
    """Ranking and truncation must not change the agent's decisions.

    If every customer's memories are scoped to that customer, a 21-day run
    writes far fewer than top_k memories per project, so the engine's cut
    never bites and a ranking engine answers identically to one that returns
    everything. If instead all customers share a project, the cut removes
    real history and the two runs diverge -- which is exactly what happened
    live: 216 contacts became 7, and Rs 633,300 recovered became Rs 221,200.

    Comparing the two fakes states that invariant directly, without pinning
    any specific number that a fixture change would invalidate.
    """
    ranked = agent.run(
        RecordedRazorpay(WORLD), ranking_fake_memory, TemplateDrafter(), days=21, merchant="test",
    )
    perfect = agent.run(
        RecordedRazorpay(WORLD), fake_memory, TemplateDrafter(), days=21, merchant="test",
    )

    assert [tuple(e.__dict__.items()) for e in ranked.audit] == [
        tuple(e.__dict__.items()) for e in perfect.audit
    ], "a ranking, truncating engine changed the agent's decisions"

    assert ranked.promises_made == perfect.promises_made
    assert ranked.disputes == perfect.disputes


def test_a_customers_recall_is_not_crowded_out_by_other_customers(ranking_fake_memory) -> None:
    """Whatever project a customer's notes are written to, a recall scoped to
    that customer must come back about that customer.

    This is the direct measurement of the bug: with one shared project, 44 of
    50 returned memories were about someone else.
    """
    agent.run(
        RecordedRazorpay(WORLD), ranking_fake_memory, TemplateDrafter(), days=21, merchant="test",
    )

    store = ranking_fake_memory.store
    assert store, "the agent wrote nothing to memory"

    # Find a customer that the run actually contacted, then check that asking
    # about them returns their own notes rather than the corpus at large.
    world_customers = [c["name"] for c in __import__("json").loads(WORLD.read_text())["customers"]]
    checked = 0
    for name in world_customers:
        query = f"customer {name} invoice payment promise dispute contact"
        for project_id in store:
            hits = ranking_fake_memory.recall(project_id, query, top_k=50)
            own = [h for h in hits if name in h]
            if not own:
                continue
            checked += 1
            assert len(own) == len(hits), (
                f"recall for {name} in project {project_id} returned "
                f"{len(hits) - len(own)} memories about other customers; "
                f"a customer's own history is being crowded out"
            )
    assert checked, "no customer's memories were found to check"


def test_a_rerun_against_a_persistent_store_starts_clean(
    tmp_path, monkeypatch, capsys, new_ranking_fake_memory,
) -> None:
    """Two runs of the same command must print the same report.

    Kora persists across runs, so the CLI namespaces each run unless --resume
    is given. Without that, the second run reads the first run's contacts and
    skips customers it has not actually contacted yet: two identical commands
    produced Rs 221,200 and then Rs 259,900. The fake here is shared between
    both runs precisely to model a store that survives, which is what makes
    this a regression test rather than a tautology.
    """
    from chaser import cli

    shared = new_ranking_fake_memory()
    monkeypatch.setattr(cli, "_build_memory", lambda args: shared)

    argv = [
        "run", "--recorded", "--days", "21",
        "--world", str(WORLD), "--out-dir", str(tmp_path),
    ]

    assert cli.main(argv) == 0
    first = capsys.readouterr().out
    assert cli.main(argv) == 0
    second = capsys.readouterr().out

    assert first == second, (
        "a second run of the same command produced a different report; "
        "run memory is leaking across runs"
    )


def test_resume_carries_memory_across_runs(
    tmp_path, monkeypatch, capsys, new_ranking_fake_memory,
) -> None:
    """--resume is the opposite promise: history is deliberately carried over.

    A second --resume run over the same world should send strictly fewer
    contacts than the first, because it already remembers who was chased and
    who promised what. This is the behaviour worth demonstrating on purpose,
    so it is worth a test.
    """
    from chaser import cli

    shared = new_ranking_fake_memory()
    monkeypatch.setattr(cli, "_build_memory", lambda args: shared)

    argv = [
        "run", "--recorded", "--days", "21", "--resume",
        "--world", str(WORLD), "--out-dir", str(tmp_path),
    ]

    def contacts_sent() -> int:
        audit = (tmp_path / "audit.jsonl").read_text().splitlines()
        return sum(1 for line in audit if '"contacted": true' in line)

    assert cli.main(argv) == 0
    capsys.readouterr()
    first = contacts_sent()

    assert cli.main(argv) == 0
    capsys.readouterr()
    second = contacts_sent()

    assert first > 0, "the first run sent no contacts at all"
    assert second < first, (
        f"a --resume rerun sent {second} contacts against the first run's "
        f"{first}; it is not reading back the previous run's history"
    )


def test_live_runs_keep_their_history(monkeypatch) -> None:
    """A --live run is one day's work, so it must NOT start from a clean store.

    Recorded runs replay a whole history from day zero and start clean, which
    is what makes them reproducible. Live runs are the opposite: the CLI
    documents them as "the way a cron job firing daily against the real API
    would" work, and a cron that forgets yesterday re-chases every customer
    who already promised to pay. Namespacing them per run would do exactly
    that, silently, against real invoices and real people.

    Asserting on the merchant slug the CLI hands the agent is the cheapest
    way to state that, since it needs no Razorpay credentials.
    """
    from chaser import cli

    seen: dict[str, str] = {}

    def capture(razorpay, memory, drafter, *, days, merchant):
        seen["merchant"] = merchant
        raise SystemExit(0)  # stop before the run needs a real Razorpay

    monkeypatch.setattr(cli.agent, "run", capture)
    monkeypatch.setattr(cli, "_build_razorpay", lambda args: object())
    monkeypatch.setattr(cli, "_build_memory", lambda args: object())

    try:
        cli.main(["run", "--live", "--merchant", "acme"])
    except SystemExit:
        pass

    assert seen["merchant"] == "acme", (
        f"a live run was namespaced to {seen['merchant']!r}; it would forget "
        "every previous day and re-chase customers who already promised"
    )
