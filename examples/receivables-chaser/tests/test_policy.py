"""Unit tests for the deterministic decision engine. Every rung and every
stopping rule gets its own test, constructing CustomerFacts directly rather
than going through Kora -- this is what proves the policy's logic, in
isolation from retrieval.
"""

from __future__ import annotations

from datetime import date, timedelta

from chaser.facts import ContactFact, CustomerFacts, DisputeFact, EscalationFact, NoResponseFact, PromiseFact
from chaser.policy import decide
from chaser.razorpay import Customer, Invoice

TODAY = date(2026, 9, 2)
CUSTOMER = Customer(id="c1", name="Test Co", email="a@test.example")


def _invoice(days_overdue: int, status: str = "open", amount: int = 10000) -> Invoice:
    due = TODAY - timedelta(days=days_overdue)
    return Invoice(
        id="inv_1", customer_id="c1", amount=amount, currency="INR",
        issued_date=due - timedelta(days=20), due_date=due, status=status,
    )


def test_skip_paid_regardless_of_everything_else() -> None:
    invoice = _invoice(days_overdue=100, status="paid")
    facts = CustomerFacts(disputes=[DisputeFact(date=TODAY, invoice_id="inv_1", reason="wrong amount")])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "skip_paid"


def test_skip_unresolved_dispute() -> None:
    invoice = _invoice(days_overdue=10)
    facts = CustomerFacts(disputes=[DisputeFact(date=TODAY, invoice_id="inv_1", reason="wrong amount")])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "skip_dispute"


def test_dispute_does_not_skip_once_paid() -> None:
    # A dispute resolved by payment should not keep blocking future logic --
    # in practice the invoice would also be status="paid" by then, but the
    # dispute-resolution check on its own should not misfire.
    from chaser.facts import PaymentFact

    invoice = _invoice(days_overdue=10)
    facts = CustomerFacts(
        disputes=[DisputeFact(date=TODAY - timedelta(days=5), invoice_id="inv_1", reason="wrong amount")],
        payments=[PaymentFact(date=TODAY - timedelta(days=1), invoice_id="inv_1")],
    )
    assert facts.unresolved_dispute("inv_1") is None
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung != "skip_dispute"


def test_skip_already_escalated() -> None:
    invoice = _invoice(days_overdue=10)
    facts = CustomerFacts(escalations=[EscalationFact(date=TODAY - timedelta(days=1), invoice_id="inv_1", reason="x")])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "skip_already_escalated"


def test_skip_promise_pending_in_future() -> None:
    invoice = _invoice(days_overdue=10)
    facts = CustomerFacts(promises=[
        PromiseFact(date_made=TODAY - timedelta(days=2), invoice_id="inv_1", promise_date=TODAY + timedelta(days=3)),
    ])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "skip_promise_pending"


def test_broken_promise_bumps_one_rung() -> None:
    # 3 days overdue is "gentle" on its own; a broken promise should bump it
    # to "firm".
    invoice = _invoice(days_overdue=3)
    facts = CustomerFacts(promises=[
        PromiseFact(date_made=TODAY - timedelta(days=10), invoice_id="inv_1", promise_date=TODAY - timedelta(days=1)),
    ])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "firm"
    assert action.broke_promise is True


def test_ladder_by_days_overdue() -> None:
    cases = [(1, "gentle"), (7, "gentle"), (8, "firm"), (21, "firm"), (22, "payment_link"), (45, "payment_link"), (46, "human")]
    for days_overdue, expected in cases:
        invoice = _invoice(days_overdue=days_overdue)
        action = decide(invoice, CUSTOMER, CustomerFacts(), TODAY)
        assert action.rung == expected, f"{days_overdue} days overdue expected {expected}, got {action.rung}"


def test_three_unanswered_contacts_escalates_to_human() -> None:
    invoice = _invoice(days_overdue=5)  # would otherwise be "gentle"
    facts = CustomerFacts(no_responses=[
        NoResponseFact(date=TODAY - timedelta(days=d), invoice_id="inv_1") for d in (6, 4, 2)
    ])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "human"


def test_human_rung_ignores_same_day_and_rate_limit_stops() -> None:
    # A human escalation is not itself a customer contact, so the contact
    # stopping rules should not suppress it.
    invoice = _invoice(days_overdue=50)
    facts = CustomerFacts(contacts=[ContactFact(date=TODAY, invoice_id="inv_1", rung="firm")])
    action = decide(invoice, CUSTOMER, facts, TODAY, contacted_today_locally=True)
    assert action.rung == "human"


def test_skip_same_day_contact_from_kora() -> None:
    invoice = _invoice(days_overdue=5)
    facts = CustomerFacts(contacts=[ContactFact(date=TODAY, invoice_id="inv_1", rung="gentle")])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "skip_same_day_contact"


def test_skip_same_day_contact_locally() -> None:
    invoice = _invoice(days_overdue=5)
    action = decide(invoice, CUSTOMER, CustomerFacts(), TODAY, contacted_today_locally=True)
    assert action.rung == "skip_same_day_contact"


def test_skip_rate_limited_after_three_contacts_in_14_days() -> None:
    invoice = _invoice(days_overdue=20)
    facts = CustomerFacts(contacts=[
        ContactFact(date=TODAY - timedelta(days=d), invoice_id="inv_1", rung="gentle") for d in (10, 6, 2)
    ])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "skip_rate_limited"


def test_contact_outside_14_day_window_does_not_count() -> None:
    invoice = _invoice(days_overdue=20)
    facts = CustomerFacts(contacts=[
        ContactFact(date=TODAY - timedelta(days=20), invoice_id="inv_1", rung="gentle"),
        ContactFact(date=TODAY - timedelta(days=18), invoice_id="inv_1", rung="gentle"),
    ])
    action = decide(invoice, CUSTOMER, facts, TODAY)
    assert action.rung == "firm"  # allowed to contact; only 0 contacts fall inside the window
