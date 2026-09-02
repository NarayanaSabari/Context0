"""Reconstructs structured facts about a customer from the raw memory text
Kora returns.

This is deliberately the only state the policy engine reads across ticks --
there is no local database of who was contacted when. Restart the process
and the answer comes back the same, because it was never anywhere but Kora.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date
from typing import Optional

from . import notes


@dataclass(frozen=True)
class ContactFact:
    date: date
    invoice_id: str
    rung: str


@dataclass(frozen=True)
class PromiseFact:
    date_made: date
    invoice_id: str
    promise_date: date


@dataclass(frozen=True)
class DisputeFact:
    date: date
    invoice_id: str
    reason: str


@dataclass(frozen=True)
class PaymentFact:
    date: date
    invoice_id: str


@dataclass(frozen=True)
class NoResponseFact:
    date: date
    invoice_id: str


@dataclass(frozen=True)
class EscalationFact:
    date: date
    invoice_id: str
    reason: str


@dataclass
class CustomerFacts:
    contacts: list[ContactFact] = field(default_factory=list)
    promises: list[PromiseFact] = field(default_factory=list)
    disputes: list[DisputeFact] = field(default_factory=list)
    payments: list[PaymentFact] = field(default_factory=list)
    no_responses: list[NoResponseFact] = field(default_factory=list)
    escalations: list[EscalationFact] = field(default_factory=list)

    def has_payment(self, invoice_id: str) -> bool:
        return any(p.invoice_id == invoice_id for p in self.payments)

    def latest_promise(self, invoice_id: str) -> Optional[PromiseFact]:
        matches = [p for p in self.promises if p.invoice_id == invoice_id]
        if not matches:
            return None
        return max(matches, key=lambda p: p.date_made)

    def unresolved_dispute(self, invoice_id: str) -> Optional[DisputeFact]:
        matches = [d for d in self.disputes if d.invoice_id == invoice_id]
        if not matches or self.has_payment(invoice_id):
            return None
        return max(matches, key=lambda d: d.date)

    def already_escalated(self, invoice_id: str) -> bool:
        return any(e.invoice_id == invoice_id for e in self.escalations)

    def unanswered_count(self, invoice_id: str) -> int:
        return sum(1 for n in self.no_responses if n.invoice_id == invoice_id)

    def contacts_in_window(self, invoice_id: str, today: date, days: int = 14) -> int:
        return sum(
            1 for c in self.contacts
            if c.invoice_id == invoice_id and 0 <= (today - c.date).days < days
        )

    def contacted_today(self, today: date) -> bool:
        return any(c.date == today for c in self.contacts)


def parse_facts(contents: list[str]) -> CustomerFacts:
    """Classifies each memory content string against the note templates and
    folds the matches into structured facts. Content that matches nothing
    (an unrelated memory, or a customer's project holding other data) is
    ignored rather than raising -- Kora's ranking, not this parser, decides
    what comes back.
    """
    result = CustomerFacts()
    for content in contents:
        m = notes.CONTACT_RE.match(content)
        if m:
            result.contacts.append(ContactFact(
                date=date.fromisoformat(m["date"]),
                invoice_id=m["invoice_id"],
                rung=notes.LABEL_TO_RUNG[m["rung_label"]],
            ))
            continue

        m = notes.PROMISE_RE.match(content)
        if m:
            result.promises.append(PromiseFact(
                date_made=date.fromisoformat(m["date"]),
                invoice_id=m["invoice_id"],
                promise_date=date.fromisoformat(m["promise_date"]),
            ))
            continue

        m = notes.DISPUTE_RE.match(content)
        if m:
            result.disputes.append(DisputeFact(
                date=date.fromisoformat(m["date"]), invoice_id=m["invoice_id"], reason=m["reason"],
            ))
            continue

        m = notes.PAYMENT_RE.match(content)
        if m:
            result.payments.append(PaymentFact(date=date.fromisoformat(m["date"]), invoice_id=m["invoice_id"]))
            continue

        m = notes.NO_RESPONSE_RE.match(content)
        if m:
            result.no_responses.append(NoResponseFact(date=date.fromisoformat(m["date"]), invoice_id=m["invoice_id"]))
            continue

        m = notes.ESCALATION_RE.match(content)
        if m:
            result.escalations.append(EscalationFact(
                date=date.fromisoformat(m["date"]), invoice_id=m["invoice_id"], reason=m["reason"],
            ))
            continue

    return result
