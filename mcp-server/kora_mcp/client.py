"""HTTP client for the Kora REST API.

Provides a thin async wrapper around the Kora /v1/* endpoints
using httpx. All methods return parsed JSON dicts.
"""

from __future__ import annotations

import os
import warnings
from typing import Any

import httpx

# Variables this package read before the project was renamed from Context0 to
# Kora. The engine refuses to start when it sees one, and the CLI warns; this
# is the same guard for the same reason.
#
# Every setting here has a fallback, so a stale variable is silently wrong
# rather than an error: KORA_HTTP_URL left unset sends requests to localhost
# instead of the configured engine, and KORA_API_KEY left unset sends no key at
# all, which surfaces as 401s the caller has to work backwards from.
#
# This warns rather than raising, matching the CLI. An MCP server is launched
# by an editor -- Claude Code, Cursor, Windsurf -- and a hard failure there
# shows up as a tool that silently does not appear, which is harder to diagnose
# than a warning in the server's stderr.
_RENAMED_ENV = {
    "CONTEXT0_URL": "KORA_HTTP_URL",
    "CONTEXT0_API_KEY": "KORA_API_KEY",
    "CONTEXT0_PROJECT": "KORA_PROJECT",
}


def warn_renamed_env() -> list[str]:
    """Warn about pre-rename variables. Returns the names found, for tests."""
    found = []
    for old, new in _RENAMED_ENV.items():
        if os.getenv(old):
            found.append(old)
            warnings.warn(
                f"{old} is set but no longer read: the project was renamed to "
                f"Kora. Use {new} instead.",
                RuntimeWarning,
                stacklevel=2,
            )
    return found


class KoraClient:
    """Async HTTP client for the Kora REST API."""

    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
    ) -> None:
        warn_renamed_env()
        self.base_url = (base_url or os.getenv("KORA_HTTP_URL", "http://localhost:8080")).rstrip("/")
        self.api_key = api_key or os.getenv("KORA_API_KEY", "")
        self._client = httpx.AsyncClient(
            base_url=self.base_url,
            headers=self._headers(),
            timeout=30.0,
        )

    def _headers(self) -> dict[str, str]:
        h: dict[str, str] = {"Content-Type": "application/json"}
        if self.api_key:
            h["X-API-Key"] = self.api_key
        return h

    async def health(self) -> dict[str, Any]:
        """GET /v1/health"""
        r = await self._client.get("/v1/health")
        r.raise_for_status()
        return r.json()

    async def store(
        self,
        content: str,
        project_id: str,
        memory_type: int = 2,
        tags: list[str] | None = None,
        session_id: str = "",
    ) -> dict[str, Any]:
        """POST /v1/memories — store a single memory."""
        r = await self._client.post("/v1/memories", json={
            "content": content,
            "type": memory_type,
            "project_id": project_id,
            "tags": tags or [],
            "session_id": session_id,
        })
        r.raise_for_status()
        return r.json()

    async def query(
        self,
        query: str,
        project_id: str,
        top_k: int = 5,
    ) -> dict[str, Any]:
        """GET /v1/memories/query — search memories."""
        r = await self._client.get("/v1/memories/query", params={
            "query": query,
            "project_id": project_id,
            "top_k": top_k,
        })
        r.raise_for_status()
        return r.json()

    async def extract(
        self,
        conversation: str,
        project_id: str,
        session_id: str = "",
    ) -> dict[str, Any]:
        """POST /v1/memories/extract — auto-extract memories from conversation."""
        r = await self._client.post("/v1/memories/extract", json={
            "conversation": conversation,
            "project_id": project_id,
            "session_id": session_id,
        })
        r.raise_for_status()
        return r.json()

    async def get_profile(
        self,
        project_id: str,
        query: str = "",
        max_memories: int = 0,
        recency_days: int = 0,
    ) -> dict[str, Any]:
        """GET /v1/profiles/{project_id} — get user/project profile.

        max_memories and recency_days are optional. Zero means the engine's
        documented defaults (200 memories, a 7-day window for what counts as
        current); the engine clamps rather than refusing values above its
        maximum, so an ambitious number is served at the maximum.
        """
        params: dict[str, Any] = {}
        if query:
            params["query"] = query
        if max_memories:
            params["maxMemories"] = max_memories
        if recency_days:
            params["recencyDays"] = recency_days
        r = await self._client.get(f"/v1/profiles/{project_id}", params=params)
        r.raise_for_status()
        return r.json()

    async def connect(
        self,
        from_id: str,
        to_id: str,
        relationship: int = 1,
        weight: float = 1.0,
    ) -> dict[str, Any]:
        """POST /v1/memories/connect — create edge between memories."""
        r = await self._client.post("/v1/memories/connect", json={
            "from_id": from_id,
            "to_id": to_id,
            "relationship": relationship,
            "weight": weight,
        })
        r.raise_for_status()
        return r.json()

    async def delete(self, memory_id: str) -> None:
        """DELETE /v1/memories/{id} — delete a memory."""
        r = await self._client.delete(f"/v1/memories/{memory_id}")
        r.raise_for_status()

    async def get_graph(
        self,
        center_id: str,
        depth: int = 2,
    ) -> dict[str, Any]:
        """GET /v1/memories/{id}/graph — get subgraph."""
        r = await self._client.get(f"/v1/memories/{center_id}/graph", params={"depth": depth})
        r.raise_for_status()
        return r.json()

    async def close(self) -> None:
        await self._client.aclose()
