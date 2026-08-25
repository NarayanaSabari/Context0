# SDKs and CLI

Two clients ship with kora: a Python SDK and a Go CLI. Both are thin wrappers over the [API](api.md), so anything they cannot do is one HTTP request away.

## Python SDK

```bash
pip install kora
```

```python
from kora import KoraClient

client = KoraClient(
    endpoint="localhost:50051",
    api_key="ctx0_...",
    project="my-project",
)
```

The client talks to the REST gateway rather than gRPC, and derives the HTTP address from `endpoint` by swapping port 50051 for 8080. If your gateway is on a different port, pass that address directly.

Nothing is imported beyond the standard library, so the SDK adds no transitive dependencies to your agent.

### Store and query

```python
mem = client.store(
    "Project uses PostgreSQL 18",
    type="semantic",            # or "episodic", "procedural"
    tags=["database"],
)

results = client.query("what database does this project use?", top_k=3)

for r in results:
    print(f"{r.score:.3f}  {r.memory.content}")
    for edge in r.context:
        print(f"   {edge.relationship} -> {edge.target_content}")
```

`query` takes `top_k`, `max_depth` for graph traversal, and an optional `types` filter. Every result carries the edges that explain it, which is the part worth surfacing to an agent.

### Relationships

```python
edge = client.connect(
    from_id=new_memory.id,
    to_id=old_memory.id,
    relationship="supersedes",   # or "relates_to", "caused_by"
    weight=0.9,
)
```

### Sessions

The context manager is the reason to use sessions from Python: it closes the session even if the block raises.

```python
with client.session(agent_id="my-agent") as sess:
    client.store("discussed auth architecture", type="episodic", session_id=sess.id)
```

Ending a session that is already ended raises `SessionAlreadyEndedError`. The context manager swallows exactly that case on exit, on the grounds that the desired state already holds.

### Everything else

```python
graph = client.get_graph(memory_id, depth=2)
client.delete(memory_id)
status = client.health()   # status, version, node_count, edge_count
```

> The SDK does not yet wrap `/v1/memories/extract` or `/v1/profiles/{project_id}`. Call those endpoints directly until it does.

## CLI

```bash
go build -o kora ./cmd/cli
```

The CLI speaks gRPC directly, and is configured entirely by environment:

| Variable | Default | Meaning |
| --- | --- | --- |
| `KORA_ENDPOINT` | `localhost:50051` | gRPC endpoint |
| `KORA_API_KEY` | none | API key |
| `KORA_PROJECT` | `default` | Project scope |

The `CONTEXT0_*` names these carried before the rename are still recognised, and the CLI warns when it sees one rather than ignoring it. That warning matters: a leftover `CONTEXT0_ENDPOINT` would silently leave the CLI talking to localhost instead of your server, which presents as missing data rather than as a misconfiguration.

### Commands

```bash
kora store "Project uses Go 1.26" --type semantic --tags golang,backend
kora query "what language" --top-k 5 --type semantic
kora connect <from-id> <to-id> --relationship supersedes --weight 0.9
kora graph <memory-id> --depth 2
kora delete <memory-id>
kora session-start
kora session-end <session-id>
kora stats
```

`query` prints each result with its score, type, tags, and the context edges beneath it. Everything else prints JSON, so it pipes into `jq` cleanly.

### Generating a key

```bash
kora keys generate
```

This runs entirely offline and needs no server: it prints a key, and the server stores only its hash. There is no way to recover a key you lose, so capture it when it is printed.

## MCP server

For AI coding agents, Kora ships an [MCP](https://modelcontextprotocol.io) server, so Claude Code, Cursor, Windsurf, Cline, and any other MCP client can store and recall memories as tools rather than through hand-written HTTP calls.

```bash
pip install ./mcp-server
```

Then point the editor at it. In Claude Code, `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "kora": {
      "command": "kora-mcp",
      "env": {
        "KORA_HTTP_URL": "http://localhost:8080",
        "KORA_API_KEY": "<generate with: kora keys generate>",
        "KORA_PROJECT": "my-project"
      }
    }
  }
}
```

Seven tools are exposed: `memory_store`, `memory_query`, `memory_extract`, `memory_profile`, `memory_connect`, `memory_delete`, and `memory_graph`. The store tool takes memory types as words rather than enum values, so an agent can write `"fact"`, `"event"`, or `"howto"`.

Full setup, including Cursor and Windsurf, is in the [MCP server README](https://github.com/NarayanaSabari/Kora/tree/main/mcp-server).

## Anything else

There is no JavaScript or Go client library yet. The REST API is plain JSON over HTTP with a header for auth, and the [proto definitions](https://github.com/NarayanaSabari/Kora/tree/main/api/proto/kora/v1) generate a gRPC client for any language buf supports.

## Next

- [API reference](api.md) - the endpoints underneath these clients
- [Configuration](configuration.md) - what the server reads from the environment
