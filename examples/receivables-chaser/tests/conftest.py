"""Shared fixtures: an in-memory Kora fake and a small hand-built world
covering every scripted customer behaviour, so integration tests do not
depend on the larger generated fixtures/world.json.
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from chaser.memory import Memory


class FakeMemory:
    """An in-memory Kora stand-in. Unlike the real engine it does no ranking
    or top_k truncation beyond the requested size, so tests exercise the
    policy's own logic rather than retrieval quality -- that is a separate
    concern, covered by the engine's own golden suite.
    """

    def __init__(self) -> None:
        self.store: dict[str, list[str]] = {}

    def remember(self, project_id: str, content: str, type: str = "episodic") -> None:
        self.store.setdefault(project_id, []).append(content)

    def recall(self, project_id: str, query: str, top_k: int = 50) -> list[str]:
        return list(self.store.get(project_id, []))[-top_k:]


@pytest.fixture
def fake_memory() -> Memory:
    return FakeMemory()


@pytest.fixture
def new_fake_memory():
    """A factory rather than a single instance, for tests that need more
    than one independent Kora fake -- e.g. comparing two separate runs.
    """
    return FakeMemory


TINY_WORLD = {
    "today": "2026-09-02",
    "customers": [
        {"id": "c1", "name": "Payer Co", "email": "a@payer.example", "behavior": "pays_after_first_contact"},
        {"id": "c2", "name": "Promiser Co", "email": "b@promiser.example", "behavior": "promise_then_pay"},
        {"id": "c3", "name": "Ghoster Co", "email": "c@ghoster.example", "behavior": "promise_then_miss"},
        {"id": "c4", "name": "Disputer Co", "email": "d@disputer.example", "behavior": "dispute"},
        {"id": "c5", "name": "Silent Co", "email": "e@silent.example", "behavior": "unreachable"},
        {"id": "c6", "name": "Settled Co", "email": "f@settled.example", "behavior": "already_paid"},
    ],
    "invoices": [
        {
            "id": "inv_1", "customer_id": "c1", "amount": 10000, "currency": "INR",
            "issued_date": "2026-08-01", "due_date": "2026-08-28", "status": "open", "paid_date": None,
        },
        {
            "id": "inv_2", "customer_id": "c2", "amount": 20000, "currency": "INR",
            "issued_date": "2026-08-01", "due_date": "2026-08-28", "status": "open", "paid_date": None,
        },
        {
            "id": "inv_3", "customer_id": "c3", "amount": 15000, "currency": "INR",
            "issued_date": "2026-08-01", "due_date": "2026-08-28", "status": "open", "paid_date": None,
        },
        {
            "id": "inv_4", "customer_id": "c4", "amount": 30000, "currency": "INR",
            "issued_date": "2026-08-01", "due_date": "2026-08-28", "status": "open", "paid_date": None,
        },
        {
            # 36 days overdue on day 0, ages past the >45 human threshold
            # partway through a 21-day run.
            "id": "inv_5", "customer_id": "c5", "amount": 5000, "currency": "INR",
            "issued_date": "2026-07-01", "due_date": "2026-07-28", "status": "open", "paid_date": None,
        },
        {
            "id": "inv_6", "customer_id": "c6", "amount": 8000, "currency": "INR",
            "issued_date": "2026-07-01", "due_date": "2026-07-20", "status": "paid", "paid_date": "2026-07-25",
        },
    ],
}


@pytest.fixture
def tiny_world_path(tmp_path: Path) -> Path:
    path = tmp_path / "world.json"
    path.write_text(json.dumps(TINY_WORLD))
    return path
