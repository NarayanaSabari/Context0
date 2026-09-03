"""The two numbers the README, the pitch and the video all lead with.

    | escalation ladder alone | Rs 633,300 | 216 messages |
    | with Kora as memory     | Rs 633,300 |  49 messages |

Those are the project's headline claim, and until this file they were pinned
by nothing: they appeared in prose and in code comments, so any change to the
policy, the fixture or the fusion weights could have moved them silently and
the suite would still have passed. A published number with no test behind it
is a number that goes stale the first time someone edits the thing it measures.

This asserts both arms against the committed fixture, and asserts the fixture
itself is the one those numbers were measured on. A deliberate change to the
policy should fail here and be re-measured; that is the point.

The memory arm runs against RankingFakeMemory rather than a live engine,
because CI has no Kora. That fake ranks by overlap and truncates at top_k like
the real engine, and test_agent_live_memory.py separately asserts it produces
the same run a perfect memory does. The live equivalence was verified by hand
against a fresh `docker compose up` on a virgin database: 49 messages,
Rs 633,300, three consecutive runs, byte-identical audit logs.
"""

from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path

from chaser import agent, report
from chaser.drafter import TemplateDrafter
from chaser.razorpay import RecordedRazorpay

EXAMPLE_ROOT = Path(__file__).resolve().parent.parent
WORLD = EXAMPLE_ROOT / "fixtures" / "world.json"

# The fixture these numbers were measured on. make_world.py is seeded, so this
# is reproducible with `python3 make_world.py`; if that regenerates a different
# world the numbers below stop describing it, which is what this pins.
WORLD_SHA256 = "25f0808294f5866ee251acc691f2b845008da3fe3a47d1edb9ea64107c58590c"

DAYS = 21
EXPECTED_RECOVERED = 633_300
EXPECTED_MESSAGES_WITHOUT_MEMORY = 216
EXPECTED_MESSAGES_WITH_MEMORY = 49


def _run(memory):
    return agent.run(
        RecordedRazorpay(WORLD), memory, TemplateDrafter(), days=DAYS, merchant="bench",
    )


def _totals(result):
    data = report.build_report(
        result, "bench",
        RecordedRazorpay(WORLD).today(),
        RecordedRazorpay(WORLD).today(),
        memory_status="test",
    )
    return data["amount_recovered"], sum(data["contacts_by_rung"].values())


def test_the_fixture_is_the_one_the_numbers_were_measured_on() -> None:
    """The workload has to be pinned, or the results describe nothing.

    50 invoices, 20 customers, 21 days, anchored at a fixed date. Regenerate it
    with `python3 make_world.py` (seed 20260902) and this hash must not move.
    """
    digest = hashlib.sha256(WORLD.read_bytes()).hexdigest()
    assert digest == WORLD_SHA256, (
        f"fixtures/world.json changed (now {digest}). The published 216 and 49 "
        "were measured on the old one; re-measure before updating this hash."
    )

    world = json.loads(WORLD.read_text())
    assert len(world["invoices"]) == 50
    assert len({i["customer_id"] for i in world["invoices"]}) == 20
    assert world["today"] == "2026-09-02", "the run's clock is the fixture's, not the wall's"


def test_without_memory_the_ladder_sends_216_messages(fake_memory) -> None:
    """The control arm: no memory, so every day is decided from scratch."""
    recovered, messages = _totals(_run(_NoMemory()))

    assert recovered == EXPECTED_RECOVERED
    assert messages == EXPECTED_MESSAGES_WITHOUT_MEMORY, (
        f"the no-memory arm sent {messages} messages, not "
        f"{EXPECTED_MESSAGES_WITHOUT_MEMORY}; the README publishes that number"
    )


def test_with_memory_the_same_money_costs_49_messages(ranking_fake_memory) -> None:
    """The treatment arm: same fixture, same rules, memory in the loop."""
    recovered, messages = _totals(_run(ranking_fake_memory))

    assert recovered == EXPECTED_RECOVERED, (
        "the memory arm recovered a different amount; the claim is that memory "
        "buys fewer messages for the SAME money, so this must not move"
    )
    assert messages == EXPECTED_MESSAGES_WITH_MEMORY, (
        f"the memory arm sent {messages} messages, not "
        f"{EXPECTED_MESSAGES_WITH_MEMORY}; the README publishes that number"
    )


def test_the_comparison_is_like_for_like(ranking_fake_memory) -> None:
    """Both arms must differ only in whether memory is present.

    A comparison where the two arms also differ in fixture, day count or
    policy would be measuring something other than what it claims.
    """
    without = _run(_NoMemory())
    with_memory = _run(ranking_fake_memory)

    assert len(without.final_invoices) == len(with_memory.final_invoices) == 50
    assert {e.date for e in without.audit} == {e.date for e in with_memory.audit}
    assert len({e.date for e in without.audit}) == DAYS

    paid_without = {i for i, inv in without.final_invoices.items() if inv.status == "paid"}
    paid_with = {i for i, inv in with_memory.final_invoices.items() if inv.status == "paid"}
    assert paid_without == paid_with, (
        "the two arms recovered different invoices, so the equal totals are a "
        "coincidence rather than the same outcome reached more cheaply"
    )


def test_the_saving_comes_from_rules_that_name_their_reason(ranking_fake_memory) -> None:
    """The 167 messages memory saves must be attributable, not unexplained.

    Every suppressed contact should carry a named reason a merchant could be
    shown. This is what makes the number a result rather than an artefact.
    """
    result = _run(ranking_fake_memory)

    reasons = {e.rung for e in result.audit if not e.contacted}
    for expected in ("skip_promise_pending", "skip_dispute", "skip_same_day_contact"):
        assert expected in reasons, f"{expected} never fired; the saving is unexplained"

    assert all(
        e.reason for e in result.audit
    ), "an audit entry has no reason; every decision must be explainable"


class _NoMemory:
    """NullMemory without the warning log, for the control arm."""

    def remember(self, project_id: str, content: str, type: str = "episodic") -> None:
        pass

    def recall(self, project_id: str, query: str, top_k: int = 50) -> list[str]:
        return []

    def status(self) -> str:
        return "no memory"
