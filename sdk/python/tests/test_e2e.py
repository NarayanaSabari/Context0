"""End-to-end tests for the Python SDK, against a running Context0.

The SDK is a public interface with no test coverage at all: 11 methods, a
hand-maintained route table, and response parsing that renames every field on
the way in (`projectId` -> `project_id`, `targetContent` -> `target_content`).
Every other public surface in this project is verified against a live cluster;
this one was not, so a broken route or a renamed response field would reach
users with nothing to catch it.

Run against a reachable deployment:

    CONTEXT0_ENDPOINT=localhost:8080 CONTEXT0_API_KEY=ctx0_... \\
        python3 sdk/python/tests/test_e2e.py

Note the endpoint convention: the client takes a gRPC-style host:port and
rewrites :50051 to :8080 internally, which is itself worth asserting -- it is
the kind of implicit mapping that breaks silently when a port changes.
"""

import os
import sys
import time
import uuid

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from context0 import Context0Client, SessionAlreadyEndedError  # noqa: E402

ENDPOINT = os.environ.get("CONTEXT0_ENDPOINT", "localhost:50051")
KEY = os.environ.get("CONTEXT0_API_KEY", "")

PASSED = 0
FAILED = 0
FAILURES = []


def check(name, condition, detail=""):
    global PASSED, FAILED
    if condition:
        print(f"  PASS  {name}")
        PASSED += 1
    else:
        print(f"  FAIL  {name}")
        if detail:
            print(f"        {detail}")
        FAILED += 1
        FAILURES.append(name)


def section(title):
    print(f"\n{title}")


def attempt(name, fn, *args, **kwargs):
    """Run an SDK call, reporting an exception as a failure rather than a crash.

    A broken route raises out of _request, and an unhandled traceback loses
    every check after it -- so one defect hides the rest of the report.
    """
    try:
        return fn(*args, **kwargs)
    except Exception as e:  # noqa: BLE001
        check(name, False, f"{type(e).__name__}: {e}")
        return None


def main():
    if not KEY:
        print("CONTEXT0_API_KEY is required", file=sys.stderr)
        return 1

    project = f"sdk-test-{uuid.uuid4().hex[:8]}"
    client = Context0Client(endpoint=ENDPOINT, api_key=KEY, project=project)

    section("1. Health")
    health = attempt("health() completes", client.health)
    if health is None:
        print("\ncannot reach the service; aborting")
        return 1
    check("health() returns a status", health.status == "ok", f"got {health.status!r}")
    # Parsed from a renamed JSON field; a rename upstream would silently produce
    # 0 here rather than an error.
    check("health() parses the node count", health.node_count > 0,
          f"got {health.node_count}")
    check("health() parses the version", bool(health.version), f"got {health.version!r}")

    section("2. Store")
    marker = uuid.uuid4().hex[:10]
    mem = client.store(
        content=f"sdk probe {marker} about prometheus",
        type="semantic",
        tags=["sdk", "metrics"],
    )
    check("store() returns an id", bool(mem.id), f"got {mem.id!r}")
    check("store() round-trips the content", marker in mem.content, mem.content)
    # The wire format is projectId; the dataclass field is project_id. Exactly
    # the kind of mapping that breaks without anyone noticing.
    check("store() maps projectId -> project_id", mem.project_id == project,
          f"got {mem.project_id!r}, want {project!r}")
    check("store() preserves tags", set(mem.tags) >= {"sdk", "metrics"},
          f"got {mem.tags}")
    check("store() parses created_at", bool(mem.created_at), f"got {mem.created_at!r}")
    # The SDK maps "semantic" to a protobuf enum value. A wrong mapping stores
    # the memory under the wrong type, which only shows up in ranking later.
    check("store() maps the type name to the right enum",
          "SEMANTIC" in mem.type.upper(), f"got {mem.type!r}")

    section("3. Query")
    # The write must be findable by a keyword unique to it: the same invariant
    # the soak checks, asserted here through the SDK's own surface.
    results = attempt("query() completes", client.query, question=marker, top_k=10)
    if results is None:
        results = []
    check("query() finds the memory just stored",
          any(marker in r.memory.content for r in results),
          f"{len(results)} results, none containing {marker!r}")
    if results:
        check("query() returns a score", results[0].score > 0,
              f"got {results[0].score}")
        check("query() parses the memory of a result",
              bool(results[0].memory.id), "result has no memory id")

    section("4. Connect")
    other = client.store(content=f"sdk related {marker}", type="semantic")
    edge = client.connect(from_id=mem.id, to_id=other.id, relationship="relates_to")
    check("connect() returns an edge id", bool(edge.id), f"got {edge.id!r}")
    check("connect() maps fromId -> from_id", edge.from_id == mem.id,
          f"got {edge.from_id!r}, want {mem.id!r}")
    check("connect() maps toId -> to_id", edge.to_id == other.id,
          f"got {edge.to_id!r}, want {other.id!r}")

    section("5. Graph")
    graph = client.get_graph(center_id=mem.id)
    nodes = graph.get("nodes", [])
    check("get_graph() returns nodes", len(nodes) > 0, f"got {len(nodes)} nodes")
    check("get_graph() includes the connected memory",
          any(n.get("id") == other.id for n in nodes),
          f"{len(nodes)} nodes, none matching the connected memory")

    section("6. Sessions")
    session = client.start_session(agent_id="sdk-test")
    check("start_session() returns an id", bool(session.id), f"got {session.id!r}")
    check("start_session() maps agentId -> agent_id", session.agent_id == "sdk-test",
          f"got {session.agent_id!r}")
    ended = client.end_session(session.id)
    check("end_session() returns the same session", ended.id == session.id,
          f"got {ended.id!r}, want {session.id!r}")

    # The context manager is the documented ergonomic path, so it must work
    # rather than merely exist.
    with client.session(agent_id="sdk-test-ctx") as s:
        ctx_id = s.id
    check("session() context manager yields a session", bool(ctx_id))

    section("7. Delete")
    client.delete(other.id)
    time.sleep(0.5)
    after = client.query(question=marker, top_k=20)
    check("delete() removes the memory",
          not any(r.memory.id == other.id for r in after),
          "the deleted memory is still returned by query")

    section("8. Authentication is enforced through the SDK")
    anon = Context0Client(endpoint=ENDPOINT, api_key="ctx0_wrong_key", project=project)
    try:
        anon.query(question="x")
        check("a bad key is rejected", False, "the request succeeded")
    except RuntimeError as e:
        check("a bad key is rejected", "401" in str(e), str(e))

    section("9. Session lifecycle")
    # The server rejects a repeat end with 409 Conflict, so a client that
    # retries after a timeout, or one whose context manager ends a session the
    # body already ended, must not be handed a spurious failure.
    sess = client.start_session(agent_id="sdk-test")
    check("start_session returns an id", bool(sess.id), f"got {sess!r}")

    ended = client.end_session(sess.id)
    check("end_session succeeds once", bool(ended.id), f"got {ended!r}")

    try:
        client.end_session(sess.id)
        check("a repeated end is rejected", False, "the second end succeeded")
    except SessionAlreadyEndedError as e:
        check("a repeated end is rejected", "409" in str(e), str(e))
    except RuntimeError as e:
        check("a repeated end is rejected", False,
              f"raised the wrong type; a caller cannot tell a stale retry "
              f"from a real failure: {e}")

    # The context manager ends the session on exit. If the body already ended
    # it, the 409 from that cleanup must not surface.
    try:
        with client.session(agent_id="sdk-test") as s:
            client.end_session(s.id)
        check("the context manager tolerates an already-ended session", True)
    except Exception as e:
        check("the context manager tolerates an already-ended session", False,
              f"exit raised {type(e).__name__}: {e}")

    # And it must never replace the caller's own exception with its cleanup
    # failure, which is what turns a real bug into an unrelated stack trace.
    sentinel = ValueError("the failure the caller needs to see")
    try:
        with client.session(agent_id="sdk-test") as s:
            client.end_session(s.id)
            raise sentinel
    except ValueError as e:
        check("the caller's exception survives session cleanup", e is sentinel, str(e))
    except Exception as e:
        check("the caller's exception survives session cleanup", False,
              f"the original ValueError was replaced by {type(e).__name__}: {e}")

    print(f"\n=== {PASSED} passed, {FAILED} failed ===")
    for f in FAILURES:
        print(f"  failed: {f}")
    return 1 if FAILED else 0


if __name__ == "__main__":
    sys.exit(main())
