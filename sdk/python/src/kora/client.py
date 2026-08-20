"""High-level Python client for the Kora REST API."""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Generator, Optional


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

# Maps each SDK operation to its REST verb and path template.
class SessionAlreadyEndedError(RuntimeError):
    """Raised when ending a session that has already ended.

    The server answers a repeat end with 409 Conflict. That is a state
    conflict rather than a bad request, and a caller retrying after a timeout
    needs to tell it apart from a real failure.
    """


_ROUTES: dict[str, tuple[str, str]] = {
    "store": ("POST", "/v1/memories"),
    "query": ("GET", "/v1/memories/query"),
    "connect": ("POST", "/v1/memories/connect"),
    "delete": ("DELETE", "/v1/memories/{id}"),
    "get_graph": ("GET", "/v1/memories/{center_id}/graph"),
    "start_session": ("POST", "/v1/sessions"),
    "end_session": ("POST", "/v1/sessions/{id}/end"),
    "health": ("GET", "/v1/health"),
}


class KoraClient:
    """Python client for the Kora memory engine.

    Usage:
        client = KoraClient(
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
        self._base = f"http://{endpoint.replace(':50051', ':8080')}"

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
        response = self._request("store", request)
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
        response = self._request("query", request)
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
        response = self._request("connect", request)
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
        self._request("delete", {"id": memory_id})

    def get_graph(self, center_id: str, depth: int = 2) -> dict:
        """Get a subgraph around a memory."""
        request = {"center_id": center_id, "depth": depth}
        return self._request("get_graph", request)

    def start_session(self, agent_id: str = "python-sdk") -> Session:
        """Start a new agent session."""
        request = {"project_id": self._project, "agent_id": agent_id}
        response = self._request("start_session", request)
        return _parse_session(response.get("session", {}))

    def end_session(self, session_id: str) -> Session:
        """End an active session."""
        response = self._request("end_session", {"id": session_id})
        return _parse_session(response.get("session", {}))

    @contextmanager
    def session(self, agent_id: str = "python-sdk") -> Generator[Session, None, None]:
        """Context manager for session lifecycle.

        The session is ended on exit. Ending one the caller has already ended
        is not an error here: the server rejects a repeat end with 409
        Conflict, and raising that from a ``finally`` block would replace
        whatever exception the body was already raising. The caller would be
        shown "session already ended" instead of the failure they need to
        debug.
        """
        sess = self.start_session(agent_id)
        try:
            yield sess
        finally:
            try:
                self.end_session(sess.id)
            except SessionAlreadyEndedError:
                # The caller ended it themselves; the desired state holds.
                pass

    def health(self) -> HealthStatus:
        """Get engine health status."""
        response = self._request("health", {})
        return HealthStatus(
            status=response.get("status", ""),
            version=response.get("version", ""),
            node_count=int(response.get("nodeCount", 0)),
            edge_count=int(response.get("edgeCount", 0)),
        )

    def _request(self, op: str, request: dict) -> dict:
        """Make an HTTP call to the REST gateway for the named operation."""
        http_method, path = _ROUTES[op]
        url = self._base + path.format(**request)

        headers = {"Content-Type": "application/json"}
        if self._api_key:
            headers["X-API-Key"] = self._api_key

        if http_method == "GET":
            # Append query params for GET requests.
            params = urllib.parse.urlencode(
                {k: v for k, v in request.items() if v}, doseq=True
            )
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
            if e.code == 409:
                # 409 Conflict is a state conflict, not a malformed request:
                # the call was well-formed and merely late. Raising it as its
                # own type lets a caller -- including the session context
                # manager -- treat "already in the desired state" differently
                # from a genuine failure.
                raise SessionAlreadyEndedError(
                    f"Kora API error ({e.code}): {body}"
                ) from e
            raise RuntimeError(f"Kora API error ({e.code}): {body}") from e


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
