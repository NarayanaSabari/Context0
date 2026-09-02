"""Round-trips every note template through parse_facts, proving the write
side and the read side agree -- the property the policy actually depends on.
"""

from __future__ import annotations

from datetime import date

from chaser import notes
from chaser.facts import parse_facts
from chaser.razorpay import Invoice

TODAY = date(2026, 9, 2)
INVOICE = Invoice(
    id="inv_017", customer_id="c1", amount=12400, currency="INR",
    issued_date=date(2026, 8, 1), due_date=date(2026, 8, 18), status="open",
)


def test_contact_round_trip() -> None:
    content = notes.format_contact(TODAY, "Asha Traders", INVOICE, "firm", days_overdue=15)
    facts = parse_facts([content])
    assert len(facts.contacts) == 1
    fact = facts.contacts[0]
    assert fact.date == TODAY
    assert fact.invoice_id == "inv_017"
    assert fact.rung == "firm"


def test_promise_round_trip() -> None:
    promise_date = date(2026, 9, 8)
    content = notes.format_promise(TODAY, "Asha Traders", INVOICE, promise_date)
    facts = parse_facts([content])
    assert len(facts.promises) == 1
    fact = facts.promises[0]
    assert fact.date_made == TODAY
    assert fact.invoice_id == "inv_017"
    assert fact.promise_date == promise_date


def test_dispute_round_trip() -> None:
    content = notes.format_dispute(TODAY, "Asha Traders", INVOICE, "amount looks wrong")
    facts = parse_facts([content])
    assert len(facts.disputes) == 1
    fact = facts.disputes[0]
    assert fact.invoice_id == "inv_017"
    assert fact.reason == "amount looks wrong"


def test_payment_round_trip() -> None:
    content = notes.format_payment(TODAY, "Asha Traders", INVOICE)
    facts = parse_facts([content])
    assert len(facts.payments) == 1
    assert facts.payments[0].invoice_id == "inv_017"
    assert facts.has_payment("inv_017")


def test_no_response_round_trip() -> None:
    content = notes.format_no_response(TODAY, "Asha Traders", INVOICE)
    facts = parse_facts([content])
    assert facts.unanswered_count("inv_017") == 1


def test_escalation_round_trip() -> None:
    content = notes.format_escalation(TODAY, "Asha Traders", INVOICE, "80 days overdue")
    facts = parse_facts([content])
    assert facts.already_escalated("inv_017")
    assert facts.escalations[0].reason == "80 days overdue"


def test_unrelated_text_is_ignored() -> None:
    facts = parse_facts(["Asha Traders likes email over phone.", "the sky is blue"])
    assert facts.contacts == []
    assert facts.promises == []
    assert facts.disputes == []
    assert facts.payments == []
    assert facts.no_responses == []
    assert facts.escalations == []


def test_latest_promise_picks_most_recent() -> None:
    older = notes.format_promise(date(2026, 8, 20), "Asha Traders", INVOICE, date(2026, 8, 25))
    newer = notes.format_promise(date(2026, 8, 27), "Asha Traders", INVOICE, date(2026, 9, 3))
    facts = parse_facts([older, newer])
    latest = facts.latest_promise("inv_017")
    assert latest is not None
    assert latest.promise_date == date(2026, 9, 3)
