#!/usr/bin/env python3
"""Generates fixtures/world.json: a seeded synthetic world for the recorded
demo. Deterministic given --seed, so the fixture in this repo is exactly
what this script produces -- rerunning it is a no-op, not a drift risk.

50 invoices across 20 customers, due dates spread over the 60 days before
--today, and one scripted behaviour per customer:

  pays_after_first_contact  pays the day they are first reminded
  promise_then_pay          promises a date on first contact, and pays it
  promise_then_miss         promises a date on first contact, then misses it
  dispute                   disputes the invoice on first contact
  unreachable               never replies
  already_paid              settled before the run starts
"""

from __future__ import annotations

import argparse
import json
import random
from datetime import date, timedelta
from pathlib import Path

CUSTOMER_NAMES = [
    "Asha Traders", "Bhavani Textiles", "Chandra Electronics", "Deccan Foods",
    "Ekta Logistics", "Falcon Auto Parts", "Ganga Steel Co", "Hariom Furnishings",
    "Indus Packaging", "Jyoti Pharma", "Kaveri Agro", "Lotus Interiors",
    "Manasa Pumps", "Nandi Hardware", "Orbit Print Works", "Prakash Plastics",
    "Rudra Chemicals", "Sarovar Foods", "Tulasi Textiles", "Varun Motors",
]

BEHAVIOR_COUNTS = {
    "pays_after_first_contact": 4,
    "promise_then_pay": 4,
    "promise_then_miss": 3,
    "dispute": 3,
    "unreachable": 3,
    "already_paid": 3,
}
assert sum(BEHAVIOR_COUNTS.values()) == len(CUSTOMER_NAMES)


def build_world(seed: int, today: date, invoice_count: int) -> dict:
    rng = random.Random(seed)

    behaviors = [b for b, n in BEHAVIOR_COUNTS.items() for _ in range(n)]
    rng.shuffle(behaviors)

    customers = []
    for i, (name, behavior) in enumerate(zip(CUSTOMER_NAMES, behaviors), start=1):
        cust_id = f"cust_{i:02d}"
        slug = name.lower().replace(" ", ".")
        customers.append({
            "id": cust_id,
            "name": name,
            "email": f"accounts@{slug}.example",
            "behavior": behavior,
        })

    # Every customer gets at least two invoices; the rest of the count is
    # handed out randomly, capped so no one customer dominates the demo.
    counts = {c["id"]: 2 for c in customers}
    remaining = invoice_count - sum(counts.values())
    eligible = [c["id"] for c in customers]
    while remaining > 0:
        cid = rng.choice(eligible)
        if counts[cid] >= 4:
            continue
        counts[cid] += 1
        remaining -= 1

    by_id = {c["id"]: c for c in customers}
    invoices = []
    n = 0
    for cust_id, count in counts.items():
        behavior = by_id[cust_id]["behavior"]
        for _ in range(count):
            n += 1
            inv_id = f"inv_{n:03d}"
            days_overdue = rng.randint(0, 60)
            due_date = today - timedelta(days=days_overdue)
            issued_date = due_date - timedelta(days=rng.randint(15, 30))
            amount = rng.randint(20, 850) * 100  # rounded to the nearest 100 rupees

            status = "open"
            paid_date = None
            if behavior == "already_paid":
                status = "paid"
                paid_date = due_date - timedelta(days=rng.randint(1, 15))
                if paid_date >= today:
                    paid_date = due_date  # keep it comfortably in the past

            invoices.append({
                "id": inv_id,
                "customer_id": cust_id,
                "amount": amount,
                "currency": "INR",
                "issued_date": issued_date.isoformat(),
                "due_date": due_date.isoformat(),
                "status": status,
                "paid_date": paid_date.isoformat() if paid_date else None,
            })

    return {"today": today.isoformat(), "customers": customers, "invoices": invoices}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=int, default=20260902)
    parser.add_argument("--today", default="2026-09-02")
    parser.add_argument("--invoices", type=int, default=50)
    parser.add_argument("--out", default=str(Path(__file__).parent / "fixtures" / "world.json"))
    args = parser.parse_args()

    world = build_world(args.seed, date.fromisoformat(args.today), args.invoices)
    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(world, indent=2) + "\n")
    print(f"wrote {len(world['customers'])} customers and {len(world['invoices'])} invoices to {out_path}")


if __name__ == "__main__":
    main()
