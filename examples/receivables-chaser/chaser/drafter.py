"""Turns a policy decision into the words sent to a customer.

TemplateDrafter is deterministic and is what the tests exercise: same
inputs, same message, every run. LLMDrafter is an optional wrapper that
rewords the template through an OpenAI-compatible chat endpoint -- wording
only, never the facts or the decision -- and falls back to the template
untouched on any error, so a flaky model server degrades the demo rather
than breaking it.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Optional, Protocol

from .facts import CustomerFacts
from .razorpay import Customer, Invoice


class Drafter(Protocol):
    def draft(self, invoice: Invoice, customer: Customer, rung: str, facts: CustomerFacts) -> str: ...


def _inr(amount: int) -> str:
    return f"₹{amount:,}"


class TemplateDrafter:
    """Deterministic English templates. Mentions what Kora remembers about
    this customer -- a broken or pending promise -- when there is one, which
    is the whole point of chasing with memory instead of a form letter.
    """

    def draft(self, invoice: Invoice, customer: Customer, rung: str, facts: CustomerFacts) -> str:
        greeting = f"Hi {customer.name},"
        amount = _inr(invoice.amount)

        mention = ""
        promise = facts.latest_promise(invoice.id)
        if promise is not None and not facts.has_payment(invoice.id):
            mention = (
                f" You mentioned on {promise.date_made:%d %b} that you'd pay "
                f"by {promise.promise_date:%d %b}."
            )

        if rung == "gentle":
            body = (
                f"This is a friendly reminder that invoice {invoice.id} for {amount}, "
                f"due {invoice.due_date:%d %b}, is still open."
            )
        elif rung == "firm":
            body = (
                f"Invoice {invoice.id} for {amount} is now significantly overdue. "
                "Please arrange payment at your earliest convenience."
            )
        elif rung == "payment_link":
            body = (
                f"Invoice {invoice.id} for {amount} remains unpaid. Here is a payment "
                f"link -- partial payments are accepted: https://rzp.io/i/{invoice.id}"
            )
        else:
            body = f"Following up on invoice {invoice.id} for {amount}."

        return f"{greeting}{mention} {body}"


class LLMDrafter:
    """Rewords a TemplateDrafter message through an OpenAI-compatible chat
    endpoint. The template is generated first and used verbatim on any
    failure, so this can never produce a message that omits or invents a
    fact -- it can only fail closed to the deterministic wording.
    """

    def __init__(self, base_url: str, api_key: str, model: str, fallback: Optional[Drafter] = None) -> None:
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key
        self._model = model
        self._fallback = fallback or TemplateDrafter()

    def draft(self, invoice: Invoice, customer: Customer, rung: str, facts: CustomerFacts) -> str:
        template = self._fallback.draft(invoice, customer, rung, facts)
        try:
            return self._reword(template)
        except Exception:  # noqa: BLE001 - any failure falls back to the template untouched
            return template

    def _reword(self, template: str) -> str:
        payload = {
            "model": self._model,
            "messages": [
                {
                    "role": "system",
                    "content": (
                        "Reword this payment reminder to sound natural and polite. "
                        "Keep every fact, date, amount, and invoice id exactly as given. "
                        "Return only the reworded message, nothing else."
                    ),
                },
                {"role": "user", "content": template},
            ],
            "temperature": 0.3,
        }
        req = urllib.request.Request(
            f"{self._base_url}/chat/completions",
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json", "Authorization": f"Bearer {self._api_key}"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read().decode("utf-8"))
        return data["choices"][0]["message"]["content"].strip()
