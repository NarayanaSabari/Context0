"""Integration tests: the full agent loop against RecordedRazorpay and an
in-memory Kora fake, one customer per scripted behaviour. Fully offline.
"""

from __future__ import annotations

from chaser import agent
from chaser.drafter import TemplateDrafter
from chaser.razorpay import RecordedRazorpay


def _run(tiny_world_path, fake_memory, days=21):
    razorpay = RecordedRazorpay(tiny_world_path)
    return agent.run(razorpay, fake_memory, TemplateDrafter(), days=days, merchant="test")


def test_pays_after_first_contact_is_recovered(tiny_world_path, fake_memory) -> None:
    result = _run(tiny_world_path, fake_memory)
    assert result.final_invoices["inv_1"].status == "paid"
    contacts = [a for a in result.audit if a.invoice_id == "inv_1" and a.contacted]
    assert len(contacts) == 1  # paid the day it was first reminded; never contacted again


def test_promise_then_pay_is_kept(tiny_world_path, fake_memory) -> None:
    result = _run(tiny_world_path, fake_memory)
    assert result.final_invoices["inv_2"].status == "paid"
    assert "inv_2" in result.promises_made


def test_promise_then_miss_breaks_and_stays_open(tiny_world_path, fake_memory) -> None:
    result = _run(tiny_world_path, fake_memory)
    assert result.final_invoices["inv_3"].status == "open"
    assert "inv_3" in result.promises_made
    # With Kora remembering the pending promise, the chaser should not have
    # pestered this customer every single day waiting for the date to pass.
    contacts = [a for a in result.audit if a.invoice_id == "inv_3" and a.contacted]
    assert 1 <= len(contacts) <= 6


def test_dispute_is_never_recontacted(tiny_world_path, fake_memory) -> None:
    result = _run(tiny_world_path, fake_memory)
    assert "inv_4" in result.disputes
    assert result.final_invoices["inv_4"].status == "open"
    contacts = [a for a in result.audit if a.invoice_id == "inv_4" and a.contacted]
    assert len(contacts) == 1  # disputed on the first reminder; skip_dispute after that


def test_unreachable_eventually_escalates_to_human(tiny_world_path, fake_memory) -> None:
    result = _run(tiny_world_path, fake_memory)
    human_entries = [a for a in result.audit if a.invoice_id == "inv_5" and a.rung == "human"]
    assert human_entries, "an unreachable customer overdue this long should reach human escalation"


def test_already_paid_is_never_contacted(tiny_world_path, fake_memory) -> None:
    result = _run(tiny_world_path, fake_memory)
    entries = [a for a in result.audit if a.invoice_id == "inv_6"]
    assert entries
    assert all(a.rung == "skip_paid" for a in entries)


def test_stopping_rule_never_contacts_same_customer_twice_a_day(tiny_world_path, fake_memory) -> None:
    result = _run(tiny_world_path, fake_memory)
    by_customer_day: dict[tuple[str, str], int] = {}
    for entry in result.audit:
        if not entry.contacted:
            continue
        key = (entry.customer_id, entry.date)
        by_customer_day[key] = by_customer_day.get(key, 0) + 1
    assert all(count <= 1 for count in by_customer_day.values())


def test_recorded_run_completes_quickly_with_null_memory() -> None:
    import time

    from chaser.memory import NullMemory
    from chaser.razorpay import RecordedRazorpay as RR

    world = _default_world_path()
    start = time.monotonic()
    razorpay = RR(world)
    agent.run(razorpay, NullMemory(), TemplateDrafter(), days=21, merchant="test")
    assert time.monotonic() - start < 10


def _default_world_path():
    from pathlib import Path

    return Path(__file__).resolve().parent.parent / "fixtures" / "world.json"
