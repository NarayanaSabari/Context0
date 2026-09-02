"""Classifies a customer's reply text without an LLM.

Payment is never inferred from reply text -- it comes from Razorpay, the
source of truth for money movement (see agent.py's payment sweep). This
module only answers two questions about a reply: does it contain a promised
date, and does it read as a dispute.
"""

from __future__ import annotations

import re
from datetime import date
from typing import Optional

_DATE_RE = re.compile(r"(\d{4}-\d{2}-\d{2})")
_DISPUTE_WORDS = ("dispute", "disputing", "incorrect", "wrong")


def find_promise_date(reply: str) -> Optional[date]:
    """A simple regex on dates: the first ISO date in the reply, if any."""
    match = _DATE_RE.search(reply)
    if match is None:
        return None
    return date.fromisoformat(match.group(1))


def is_dispute(reply: str) -> bool:
    lowered = reply.lower()
    return any(word in lowered for word in _DISPUTE_WORDS)
