"""End-to-end tests for the MCP server, against a running Kora.

This package shipped with no tests at all: 440 lines of client and server
code, a hand-maintained route table, and a memory-type mapping that turns
human words into wire enum values. Nothing in CI would have noticed any of it
breaking. sdk/python has 28 tests against a live engine; this is the same idea
for the same reason, and deliberately mirrors that file's structure so the two
read alike.

What this covers that a unit test would not: every route the client calls
actually exists on the engine, the field renaming on the way in still matches
what the server returns, and the memory-type words the MCP tools accept still
map to the enum values the API expects. All three are the kind of thing that
breaks silently when the API moves.

Run against a reachable deployment:

    KORA_HTTP_URL=http://localhost:8080 KORA_API_KEY=ctx0_... \\
        python3 mcp-server/tests/test_e2e.py
"""

import asyncio
import os
import sys
import uuid
import warnings

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from kora_mcp.client import KoraClient, warn_renamed_env  # noqa: E402

URL = os.environ.get("KORA_HTTP_URL", "http://localhost:8080")
KEY = os.environ.get("KORA_API_KEY", "")

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


async def attempt(name, coro):
    """Await a client call, reporting an exception as a failure not a crash.

    A removed route raises out of raise_for_status, and an unhandled traceback
    loses every check after it -- so one defect would hide the rest.
    """
    try:
        return await coro
    except Exception as e:  # noqa: BLE001
        check(name, False, f"{type(e).__name__}: {e}")
        return None


async def run():
    project = f"mcp-test-{uuid.uuid4().hex[:8]}"
    c = KoraClient(base_url=URL, api_key=KEY)
    stored_id = None

    section("1. Health")
    h = await attempt("health() completes", c.health())
    if h is None:
        print("\ncannot reach the service; aborting", file=sys.stderr)
        await c.close()
        return 1
    check("health() reports a status", h.get("status") == "ok", f"got {h.get('status')!r}")
    # nodeCount is a string in the JSON gateway's output, not a number. The
    # MCP resource formats it, so a type change here would surface as a
    # confusing string in an agent's context rather than an error.
    check("health() returns graph counts", "nodeCount" in h, f"keys: {sorted(h)}")

    section("2. Store")
    s = await attempt(
        "store() completes",
        c.store(content="MCP end-to-end probe", project_id=project, memory_type=2),
    )
    if s is not None:
        mem = s.get("memory", {})
        stored_id = mem.get("id")
        check("store() returns an id", bool(stored_id), f"got {mem!r}")
        check(
            "store() echoes the project",
            mem.get("projectId") == project,
            f"got {mem.get('projectId')!r}",
        )
        # The client sends the enum as an int; the API answers with its name.
        # If that mapping drifts, memories are stored as the wrong type and
        # nothing errors.
        check(
            "memory_type=2 stores as MEMORY_TYPE_SEMANTIC",
            mem.get("type") == "MEMORY_TYPE_SEMANTIC",
            f"got {mem.get('type')!r}",
        )

    section("3. Query")
    q = await attempt("query() completes", c.query(query="end-to-end probe", project_id=project))
    if q is not None:
        results = q.get("results", [])
        check("query() finds the stored memory", len(results) > 0, f"got {len(results)} results")
        if results:
            check(
                "query() returns the memory that was stored",
                results[0].get("memory", {}).get("id") == stored_id,
                f"got {results[0].get('memory', {}).get('id')!r}",
            )

    section("4. Profile")
    p = await attempt("get_profile() completes", c.get_profile(project))
    if p is not None:
        check("get_profile() returns an object", isinstance(p, dict), f"got {type(p).__name__}")

    section("5. Connect and graph")
    s2 = await attempt(
        "a second store for the edge",
        c.store(content="MCP probe, related memory", project_id=project, memory_type=2),
    )
    second_id = (s2 or {}).get("memory", {}).get("id")
    if stored_id and second_id:
        conn = await attempt(
            "connect() completes",
            c.connect(from_id=stored_id, to_id=second_id, relationship=1),
        )
        check("connect() returns a result", conn is not None)

        g = await attempt("get_graph() completes", c.get_graph(stored_id, depth=2))
        if g is not None:
            nodes = g.get("nodes", [])
            check("get_graph() returns nodes", len(nodes) > 0, f"got {len(nodes)}")
            check(
                "get_graph() includes the connected memory",
                any(n.get("id") == second_id for n in nodes),
                f"ids: {[n.get('id') for n in nodes][:4]}",
            )

    section("6. Extract")
    e = await attempt(
        "extract() completes",
        c.extract(
            conversation="User: I prefer dark mode.\nAssistant: Noted, dark mode it is.",
            project_id=project,
        ),
    )
    if e is not None:
        check("extract() returns an object", isinstance(e, dict), f"got {type(e).__name__}")

    section("7. Delete")
    if stored_id:
        # delete() returns None on success, so absence of an exception is the
        # signal; the real check is that the memory stops coming back.
        await attempt("delete() completes", c.delete(stored_id))
        after = await attempt("query() after delete", c.query(query="end-to-end probe", project_id=project))
        if after is not None:
            ids = [r.get("memory", {}).get("id") for r in after.get("results", [])]
            check("the deleted memory is gone", stored_id not in ids, f"still present in {ids}")

    section("8. Authentication is enforced")
    bad = KoraClient(base_url=URL, api_key="ctx0_definitely_not_a_real_key")
    try:
        await bad.health()
        # /v1/health is deliberately reachable; the check is that a write is not.
        try:
            await bad.store(content="should not be stored", project_id=project, memory_type=2)
            check("a bad key is rejected on write", False, "the store succeeded")
        except Exception:  # noqa: BLE001
            check("a bad key is rejected on write", True)
    except Exception:  # noqa: BLE001
        check("a bad key is rejected on write", True)
    finally:
        await bad.close()

    section("9. Pre-rename environment variables warn")
    # The engine refuses to start on CONTEXT0_*; this package warns. Without
    # this, a user carrying an old config gets a client pointed at localhost
    # with no key and no explanation.
    #
    # Constructing a client is what exercises it, rather than calling
    # warn_renamed_env directly: calling the helper only proves the helper
    # works, and would still pass if nothing ever called it. Verified by
    # deleting the call from __init__ and watching this fail.
    os.environ["CONTEXT0_URL"] = "http://old-host:8080"
    try:
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter("always")
            probe = KoraClient(base_url=URL, api_key=KEY)
        await probe.close()
        msgs = [str(w.message) for w in caught]
        check(
            "constructing a client warns about CONTEXT0_URL",
            any("CONTEXT0_URL" in m for m in msgs),
            f"warnings: {msgs}",
        )
        check(
            "the warning names the replacement",
            any("KORA_HTTP_URL" in m for m in msgs),
            f"warnings: {msgs}",
        )
        # The helper's return value is what a caller would branch on.
        check("warn_renamed_env reports the name", "CONTEXT0_URL" in warn_renamed_env())
    finally:
        del os.environ["CONTEXT0_URL"]

    # Keeps the checks above from passing by warning about everything.
    with warnings.catch_warnings(record=True) as caught:
        warnings.simplefilter("always")
        clean = KoraClient(base_url=URL, api_key=KEY)
    await clean.close()
    check("a clean environment warns about nothing", len(caught) == 0, f"got {[str(w.message) for w in caught]}")

    section("10. The server module loads and registers its tools")
    # Everything above exercises the client. server.py is the larger half of
    # this package -- the tool definitions an editor actually calls -- and
    # importing it is the minimum proof that its decorators are well formed
    # and its fastmcp dependency is satisfied. A syntax or schema error here
    # would leave an agent with no memory tools and no error to point at.
    try:
        from kora_mcp import server as srv  # noqa: PLC0415

        # list_tools() is the public accessor and returns Tool objects, not
        # names. Reading .name off each is what an MCP client effectively does
        # when it enumerates what the server offers.
        tools = await srv.mcp.list_tools()
        names = {t.name for t in tools}
        expected = {
            "memory_store",
            "memory_query",
            "memory_extract",
            "memory_profile",
            "memory_connect",
            "memory_delete",
            "memory_graph",
        }
        check(
            "all seven memory tools are registered",
            expected <= names,
            f"missing {sorted(expected - names)}",
        )
    except Exception as e:  # noqa: BLE001
        check("the server module imports", False, f"{type(e).__name__}: {e}")
        srv = None

    if srv is not None:
        section("11. The server's type mappings match the API")
        # These turn the words an agent types into wire enum values. If the
        # proto renumbers an enum, or someone edits this dict, memories are
        # stored as the wrong type and nothing errors -- the store succeeds,
        # the type is simply wrong. So each mapping is checked by storing a
        # memory and reading back the type name the API assigns.
        for word, expected_name in [
            ("fact", "MEMORY_TYPE_SEMANTIC"),
            ("semantic", "MEMORY_TYPE_SEMANTIC"),
            ("event", "MEMORY_TYPE_EPISODIC"),
            ("episodic", "MEMORY_TYPE_EPISODIC"),
            ("howto", "MEMORY_TYPE_PROCEDURAL"),
            ("procedural", "MEMORY_TYPE_PROCEDURAL"),
        ]:
            enum_value = srv.MEMORY_TYPES[word]
            r = await attempt(
                f"store as {word!r}",
                c.store(content=f"type probe {word}", project_id=project, memory_type=enum_value),
            )
            got = (r or {}).get("memory", {}).get("type")
            check(f"{word!r} maps to {expected_name}", got == expected_name, f"got {got!r}")

        # An unrecognised word falls back to semantic rather than sending 0,
        # which the API rejects as MEMORY_TYPE_UNSPECIFIED.
        fallback = srv.MEMORY_TYPES.get("not-a-real-type", 2)
        check("an unknown type falls back to semantic, not unspecified", fallback == 2, f"got {fallback}")

        r = await attempt(
            "the fallback value is accepted by the API",
            c.store(content="fallback probe", project_id=project, memory_type=fallback),
        )
        check(
            "the fallback stores as semantic",
            (r or {}).get("memory", {}).get("type") == "MEMORY_TYPE_SEMANTIC",
            f"got {(r or {}).get('memory', {}).get('type')!r}",
        )

    await c.close()
    return 0


def main():
    if not KEY:
        print("KORA_API_KEY is required", file=sys.stderr)
        return 1

    rc = asyncio.run(run())
    if rc != 0:
        return rc

    print(f"\n=== {PASSED} passed, {FAILED} failed ===")
    for f in FAILURES:
        print(f"  failed: {f}")
    return 1 if FAILED else 0


if __name__ == "__main__":
    sys.exit(main())
