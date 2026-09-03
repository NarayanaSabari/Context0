"""A thin Kora client for the chaser: store a memory, recall memories by
text, or run without Kora at all.

The chaser reads all of its cross-tick state -- who was contacted when, what
they promised, whether they disputed -- back out of Kora rather than keeping
a local ledger. That is deliberate: it is the only way a policy decision can
be shown to depend on the memory engine rather than on process state the
engine had nothing to do with. See policy.py and facts.py.
"""

from __future__ import annotations

import logging
import sys
from pathlib import Path
from typing import Optional, Protocol

# The SDK lives in sdk/python/src, not installed as a package. Add it to
# sys.path so `python -m chaser` works from this example's own directory
# without a separate `pip install -e`.
_SDK_SRC = Path(__file__).resolve().parents[3] / "sdk" / "python" / "src"
if _SDK_SRC.is_dir() and str(_SDK_SRC) not in sys.path:
    sys.path.insert(0, str(_SDK_SRC))

from kora import KoraClient  # noqa: E402  (path must be set up first)

logger = logging.getLogger("chaser.memory")


class Memory(Protocol):
    def remember(self, project_id: str, content: str, type: str = "episodic") -> None: ...

    def recall(self, project_id: str, query: str, top_k: int = 50) -> list[str]: ...

    def status(self) -> str:
        """One line naming the backend actually in use, for the report.

        Every way of losing Kora at runtime is caught and logged rather than
        raised, so a degraded run prints a report that looks entirely normal.
        Asking the memory to describe itself lets the report carry that fact
        instead of leaving it in a warning that has scrolled away.
        """
        ...


class KoraMemory:
    """Wraps the Kora SDK client.

    Store and query are the only two calls the chaser needs: every event
    (a reminder sent, a promise made, a dispute raised) is written as a
    plain memory, and read back by a text query scoped to the customer.
    """

    def __init__(self, url: str, api_key: str, merchant: str = "demo-merchant") -> None:
        # KoraClient wants a bare host:port, not a URL with a scheme; KORA_URL
        # is documented (and passed by docker compose / the Makefile) as a
        # full URL, so strip the scheme here rather than push that detail
        # onto every caller.
        endpoint = url.split("://", 1)[-1].rstrip("/")
        self._endpoint = endpoint
        self._api_key = api_key
        self._merchant = merchant
        self._clients: dict[str, KoraClient] = {}

    def _client(self, project_id: str) -> KoraClient:
        # The SDK binds a project at construction time; one client per
        # project (one per customer's merchant scope) rather than per call.
        if project_id not in self._clients:
            self._clients[project_id] = KoraClient(
                endpoint=self._endpoint, api_key=self._api_key, project=project_id,
            )
        return self._clients[project_id]

    def remember(self, project_id: str, content: str, type: str = "episodic") -> None:
        self._client(project_id).store(content, type=type)

    def recall(self, project_id: str, query: str, top_k: int = 50) -> list[str]:
        results = self._client(project_id).query(query, top_k=top_k)
        return [r.memory.content for r in results]

    def status(self) -> str:
        return f"Kora at {self._endpoint}"


class NullMemory:
    """Used when no Kora endpoint is configured at all.

    The policy degrades to the escalation ladder alone -- it can no longer
    see promises or disputes -- rather than the demo crashing because no
    memory engine was ever pointed to.
    """

    def __init__(self) -> None:
        self._warned: set[str] = set()

    def remember(self, project_id: str, content: str, type: str = "episodic") -> None:
        pass

    def recall(self, project_id: str, query: str, top_k: int = 50) -> list[str]:
        if project_id not in self._warned:
            logger.warning("no Kora configured; %s is running without memory", project_id)
            self._warned.add(project_id)
        return []

    def status(self) -> str:
        return "no memory (KORA_URL / KORA_API_KEY not set)"


class SafeMemory:
    """Wraps another Memory and never lets a failure reach the agent loop.

    Kora being unreachable at runtime -- container not up, network blip --
    should degrade the demo the same way NullMemory does, not crash it
    mid-run. This is what the CLI wraps KoraMemory in by default.
    """

    def __init__(self, inner: Memory) -> None:
        self._inner = inner
        self._warned = False

    def remember(self, project_id: str, content: str, type: str = "episodic") -> None:
        try:
            self._inner.remember(project_id, content, type=type)
        except Exception as exc:  # noqa: BLE001 - any failure here must not crash the run
            self._warn(exc)

    def recall(self, project_id: str, query: str, top_k: int = 50) -> list[str]:
        try:
            return self._inner.recall(project_id, query, top_k=top_k)
        except Exception as exc:  # noqa: BLE001
            self._warn(exc)
            return []

    def _warn(self, exc: Exception) -> None:
        if not self._warned:
            logger.warning("Kora unreachable (%s); continuing without memory", exc)
            self._warned = True

    def status(self) -> str:
        # A failure part-way through still means the run was not backed by
        # memory for its whole length, so say so rather than reporting the
        # endpoint it started out talking to.
        if self._warned:
            return f"{self._inner.status()} -- UNREACHABLE, ran without memory"
        return self._inner.status()
