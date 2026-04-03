"""HTTP client for the Context0 REST API.

Provides a thin async wrapper around the Context0 /v1/* endpoints
using httpx. All methods return parsed JSON dicts.
"""

from __future__ import annotations

import os
from typing import Any

import httpx


class Context0Client:
    """Async HTTP client for the Context0 REST API."""

    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
    ) -> None:
        self.base_url = (base_url or os.getenv("CONTEXT0_URL", "http://localhost:8080")).rstrip("/")
        self.api_key = api_key or os.getenv("CONTEXT0_API_KEY", "")
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
    ) -> dict[str, Any]:
        """GET /v1/profiles/{project_id} — get user/project profile."""
        params: dict[str, Any] = {}
        if query:
            params["query"] = query
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
