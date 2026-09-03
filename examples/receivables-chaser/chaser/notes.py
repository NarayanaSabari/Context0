"""Deterministic English sentences written to Kora, and the regexes that
read them back.

Both directions live in one file so they cannot drift apart: a changed
wording is a one-line change here, not a bug hunt across two modules. See
facts.py, which is the only caller of the *_RE patterns below.
"""

from __future__ import annotations

import re
from datetime import date

from .razorpay import Invoice

RUNG_LABEL = {
    "gentle": "gentle reminder",
    "firm": "firm reminder",
    "payment_link": "payment-link offer",
}
LABEL_TO_RUNG = {label: rung for rung, label in RUNG_LABEL.items()}


def _inr(amount: int) -> str:
    return f"₹{amount:,}"


def format_contact(today: date, customer_name: str, invoice: Invoice, rung: str, days_overdue: int) -> str:
    return (
        f"On {today.isoformat()}, sent a {RUNG_LABEL[rung]} to {customer_name} "
        f"for invoice {invoice.id} ({_inr(invoice.amount)}, {days_overdue} days overdue)."
    )


def format_no_response(today: date, customer_name: str, invoice: Invoice) -> str:
    return f"On {today.isoformat()}, no response from {customer_name} for invoice {invoice.id}."


def format_promise(today: date, customer_name: str, invoice: Invoice, promise_date: date) -> str:
    return (
        f"On {today.isoformat()}, {customer_name} promised to pay invoice {invoice.id} "
        f"by {promise_date.isoformat()}."
    )


def format_dispute(today: date, customer_name: str, invoice: Invoice, reply_text: str) -> str:
    return f"On {today.isoformat()}, {customer_name} disputed invoice {invoice.id}: {reply_text}"


def format_payment(today: date, customer_name: str, invoice: Invoice) -> str:
    return (
        f"On {today.isoformat()}, {customer_name} paid invoice {invoice.id} in full "
        f"({_inr(invoice.amount)})."
    )


def format_escalation(today: date, customer_name: str, invoice: Invoice, reason: str) -> str:
    return f"On {today.isoformat()}, invoice {invoice.id} for {customer_name} was escalated to a human: {reason}."


# --- parsing -------------------------------------------------------------
#
# Reading our own templates back is safe because we chose the wording: every
# sentence a memory can hold matches exactly one pattern below, or none, and
# facts.py ignores anything that matches none. No LLM, no fuzzy matching --
# these are the same sentences the format_* functions above produce.

_DATE = r"\d{4}-\d{2}-\d{2}"

CONTACT_RE = re.compile(
    rf"^On (?P<date>{_DATE}), sent a (?P<rung_label>gentle reminder|firm reminder|payment-link offer) "
    rf"to .+ for invoice (?P<invoice_id>\S+) \(.+, (?P<days>\d+) days overdue\)\.$"
)
NO_RESPONSE_RE = re.compile(rf"^On (?P<date>{_DATE}), no response from .+ for invoice (?P<invoice_id>\S+)\.$")
PROMISE_RE = re.compile(
    rf"^On (?P<date>{_DATE}), .+ promised to pay invoice (?P<invoice_id>\S+) by (?P<promise_date>{_DATE})\.$"
)
DISPUTE_RE = re.compile(rf"^On (?P<date>{_DATE}), .+ disputed invoice (?P<invoice_id>\S+): (?P<reason>.+)$")
PAYMENT_RE = re.compile(rf"^On (?P<date>{_DATE}), .+ paid invoice (?P<invoice_id>\S+) in full \(.+\)\.$")
ESCALATION_RE = re.compile(
    rf"^On (?P<date>{_DATE}), invoice (?P<invoice_id>\S+) for .+ was escalated to a human: (?P<reason>.+)\.$"
)
