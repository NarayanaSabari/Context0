"""High-level Python client for Context0 gRPC API."""

from __future__ import annotations

import json
from contextlib import contextmanager
from dataclasses import dataclass, field
from typing import Generator, Optional

import grpc


@dataclass(frozen=True)
class Memory:
    """A memory node returned from the engine."""

    id: str
    content: str
    type: str
    project_id: str
    tags: list[str]
    created_at: str
    access_count: int
    decay_score: float


@dataclass(frozen=True)
class Edge:
    """A relationship edge between memories."""

    id: str
    from_id: str
    to_id: str
    relationship: str
    weight: float
    created_at: str


@dataclass(frozen=True)
class ContextEdge:
    """A context edge returned with query results."""

    relationship: str
    target_id: str
    target_content: str
    weight: float


@dataclass(frozen=True)
class MemoryResult:
    """A ranked memory result with context."""

    memory: Memory
    context: list[ContextEdge]
    score: float


@dataclass(frozen=True)
class Session:
    """An agent session."""

    id: str
    project_id: str
    agent_id: str
    started_at: str
    ended_at: Optional[str] = None


@dataclass(frozen=True)
class HealthStatus:
    """Engine health status."""

    status: str
    version: str
    node_count: int
    edge_count: int


# Type mapping
_MEMORY_TYPES = {
    "episodic": 1,
    "semantic": 2,
    "procedural": 3,
}

_REL_TYPES = {
    "relates_to": 1,
    "supersedes": 2,
    "caused_by": 3,
}


class Context0Client:
    """Python client for the Context0 memory engine.

    Usage:
        client = Context0Client(
            endpoint="localhost:50051",
            api_key="ctx0_...",
            project="my-project",
        )

        mem = client.store("Project uses PostgreSQL", type="semantic", tags=["db"])
        results = client.query("what database?", top_k=3)
    """

    def __init__(
        self,
        endpoint: str = "localhost:50051",
        api_key: str = "",
        project: str = "default",
    ) -> None:
        self._endpoint = endpoint
        self._api_key = api_key
        self._project = project
        self._channel = grpc.insecure_channel(endpoint)

        # Lazy import generated stubs — they may not exist until proto-gen runs.
        # For the SDK, we use the REST API via grpc as a transport.
        self._metadata: list[tuple[str, str]] = []
        if api_key:
            self._metadata.append(("x-api-key", api_key))

    def close(self) -> None:
        """Close the gRPC channel."""
        self._channel.close()

    def __enter__(self) -> "Context0Client":
        return self

    def __exit__(self, *args: object) -> None:
        self.close()

    def store(
        self,
        content: str,
        type: str = "semantic",
        tags: Optional[list[str]] = None,
        session_id: str = "",
    ) -> Memory:
        """Store a new memory."""
        request = {
            "content": content,
            "type": _MEMORY_TYPES.get(type, 2),
            "project_id": self._project,
            "tags": tags or [],
            "session_id": session_id,
        }
        response = self._call("/context0.v1.Context0/Store", request)
        return _parse_memory(response.get("memory", {}))

    def query(
        self,
        question: str,
        top_k: int = 5,
        types: Optional[list[str]] = None,
        max_depth: int = 2,
    ) -> list[MemoryResult]:
        """Query memories by natural language."""
        type_values = [_MEMORY_TYPES.get(t, 0) for t in (types or [])]
        request = {
            "query": question,
            "project_id": self._project,
            "top_k": top_k,
            "max_depth": max_depth,
            "types": type_values,
        }
        response = self._call("/context0.v1.Context0/Query", request)
        results = []
        for r in response.get("results", []):
            mem = _parse_memory(r.get("memory", {}))
            ctx = [
                ContextEdge(
                    relationship=str(e.get("relationship", "")),
                    target_id=e.get("targetId", ""),
                    target_content=e.get("targetContent", ""),
                    weight=float(e.get("weight", 0)),
                )
                for e in r.get("context", [])
            ]
            results.append(MemoryResult(memory=mem, context=ctx, score=float(r.get("score", 0))))
        return results

    def connect(
        self,
        from_id: str,
        to_id: str,
        relationship: str = "relates_to",
        weight: float = 1.0,
    ) -> Edge:
        """Create a relationship between two memories."""
        request = {
            "from_id": from_id,
            "to_id": to_id,
            "relationship": _REL_TYPES.get(relationship, 1),
            "weight": weight,
        }
        response = self._call("/context0.v1.Context0/Connect", request)
        e = response.get("edge", {})
        return Edge(
            id=e.get("id", ""),
            from_id=e.get("fromId", ""),
            to_id=e.get("toId", ""),
            relationship=str(e.get("relationship", "")),
            weight=float(e.get("weight", 0)),
            created_at=e.get("createdAt", ""),
        )

    def delete(self, memory_id: str) -> None:
        """Delete a memory and its edges."""
        self._call("/context0.v1.Context0/Delete", {"id": memory_id})

    def get_graph(self, center_id: str, depth: int = 2) -> dict:
        """Get a subgraph around a memory."""
        request = {"center_id": center_id, "depth": depth}
        return self._call("/context0.v1.Context0/GetGraph", request)

    def start_session(self, agent_id: str = "python-sdk") -> Session:
        """Start a new agent session."""
        request = {"project_id": self._project, "agent_id": agent_id}
        response = self._call("/context0.v1.SessionService/StartSession", request)
        return _parse_session(response.get("session", {}))

    def end_session(self, session_id: str) -> Session:
        """End an active session."""
        response = self._call("/context0.v1.SessionService/EndSession", {"id": session_id})
        return _parse_session(response.get("session", {}))

    @contextmanager
    def session(self, agent_id: str = "python-sdk") -> Generator[Session, None, None]:
        """Context manager for session lifecycle."""
        sess = self.start_session(agent_id)
        try:
            yield sess
        finally:
            self.end_session(sess.id)

    def health(self) -> HealthStatus:
        """Get engine health status."""
        response = self._call("/context0.v1.HealthService/Health", {})
        return HealthStatus(
            status=response.get("status", ""),
            version=response.get("version", ""),
            node_count=int(response.get("nodeCount", 0)),
            edge_count=int(response.get("edgeCount", 0)),
        )

    def _call(self, method: str, request: dict) -> dict:
        """Make a gRPC call using JSON codec for simplicity.

        In production, this would use generated protobuf stubs.
        For the MVP SDK, we use a lightweight JSON-based approach
        that works without requiring proto codegen on the Python side.
        The REST gateway accepts JSON, so we use HTTP as transport.
        """
        import urllib.request
        import urllib.error

        # Convert gRPC method to REST path.
        rest_url = self._grpc_to_rest(method, request)
        if rest_url:
            return self._rest_call(*rest_url, request)

        # Fallback: direct gRPC would require generated stubs.
        raise NotImplementedError(f"gRPC method {method} not mapped to REST")

    def _grpc_to_rest(self, method: str, request: dict) -> Optional[tuple[str, str]]:
        """Map gRPC methods to REST endpoints."""
        base = f"http://{self._endpoint.replace(':50051', ':8080')}"

        mapping = {
            "/context0.v1.Context0/Store": ("POST", f"{base}/v1/memories"),
            "/context0.v1.Context0/Query": ("GET", f"{base}/v1/memories/query"),
            "/context0.v1.Context0/Connect": ("POST", f"{base}/v1/memories/connect"),
            "/context0.v1.Context0/Delete": ("DELETE", f"{base}/v1/memories/{request.get('id', '')}"),
            "/context0.v1.Context0/GetGraph": ("GET", f"{base}/v1/memories/{request.get('center_id', '')}/graph"),
            "/context0.v1.SessionService/StartSession": ("POST", f"{base}/v1/sessions"),
            "/context0.v1.SessionService/EndSession": ("POST", f"{base}/v1/sessions/{request.get('id', '')}/end"),
            "/context0.v1.HealthService/Health": ("GET", f"{base}/v1/health"),
        }

        return mapping.get(method)

    def _rest_call(self, http_method: str, url: str, request: dict) -> dict:
        """Make an HTTP REST call."""
        import urllib.request
        import urllib.error

        headers = {"Content-Type": "application/json"}
        if self._api_key:
            headers["X-API-Key"] = self._api_key

        if http_method == "GET":
            # Append query params for GET requests.
            params = "&".join(f"{k}={v}" for k, v in request.items() if v)
            if params:
                url = f"{url}?{params}"
            req = urllib.request.Request(url, headers=headers, method="GET")
        elif http_method == "DELETE":
            req = urllib.request.Request(url, headers=headers, method="DELETE")
        else:
            data = json.dumps(request).encode("utf-8")
            req = urllib.request.Request(url, data=data, headers=headers, method=http_method)

        try:
            with urllib.request.urlopen(req) as resp:
                body = resp.read().decode("utf-8")
                if body:
                    return json.loads(body)
                return {}
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8")
            raise RuntimeError(f"Context0 API error ({e.code}): {body}") from e


def _parse_memory(data: dict) -> Memory:
    return Memory(
        id=data.get("id", ""),
        content=data.get("content", ""),
        type=str(data.get("type", "")),
        project_id=data.get("projectId", ""),
        tags=data.get("tags", []),
        created_at=data.get("createdAt", ""),
        access_count=int(data.get("accessCount", 0)),
        decay_score=float(data.get("decayScore", 0)),
    )


def _parse_session(data: dict) -> Session:
    return Session(
        id=data.get("id", ""),
        project_id=data.get("projectId", ""),
        agent_id=data.get("agentId", ""),
        started_at=data.get("startedAt", ""),
        ended_at=data.get("endedAt"),
    )
