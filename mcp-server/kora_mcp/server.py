"""Kora MCP Server — gives any MCP-compatible AI agent persistent memory.

Works with Claude Code, Cursor, Windsurf, Cline, and any MCP client.

Tools:
  - memory_store: Store a memory (fact, event, or procedure)
  - memory_query: Search memories by natural language
  - memory_extract: Auto-extract memories from a conversation
  - memory_profile: Get aggregated user/project profile
  - memory_connect: Create relationship between two memories
  - memory_delete: Delete a memory
  - memory_graph: Get subgraph around a memory

Resources:
  - memory://health: Engine health status
  - memory://profile/{project_id}: User/project profile

Environment:
  KORA_HTTP_URL  Kora API base URL (default: http://localhost:8080)
  KORA_API_KEY   API key for authentication
  KORA_PROJECT   Default project ID (default: "default")
"""

from __future__ import annotations

import os
from typing import Annotated

from fastmcp import FastMCP, Context
from pydantic import Field

from kora_mcp.client import KoraClient

# ── Server setup ────────────────────────────────────────────────────────

mcp = FastMCP(
    "Kora Memory",
    instructions=(
        "Kora provides persistent memory for AI agents. "
        "Use memory_store to save important facts, decisions, and preferences. "
        "Use memory_query to recall relevant context before answering questions. "
        "Use memory_extract to process raw conversations into structured memories. "
        "Use memory_profile to get a complete overview of what you know about a user or project."
    ),
)

# Lazy-initialized client (created on first tool call).
_client: KoraClient | None = None

DEFAULT_PROJECT = os.getenv("KORA_PROJECT", "default")

# Memory type mapping for human-readable input.
MEMORY_TYPES = {"semantic": 2, "episodic": 1, "procedural": 3, "fact": 2, "event": 1, "howto": 3}
REL_TYPES = {"relates_to": 1, "supersedes": 2, "caused_by": 3}


def _get_client() -> KoraClient:
    """Get or create the Kora API client."""
    global _client
    if _client is None:
        _client = KoraClient()
    return _client


# ── Tools ───────────────────────────────────────────────────────────────

@mcp.tool(
    description="Store a new memory in the Kora knowledge graph. "
    "Use this to save important facts, decisions, preferences, events, or procedures "
    "that should be remembered across sessions.",
    tags={"memory", "store"},
)
async def memory_store(
    content: Annotated[str, Field(description="The memory content to store (a fact, event, or procedure)")],
    memory_type: Annotated[str, Field(description="Type of memory: 'fact' (semantic), 'event' (episodic), or 'howto' (procedural)")] = "fact",
    tags: Annotated[list[str], Field(description="Tags for categorization and retrieval")] = [],
    project_id: Annotated[str, Field(description="Project to scope this memory to")] = "",
) -> str:
    """Store a memory in the knowledge graph."""
    client = _get_client()
    pid = project_id or DEFAULT_PROJECT
    mt = MEMORY_TYPES.get(memory_type.lower(), 2)

    result = await client.store(content=content, project_id=pid, memory_type=mt, tags=tags)
    mem = result.get("memory", {})
    return f"Stored memory {mem.get('id', 'unknown')[:8]}... [{memory_type}] tags={tags}"


@mcp.tool(
    description="Search memories by natural language query. "
    "Use this to recall relevant context before answering questions or making decisions. "
    "Returns ranked results with scores.",
    tags={"memory", "query"},
)
async def memory_query(
    query: Annotated[str, Field(description="Natural language search query")],
    top_k: Annotated[int, Field(description="Maximum number of results to return", ge=1, le=20)] = 5,
    project_id: Annotated[str, Field(description="Project to search within")] = "",
) -> str:
    """Search memories by natural language query."""
    client = _get_client()
    pid = project_id or DEFAULT_PROJECT

    result = await client.query(query=query, project_id=pid, top_k=top_k)
    results = result.get("results", [])

    if not results:
        return "No memories found for this query."

    lines = [f"Found {len(results)} memories:\n"]
    for i, r in enumerate(results, 1):
        mem = r.get("memory", {})
        score = r.get("score", 0)
        mtype = mem.get("type", "").replace("MEMORY_TYPE_", "").lower()
        tags = mem.get("tags", [])
        content = mem.get("content", "")
        lines.append(f"{i}. [{mtype}] (score: {score:.2f}) {content}")
        if tags:
            lines.append(f"   tags: {', '.join(tags)}")

    return "\n".join(lines)


@mcp.tool(
    description="Auto-extract structured memories from a raw conversation. "
    "Feed in a multi-turn conversation and the engine automatically identifies "
    "facts, preferences, events, and procedures — creating memory nodes and "
    "relationship edges in the knowledge graph.",
    tags={"memory", "extract"},
)
async def memory_extract(
    conversation: Annotated[str, Field(description="Raw conversation text (multi-turn, newline-separated)")],
    project_id: Annotated[str, Field(description="Project to scope extracted memories to")] = "",
) -> str:
    """Auto-extract memories from a conversation."""
    client = _get_client()
    pid = project_id or DEFAULT_PROJECT

    result = await client.extract(conversation=conversation, project_id=pid)
    memories = result.get("memories", [])
    rels = result.get("relationshipsCreated", 0)

    if not memories:
        return "No memories could be extracted from this conversation."

    lines = [f"Extracted {len(memories)} memories ({rels} relationships created):\n"]
    for mem in memories:
        mtype = mem.get("type", "").replace("MEMORY_TYPE_", "").lower()
        content = mem.get("content", "")
        tags = mem.get("tags", [])
        lines.append(f"- [{mtype}] {content}")
        if tags:
            lines.append(f"  tags: {', '.join(tags)}")

    return "\n".join(lines)


@mcp.tool(
    description="Get an aggregated profile for a user or project, combining stable facts "
    "(preferences, expertise, known information) with recent context (recent events, "
    "7 days by default). Use this at the start of a conversation to understand who "
    "you're talking to. Widen recency_days when the work is episodic and slower moving.",
    tags={"memory", "profile"},
)
async def memory_profile(
    project_id: Annotated[str, Field(description="Project/user to get profile for")] = "",
    query: Annotated[str, Field(description="Optional query to filter profile relevance")] = "",
    max_memories: Annotated[
        int, Field(description="How many memories to build the profile from (0 = engine default of 200)")
    ] = 0,
    recency_days: Annotated[
        int, Field(description="How recent a memory must be to count as current (0 = engine default of 7)")
    ] = 0,
) -> str:
    """Get aggregated user/project profile."""
    client = _get_client()
    pid = project_id or DEFAULT_PROJECT

    result = await client.get_profile(
        project_id=pid, query=query, max_memories=max_memories, recency_days=recency_days
    )

    static = result.get("staticProfile", [])
    dynamic = result.get("dynamicProfile", [])
    total = result.get("totalMemories", 0)

    lines = [f"Profile for '{pid}' ({total} total memories):\n"]

    if static:
        lines.append("== Known Facts & Preferences ==")
        for fact in static:
            mtype = fact.get("type", "").replace("MEMORY_TYPE_", "").lower()
            lines.append(f"- [{mtype}] {fact.get('content', '')}")
    else:
        lines.append("== No stable facts recorded yet ==")

    lines.append("")

    if dynamic:
        lines.append("== Recent Context (last 7 days) ==")
        for fact in dynamic:
            lines.append(f"- {fact.get('content', '')}")
    else:
        lines.append("== No recent events ==")

    return "\n".join(lines)


@mcp.tool(
    description="Create a relationship (edge) between two existing memories. "
    "Use 'relates_to' for general associations, 'supersedes' when a newer fact "
    "replaces an older one, or 'caused_by' for causal relationships.",
    tags={"memory", "connect"},
)
async def memory_connect(
    from_id: Annotated[str, Field(description="Source memory ID")],
    to_id: Annotated[str, Field(description="Target memory ID")],
    relationship: Annotated[str, Field(description="Type: 'relates_to', 'supersedes', or 'caused_by'")] = "relates_to",
    weight: Annotated[float, Field(description="Strength of relationship (0-1)", ge=0, le=1)] = 1.0,
) -> str:
    """Create a relationship between two memories."""
    client = _get_client()
    rel = REL_TYPES.get(relationship.lower(), 1)
    result = await client.connect(from_id=from_id, to_id=to_id, relationship=rel, weight=weight)
    edge = result.get("edge", {})
    return f"Created edge {edge.get('id', 'unknown')[:8]}... ({relationship}, weight={weight})"


@mcp.tool(
    description="Delete a memory from the knowledge graph.",
    tags={"memory", "delete"},
    annotations={"destructiveHint": True},
)
async def memory_delete(
    memory_id: Annotated[str, Field(description="ID of the memory to delete")],
) -> str:
    """Delete a memory and its edges."""
    client = _get_client()
    await client.delete(memory_id)
    return f"Deleted memory {memory_id[:8]}..."


@mcp.tool(
    description="Get the subgraph (neighboring memories and edges) around a specific memory. "
    "Useful for understanding how a memory relates to other knowledge.",
    tags={"memory", "graph"},
)
async def memory_graph(
    memory_id: Annotated[str, Field(description="Center memory ID to explore around")],
    depth: Annotated[int, Field(description="How many hops to traverse", ge=1, le=5)] = 2,
) -> str:
    """Get subgraph around a memory."""
    client = _get_client()
    result = await client.get_graph(center_id=memory_id, depth=depth)

    nodes = result.get("nodes", [])
    edges = result.get("edges", [])

    lines = [f"Subgraph around {memory_id[:8]}... ({len(nodes)} nodes, {len(edges)} edges):\n"]

    if nodes:
        lines.append("Nodes:")
        for n in nodes:
            mtype = n.get("type", "").replace("MEMORY_TYPE_", "").lower()
            lines.append(f"  [{mtype}] {n.get('id', '')[:8]}... {n.get('content', '')[:60]}")

    if edges:
        lines.append("\nEdges:")
        for e in edges:
            rel = e.get("relationship", "").replace("RELATIONSHIP_TYPE_", "").lower()
            lines.append(f"  {e.get('fromId', '')[:8]}... --{rel}--> {e.get('toId', '')[:8]}...")

    if not nodes and not edges:
        lines.append("No neighboring memories found.")

    return "\n".join(lines)


# ── Resources ───────────────────────────────────────────────────────────

@mcp.resource("memory://health", description="Kora engine health status and statistics")
async def health_resource() -> str:
    """Get engine health status."""
    client = _get_client()
    data = await client.health()
    return (
        f"Status: {data.get('status', 'unknown')}\n"
        f"Version: {data.get('version', 'unknown')}\n"
        f"Nodes: {data.get('nodeCount', 0)}\n"
        f"Edges: {data.get('edgeCount', 0)}"
    )


@mcp.resource(
    "memory://profile/{project_id}",
    description="User/project profile with known facts and recent context",
)
async def profile_resource(project_id: str) -> str:
    """Get profile as a resource."""
    return await memory_profile(project_id=project_id)


# ── Entry point ─────────────────────────────────────────────────────────

def main():
    """Run the Kora MCP server."""
    mcp.run()


if __name__ == "__main__":
    main()
