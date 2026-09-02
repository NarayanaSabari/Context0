"""Turns a RunResult into a markdown report: what happened, how much money
moved and by which rung, and an honest list of everything not recovered.

No LLM here either -- the numbers are arithmetic over the audit trail and
the invoice ledger, not a summary a model wrote.
"""

from __future__ import annotations

import json
from datetime import date
from pathlib import Path
from typing import Any

from .agent import RunResult


def _inr(amount: int) -> str:
    return f"₹{amount:,}"


def build_report(result: RunResult, merchant: str, start_date: date, end_date: date) -> dict[str, Any]:
    initial = result.initial_invoices
    final = result.final_invoices

    contacts_by_rung = {"gentle": 0, "firm": 0, "payment_link": 0}
    for entry in result.audit:
        if entry.contacted:
            contacts_by_rung[entry.rung] += 1

    outstanding_start = sum(inv.amount for inv in initial.values() if inv.status != "paid")

    recovered_by_rung: dict[str, int] = {}
    total_recovered = 0
    for inv_id, inv in final.items():
        if inv.status == "paid" and initial[inv_id].status != "paid":
            rung = result.last_contact_rung.get(inv_id, "no_contact")
            recovered_by_rung[rung] = recovered_by_rung.get(rung, 0) + inv.amount
            total_recovered += inv.amount

    made = len(result.promises_made)
    kept = sum(1 for i in result.promises_made if final[i].status == "paid")
    broken = sum(
        1 for i in result.promises_made
        if final[i].status != "paid" and result.promise_dates.get(i, end_date) < end_date
    )
    pending = made - kept - broken

    # Audit entries are appended in chronological order, so the last one
    # written for an invoice is the most recent decision about it.
    latest_by_invoice = {}
    for entry in result.audit:
        latest_by_invoice[entry.invoice_id] = entry

    exceptions = []
    for inv_id, inv in final.items():
        if inv.status == "paid":
            continue
        entry = latest_by_invoice.get(inv_id)
        customer = result.customers.get(inv.customer_id)
        exceptions.append({
            "invoice_id": inv_id,
            "customer": customer.name if customer else inv.customer_id,
            "amount": inv.amount,
            "last_action": entry.rung if entry else "n/a",
            "reason": entry.reason if entry else "never evaluated",
        })
    exceptions.sort(key=lambda e: e["invoice_id"])

    return {
        "merchant": merchant,
        "start_date": start_date.isoformat(),
        "end_date": end_date.isoformat(),
        "invoices_processed": len(final),
        "contacts_by_rung": contacts_by_rung,
        "amount_outstanding_start": outstanding_start,
        "amount_recovered": total_recovered,
        "amount_recovered_by_rung": recovered_by_rung,
        "promises": {"made": made, "kept": kept, "broken": broken, "pending": pending},
        "disputes_escalated": len(result.disputes),
        "exceptions": exceptions,
    }


def _table(headers: list[str], rows: list[list[str]]) -> str:
    widths = [len(h) for h in headers]
    for row in rows:
        for i, cell in enumerate(row):
            widths[i] = max(widths[i], len(cell))

    def fmt_row(cells: list[str]) -> str:
        return "| " + " | ".join(cell.ljust(widths[i]) for i, cell in enumerate(cells)) + " |"

    lines = [fmt_row(headers), "|" + "|".join("-" * (w + 2) for w in widths) + "|"]
    lines.extend(fmt_row(row) for row in rows)
    return "\n".join(lines)


def _label(rung: str) -> str:
    return rung.replace("_", " ").title()


def render_markdown(data: dict[str, Any]) -> str:
    lines = [
        f"# Receivables Chaser Report -- {data['merchant']}",
        "",
        f"Run: {data['start_date']} to {data['end_date']} ({data['invoices_processed']} invoices)",
        "",
        "## Contacts sent",
        "",
        _table(["Rung", "Contacts"], [[_label(r), str(c)] for r, c in data["contacts_by_rung"].items()]),
        "",
        "## Money",
        "",
    ]

    money_rows = [
        ["Outstanding at start", _inr(data["amount_outstanding_start"])],
        ["Recovered", _inr(data["amount_recovered"])],
    ]
    for rung, amount in sorted(data["amount_recovered_by_rung"].items()):
        money_rows.append([f"  by {_label(rung)}", _inr(amount)])
    lines.append(_table(["Metric", "Amount"], money_rows))
    lines.append("")

    p = data["promises"]
    lines.append("## Promises")
    lines.append("")
    lines.append(_table(
        ["Made", "Kept", "Broken", "Pending"],
        [[str(p["made"]), str(p["kept"]), str(p["broken"]), str(p["pending"])]],
    ))
    lines.append("")
    lines.append(f"Disputes escalated: {data['disputes_escalated']}")
    lines.append("")

    lines.append("## Exceptions (not recovered)")
    lines.append("")
    if data["exceptions"]:
        rows = [
            [e["invoice_id"], e["customer"], _inr(e["amount"]), _label(e["last_action"]), e["reason"]]
            for e in data["exceptions"]
        ]
        lines.append(_table(["Invoice", "Customer", "Amount", "Last action", "Reason"], rows))
    else:
        lines.append("None -- every invoice was recovered.")

    return "\n".join(lines)


def print_report(data: dict[str, Any]) -> None:
    print(render_markdown(data))


def write_json(data: dict[str, Any], path: Path | str) -> None:
    Path(path).write_text(json.dumps(data, indent=2) + "\n")
