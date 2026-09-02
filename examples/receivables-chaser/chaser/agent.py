"""The chase loop: fetch open invoices, decide, draft, contact, remember.

Money state (paid or not, how much) always comes from RazorpayClient, the
source of truth for payment. Relationship state (what was promised, what was
disputed, who was already contacted) always comes from Kora. Nothing here
keeps its own shadow ledger of either -- see policy.py and facts.py for why
that matters.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date, timedelta
from typing import Optional

from . import facts as factsmod
from . import notes, policy, replies
from .drafter import Drafter
from .memory import Memory
from .razorpay import Customer, Invoice, RazorpayClient


@dataclass(frozen=True)
class AuditEntry:
    date: str
    invoice_id: str
    customer_id: str
    customer_name: str
    rung: str
    reason: str
    amount: int
    days_overdue: int
    contacted: bool


@dataclass
class RunResult:
    audit: list[AuditEntry]
    initial_invoices: dict[str, Invoice]
    final_invoices: dict[str, Invoice]
    customers: dict[str, Customer]
    promises_made: set[str] = field(default_factory=set)
    promise_dates: dict[str, date] = field(default_factory=dict)
    disputes: set[str] = field(default_factory=set)
    last_contact_rung: dict[str, str] = field(default_factory=dict)


def run(
    razorpay: RazorpayClient,
    memory: Memory,
    drafter: Drafter,
    days: int,
    merchant: str = "demo-merchant",
) -> RunResult:
    """Runs the chase loop for `days` daily ticks starting at the client's
    anchor date. Deterministic given the client's own data: same world, same
    days, the same audit log every time.
    """
    project_id = f"receivables-{merchant}"
    start_date = razorpay.today()

    audit: list[AuditEntry] = []
    promises_made: set[str] = set()
    promise_dates: dict[str, date] = {}
    disputes: set[str] = set()
    last_contact_rung: dict[str, str] = {}
    paid_recorded: set[str] = set()

    initial_invoices: Optional[dict[str, Invoice]] = None
    final_invoices: dict[str, Invoice] = {}
    customers: dict[str, Customer] = {}

    for offset in range(days):
        today = start_date + timedelta(days=offset)
        razorpay.resolve_pending(today)
        invoices = sorted(razorpay.list_invoices(today), key=lambda inv: inv.id)

        if initial_invoices is None:
            initial_invoices = {inv.id: inv for inv in invoices}
            paid_recorded = {inv.id for inv in invoices if inv.status == "paid"}

        by_customer: dict[str, list[Invoice]] = {}
        for inv in invoices:
            by_customer.setdefault(inv.customer_id, []).append(inv)

        for customer_id in sorted(by_customer):
            customer = razorpay.get_customer(customer_id)
            customers[customer_id] = customer

            query = f"customer {customer.name} invoice payment promise dispute contact"
            raw = memory.recall(project_id, query, top_k=50)
            customer_facts = factsmod.parse_facts(raw)
            contacted_today_locally = False

            for inv in by_customer[customer_id]:
                action = policy.decide(inv, customer, customer_facts, today, contacted_today_locally)
                days_overdue = (today - inv.due_date).days

                if action.rung in policy.CONTACT_RUNGS:
                    message = drafter.draft(inv, customer, action.rung, customer_facts)
                    reply = razorpay.contact(inv, customer, message, today)
                    memory.remember(project_id, notes.format_contact(today, customer.name, inv, action.rung, days_overdue))
                    contacted_today_locally = True
                    last_contact_rung[inv.id] = action.rung

                    if reply is None:
                        memory.remember(project_id, notes.format_no_response(today, customer.name, inv))
                    elif replies.is_dispute(reply):
                        memory.remember(project_id, notes.format_dispute(today, customer.name, inv, reply.strip()))
                        disputes.add(inv.id)
                    else:
                        promise_date = replies.find_promise_date(reply)
                        if promise_date is not None:
                            memory.remember(project_id, notes.format_promise(today, customer.name, inv, promise_date))
                            promises_made.add(inv.id)
                            promise_dates[inv.id] = promise_date
                elif action.rung == "human":
                    memory.remember(
                        project_id, notes.format_escalation(today, customer.name, inv, action.reason), type="semantic",
                    )

                audit.append(AuditEntry(
                    date=today.isoformat(),
                    invoice_id=inv.id,
                    customer_id=customer.id,
                    customer_name=customer.name,
                    rung=action.rung,
                    reason=action.reason,
                    amount=inv.amount,
                    days_overdue=days_overdue,
                    contacted=action.rung in policy.CONTACT_RUNGS,
                ))

        # End-of-day payment sweep: one place that notices an invoice has
        # gone from open to paid, whether that happened via a scripted
        # reply above or via a promise maturing in resolve_pending() at the
        # top of this tick. Amount and date come from Razorpay, never from
        # parsing a reply.
        for inv in sorted(razorpay.list_invoices(today), key=lambda i: i.id):
            if inv.status == "paid" and inv.id not in paid_recorded:
                customer = razorpay.get_customer(inv.customer_id)
                memory.remember(project_id, notes.format_payment(today, customer.name, inv))
                paid_recorded.add(inv.id)

        final_invoices = {inv.id: inv for inv in sorted(razorpay.list_invoices(today), key=lambda i: i.id)}

    return RunResult(
        audit=audit,
        initial_invoices=initial_invoices or {},
        final_invoices=final_invoices,
        customers=customers,
        promises_made=promises_made,
        promise_dates=promise_dates,
        disputes=disputes,
        last_contact_rung=last_contact_rung,
    )
