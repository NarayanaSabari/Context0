"""Razorpay integration: one protocol, two implementations.

RecordedRazorpay replays a scripted world (fixtures/world.json) so the demo
runs offline, in seconds, with the same outcome every time. LiveRazorpay
talks to Razorpay's real test-mode Invoices API. Both expose the same
surface, so the rest of the chaser -- policy, drafter, agent -- never knows
which one it is talking to.
"""

from __future__ import annotations

import base64
import json
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, replace
from datetime import date, timedelta
from pathlib import Path
from typing import Optional, Protocol


@dataclass(frozen=True)
class Customer:
    id: str
    name: str
    email: str


@dataclass(frozen=True)
class Invoice:
    id: str
    customer_id: str
    amount: int  # whole rupees; Razorpay's own API deals in paise, converted at the LiveRazorpay boundary
    currency: str
    issued_date: date
    due_date: date
    status: str  # "open" | "paid"
    paid_date: Optional[date] = None


class RazorpayClient(Protocol):
    def today(self) -> date:
        """The anchor date the run starts from."""
        ...

    def list_invoices(self, as_of: date) -> list[Invoice]:
        """Every invoice known to the merchant, current as of this date.

        Deliberately unfiltered -- including ones already paid -- so the
        policy layer's "skip if paid" rule is the thing that decides, and
        that decision shows up in the audit trail instead of happening
        silently before the chaser ever sees the invoice.
        """
        ...

    def get_invoice(self, invoice_id: str) -> Invoice: ...

    def get_customer(self, customer_id: str) -> Customer: ...

    def resolve_pending(self, as_of: date) -> None:
        """Applies anything that happens with the passage of time and no
        further contact from the chaser (a kept promise coming due).

        A no-op for a live client: time passing there is the real clock,
        not something this call needs to simulate.
        """
        ...

    def contact(self, invoice: Invoice, customer: Customer, message: str, as_of: date) -> Optional[str]:
        """Sends `message` to the customer. Returns their reply text if one
        arrives synchronously, or None.

        The recorded world always resolves synchronously, so its scripted
        customers can reply here. A live run cannot: a real reply comes back
        later, as a webhook or a support ticket, not as this call's return
        value. See LiveRazorpay.contact.
        """
        ...


# Behaviours that produce a promise-to-pay reply on first contact. The two
# differ only in whether the promise is later kept; see RecordedRazorpay.
_BEHAVIORS_WITH_PROMISE = {"promise_then_pay", "promise_then_miss"}


class RecordedRazorpay:
    """Replays fixtures/world.json.

    Customer replies are scripted by a `behavior` label that only this class
    reads -- Customer, the model the policy layer sees, does not carry it.
    A passing test therefore proves the policy engine reasons from Kora
    memories and the invoice ledger alone, the same inputs it would have
    against the real API. Generate the fixture with make_world.py.
    """

    def __init__(self, world_path: Path | str) -> None:
        data = json.loads(Path(world_path).read_text())
        self._today = date.fromisoformat(data["today"])

        self._customers: dict[str, Customer] = {}
        self._behaviors: dict[str, str] = {}
        for c in data["customers"]:
            self._customers[c["id"]] = Customer(id=c["id"], name=c["name"], email=c["email"])
            self._behaviors[c["id"]] = c["behavior"]

        self._invoices: dict[str, Invoice] = {}
        for inv in data["invoices"]:
            self._invoices[inv["id"]] = Invoice(
                id=inv["id"],
                customer_id=inv["customer_id"],
                amount=inv["amount"],
                currency=inv.get("currency", "INR"),
                issued_date=date.fromisoformat(inv["issued_date"]),
                due_date=date.fromisoformat(inv["due_date"]),
                status=inv["status"],
                paid_date=date.fromisoformat(inv["paid_date"]) if inv.get("paid_date") else None,
            )

        # invoice_id -> (promise_date, will_keep). Populated by contact()
        # when a customer's script produces a promise, consumed by
        # resolve_pending() on the day it comes due.
        self._pending_promises: dict[str, tuple[date, bool]] = {}
        # Invoices whose customer has already given its one scripted reply.
        # A promise- or dispute-scripted customer commits once per invoice
        # and goes quiet after, the way someone chased again after already
        # answering often does.
        self._replied_once: set[str] = set()

    def today(self) -> date:
        return self._today

    def list_invoices(self, as_of: date) -> list[Invoice]:
        return list(self._invoices.values())

    def get_invoice(self, invoice_id: str) -> Invoice:
        return self._invoices[invoice_id]

    def get_customer(self, customer_id: str) -> Customer:
        return self._customers[customer_id]

    def resolve_pending(self, as_of: date) -> None:
        due = [inv_id for inv_id, (promise_date, _keep) in self._pending_promises.items() if promise_date <= as_of]
        for inv_id in due:
            promise_date, will_keep = self._pending_promises.pop(inv_id)
            if will_keep:
                self._mark_paid(inv_id, promise_date)

    def contact(self, invoice: Invoice, customer: Customer, message: str, as_of: date) -> Optional[str]:
        behavior = self._behaviors[customer.id]

        if behavior == "pays_after_first_contact":
            self._mark_paid(invoice.id, as_of)
            return f"Paying invoice {invoice.id} now, thank you for the reminder."

        if behavior in _BEHAVIORS_WITH_PROMISE:
            if invoice.id in self._replied_once:
                return None  # already made its one commitment; silent after
            self._replied_once.add(invoice.id)
            offset = 6 if behavior == "promise_then_pay" else 5
            promise_date = as_of + timedelta(days=offset)
            self._pending_promises[invoice.id] = (promise_date, behavior == "promise_then_pay")
            return f"We will pay invoice {invoice.id} by {promise_date.isoformat()}."

        if behavior == "dispute":
            if invoice.id in self._replied_once:
                return None
            self._replied_once.add(invoice.id)
            return f"Invoice {invoice.id} amount looks wrong, we are disputing it."

        if behavior == "unreachable":
            return None

        if behavior == "already_paid":
            # Its invoices start paid, so the policy skips before ever
            # reaching contact(). Kept as an explicit branch so an
            # unrecognised behaviour below raises instead of silently
            # matching here.
            return None

        raise ValueError(f"unknown customer behaviour: {behavior!r}")

    def _mark_paid(self, invoice_id: str, paid_date: date) -> None:
        inv = self._invoices[invoice_id]
        self._invoices[invoice_id] = replace(inv, status="paid", paid_date=paid_date)


class LiveRazorpay:
    """Talks to Razorpay's test-mode Invoices API.

    https://razorpay.com/docs/api/payments/invoices/

    Only the read paths (list, get) and the notify action are exercised by
    this class's own logic; there is no test-mode Razorpay account reachable
    from this environment to verify the write paths against, so `contact` is
    written to the documented contract and should be treated as unverified
    rather than tested. `list_invoices` and `get_invoice` are read paths and
    are the ones the demo relies on if pointed at a real account.
    """

    BASE_URL = "https://api.razorpay.com/v1"

    def __init__(self, key_id: str, key_secret: str) -> None:
        self._auth = base64.b64encode(f"{key_id}:{key_secret}".encode()).decode()
        self._customers: dict[str, Customer] = {}

    def today(self) -> date:
        return date.today()

    def list_invoices(self, as_of: date) -> list[Invoice]:
        # Razorpay has no single filter for "issued or partially_paid", so
        # this is two list calls rather than one.
        invoices: list[Invoice] = []
        for status in ("issued", "partially_paid"):
            payload = self._get("/invoices", {"status": status, "count": 100})
            for item in payload.get("items", []):
                invoices.append(self._to_invoice(item))
        return invoices

    def get_invoice(self, invoice_id: str) -> Invoice:
        return self._to_invoice(self._get(f"/invoices/{invoice_id}"))

    def get_customer(self, customer_id: str) -> Customer:
        if customer_id not in self._customers:
            data = self._get(f"/customers/{customer_id}")
            self._customers[customer_id] = Customer(
                id=data["id"], name=data.get("name", customer_id), email=data.get("email", ""),
            )
        return self._customers[customer_id]

    def resolve_pending(self, as_of: date) -> None:
        pass  # time passing is the real clock here; nothing to simulate

    def contact(self, invoice: Invoice, customer: Customer, message: str, as_of: date) -> Optional[str]:
        # POST /invoices/{id}/notify_by/email sends the reminder through
        # Razorpay's own templates; `message` documents what the chaser
        # decided to say even though the API does not take custom body text
        # for this action. A real reply arrives later as a webhook or a
        # support ticket, never as this call's return value -- which is
        # exactly the gap the recorded world exists to demo around.
        self._post(f"/invoices/{invoice.id}/notify_by/email", {})
        return None

    def _to_invoice(self, item: dict) -> Invoice:
        status = "paid" if item.get("status") == "paid" else "open"
        return Invoice(
            id=item["id"],
            customer_id=(item.get("customer_details") or {}).get("id", ""),
            amount=int(item.get("amount", 0)) // 100,  # paise to rupees
            currency=item.get("currency", "INR"),
            issued_date=date.fromtimestamp(item["date"]) if item.get("date") else date.today(),
            due_date=date.fromtimestamp(item["expire_by"]) if item.get("expire_by") else date.today(),
            status=status,
            paid_date=None,
        )

    def _get(self, path: str, params: Optional[dict] = None) -> dict:
        url = f"{self.BASE_URL}{path}"
        if params:
            url += "?" + urllib.parse.urlencode(params)
        req = urllib.request.Request(url, headers={"Authorization": f"Basic {self._auth}"})
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode())

    def _post(self, path: str, payload: dict) -> dict:
        url = f"{self.BASE_URL}{path}"
        req = urllib.request.Request(
            url,
            data=json.dumps(payload).encode(),
            headers={"Authorization": f"Basic {self._auth}", "Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode())
