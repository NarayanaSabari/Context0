"""Two runs against the same recorded world must produce identical audit
logs. Nothing in the loop should read a wall clock, a random source, or
anything else that could make one run differ from the next.
"""

from __future__ import annotations

from chaser import agent
from chaser.drafter import TemplateDrafter
from chaser.razorpay import RecordedRazorpay


def test_two_runs_produce_identical_audit_logs(tiny_world_path, new_fake_memory) -> None:
    result_a = agent.run(RecordedRazorpay(tiny_world_path), new_fake_memory(), TemplateDrafter(), days=21, merchant="test")
    result_b = agent.run(RecordedRazorpay(tiny_world_path), new_fake_memory(), TemplateDrafter(), days=21, merchant="test")

    assert [tuple(e.__dict__.items()) for e in result_a.audit] == [tuple(e.__dict__.items()) for e in result_b.audit]


def test_two_runs_recover_the_same_amount(tiny_world_path, new_fake_memory) -> None:
    result_a = agent.run(RecordedRazorpay(tiny_world_path), new_fake_memory(), TemplateDrafter(), days=21, merchant="test")
    result_b = agent.run(RecordedRazorpay(tiny_world_path), new_fake_memory(), TemplateDrafter(), days=21, merchant="test")

    paid_a = {inv_id for inv_id, inv in result_a.final_invoices.items() if inv.status == "paid"}
    paid_b = {inv_id for inv_id, inv in result_b.final_invoices.items() if inv.status == "paid"}
    assert paid_a == paid_b
