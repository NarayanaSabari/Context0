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


class RankingFakeMemory:
    """A Kora stand-in that ranks and truncates the way the real engine does.

    FakeMemory above returns everything in a project regardless of the query,
    which makes it blind to the failure that matters most here: if the agent
    writes every customer's notes into one project and then asks for top_k
    results, the engine returns the top_k best matches across ALL customers,
    and a given customer's own promises can be crowded out entirely by
    unrelated traffic. Against FakeMemory that run looks perfect; against a
    real Kora the agent stopped seeing promises and disputes altogether.

    So this fake keeps the two properties of the real engine that the bug
    depended on -- relevance ordering and a hard top_k cut -- while staying
    deterministic. Scoring is deliberately crude (shared words, no IDF, no
    vectors): the point is not to imitate the ranker but to ensure that
    writing unrelated memories into a shared project pushes the relevant
    ones out, which is exactly what a real engine does.
    """

    def __init__(self) -> None:
        self.store: dict[str, list[str]] = {}

    def remember(self, project_id: str, content: str, type: str = "episodic") -> None:
        self.store.setdefault(project_id, []).append(content)

    @staticmethod
    def _score(query: str, content: str) -> int:
        q = {w.strip(".,:()").lower() for w in query.split()}
        c = {w.strip(".,:()").lower() for w in content.split()}
        return len(q & c)

    def recall(self, project_id: str, query: str, top_k: int = 50) -> list[str]:
        items = self.store.get(project_id, [])
        # Rank by relevance, tie-break by recency (later writes first), then
        # cut to top_k -- the order the engine's own fusion produces.
        ranked = sorted(
            enumerate(items),
            key=lambda pair: (-self._score(query, pair[1]), -pair[0]),
        )
        return [content for _, content in ranked[:top_k]]


@pytest.fixture
def fake_memory() -> Memory:
    return FakeMemory()


@pytest.fixture
def ranking_fake_memory() -> Memory:
    return RankingFakeMemory()


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


@pytest.fixture
def new_ranking_fake_memory():
    """A factory, for tests that need to keep one ranking fake alive across
    two separate agent runs -- which is how a persistent store behaves.
    """
    return RankingFakeMemory
