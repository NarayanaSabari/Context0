"""The deterministic decision engine.

No LLM anywhere in this file. Given an invoice, its customer, the facts
recovered from Kora, and today's date, `decide` returns the one Action that
fires -- the rule name and a human-readable reason are part of the return
value, not reconstructed after the fact, so the audit trail is exactly what
decided the outcome.

Rule order (each returns before the next is consulted):
  1. skip if the invoice is already paid
  2. skip if an unresolved dispute exists
  3. skip if already escalated to a human (stop-after-escalation)
  4. skip if a promise-to-pay has a date in the future
  5. if a promise's date has passed unpaid, mark it broken and bump one rung
  6. escalation ladder by days overdue: gentle (1-7) -> firm (8-21) ->
     payment-link (22-45) -> human (>45, or after 3 unanswered contacts)
  7. stopping rules on an actual contact: at most 3 contacts per invoice per
     14 days; never contact the same customer twice in one day
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date

from .facts import CustomerFacts
from .razorpay import Customer, Invoice

LADDER = ["gentle", "firm", "payment_link", "human"]
CONTACT_RUNGS = frozenset({"gentle", "firm", "payment_link"})


@dataclass(frozen=True)
class Action:
    rung: str  # a CONTACT_RUNGS member, "human", or one of the skip_* rungs below
    reason: str
    broke_promise: bool = False


def _base_rung(days_overdue: int) -> str:
    if days_overdue <= 7:
        return "gentle"
    if days_overdue <= 21:
        return "firm"
    if days_overdue <= 45:
        return "payment_link"
    return "human"


def _bump(rung: str) -> str:
    return LADDER[min(LADDER.index(rung) + 1, len(LADDER) - 1)]


def decide(
    invoice: Invoice,
    customer: Customer,
    facts: CustomerFacts,
    today: date,
    contacted_today_locally: bool = False,
) -> Action:
    """`contacted_today_locally` covers a customer with more than one
    invoice decided within the same tick: the first contact this tick has
    not been written to Kora yet when the second invoice is decided, so the
    caller passes what it already knows happened moments ago in the same
    pass.
    """

    if invoice.status == "paid":
        return Action("skip_paid", "invoice already paid")

    if facts.unresolved_dispute(invoice.id) is not None:
        return Action("skip_dispute", "dispute open, escalated to human")

    if facts.already_escalated(invoice.id):
        return Action("skip_already_escalated", "already escalated to human, awaiting manual resolution")

    days_overdue = (today - invoice.due_date).days

    broke_promise = False
    promise = facts.latest_promise(invoice.id)
    if promise is not None and not facts.has_payment(invoice.id):
        if promise.promise_date >= today:
            return Action(
                "skip_promise_pending",
                f"promised to pay by {promise.promise_date.isoformat()}, not due yet",
            )
        broke_promise = True

    rung = _base_rung(days_overdue)
    if broke_promise:
        rung = _bump(rung)
    if facts.unanswered_count(invoice.id) >= 3:
        rung = "human"

    if rung == "human":
        if broke_promise:
            reason = f"broken promise (was due {promise.promise_date.isoformat()})"
        elif days_overdue > 45:
            reason = f"{days_overdue} days overdue"
        else:
            reason = "3 contacts with no response"
        return Action("human", f"escalated to human: {reason}", broke_promise=broke_promise)

    if contacted_today_locally or facts.contacted_today(today):
        return Action("skip_same_day_contact", "customer already contacted today")

    if facts.contacts_in_window(invoice.id, today, days=14) >= 3:
        return Action("skip_rate_limited", "3 contacts in the last 14 days, waiting")

    reason = "broken promise, escalation stepped up" if broke_promise else f"{days_overdue} days overdue"
    return Action(rung, reason, broke_promise=broke_promise)
