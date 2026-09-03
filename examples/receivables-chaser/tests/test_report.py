"""build_report is pure arithmetic over a RunResult; these tests construct
one by hand rather than running the full agent loop, so they pin down the
arithmetic itself.
"""

from __future__ import annotations

from dataclasses import replace
from datetime import date

from chaser.agent import AuditEntry, RunResult
from chaser.razorpay import Customer, Invoice
from chaser.report import build_report

START = date(2026, 9, 2)
END = date(2026, 9, 22)


def _invoice(inv_id: str, customer_id: str, amount: int, status: str) -> Invoice:
    return Invoice(
        id=inv_id, customer_id=customer_id, amount=amount, currency="INR",
        issued_date=date(2026, 8, 1), due_date=date(2026, 8, 20), status=status,
    )


def _result() -> RunResult:
    initial = {
        "inv_a": _invoice("inv_a", "cust1", 1000, "open"),
        "inv_b": _invoice("inv_b", "cust2", 2000, "open"),
        "inv_c": _invoice("inv_c", "cust1", 500, "paid"),  # already settled before the run
    }
    final = {
        "inv_a": replace(initial["inv_a"], status="paid"),
        "inv_b": initial["inv_b"],  # still open at the end
        "inv_c": initial["inv_c"],
    }
    customers = {
        "cust1": Customer(id="cust1", name="Cust One", email="one@example.com"),
        "cust2": Customer(id="cust2", name="Cust Two", email="two@example.com"),
    }
    audit = [
        AuditEntry(
            date=START.isoformat(), invoice_id="inv_a", customer_id="cust1", customer_name="Cust One",
            rung="gentle", reason="3 days overdue", amount=1000, days_overdue=3, contacted=True,
        ),
        AuditEntry(
            date=START.isoformat(), invoice_id="inv_b", customer_id="cust2", customer_name="Cust Two",
            rung="firm", reason="broken promise, escalation stepped up", amount=2000, days_overdue=12, contacted=True,
        ),
        AuditEntry(
            date=START.isoformat(), invoice_id="inv_c", customer_id="cust1", customer_name="Cust One",
            rung="skip_paid", reason="invoice already paid", amount=500, days_overdue=13, contacted=False,
        ),
    ]
    return RunResult(
        audit=audit,
        initial_invoices=initial,
        final_invoices=final,
        customers=customers,
        promises_made={"inv_b"},
        promise_dates={"inv_b": date(2026, 9, 10)},  # before END, so unpaid means broken
        disputes=set(),
        last_contact_rung={"inv_a": "gentle", "inv_b": "firm"},
    )


def test_recovered_equals_sum_of_newly_paid_invoices() -> None:
    data = build_report(_result(), "acme", START, END)
    assert data["amount_recovered"] == 1000
    assert data["amount_recovered_by_rung"] == {"gentle": 1000}


def test_outstanding_start_excludes_already_paid() -> None:
    data = build_report(_result(), "acme", START, END)
    assert data["amount_outstanding_start"] == 3000  # inv_a + inv_b; inv_c was already paid


def test_promise_broken_when_unpaid_past_its_date() -> None:
    data = build_report(_result(), "acme", START, END)
    assert data["promises"] == {"made": 1, "kept": 0, "broken": 1, "pending": 0}


def test_exception_list_has_the_unrecovered_invoice() -> None:
    data = build_report(_result(), "acme", START, END)
    ids = [e["invoice_id"] for e in data["exceptions"]]
    assert ids == ["inv_b"]
    assert data["exceptions"][0]["reason"] == "broken promise, escalation stepped up"


def test_disputes_escalated_counts_the_set() -> None:
    result = _result()
    result.disputes = {"inv_b"}
    data = build_report(result, "acme", START, END)
    assert data["disputes_escalated"] == 1
