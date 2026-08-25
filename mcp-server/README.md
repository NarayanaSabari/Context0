# Kora MCP Server

MCP server that gives any AI agent persistent memory via the Kora engine.

Works with **Claude Code, Cursor, Windsurf, Cline**, and any MCP-compatible client.

## Setup

### Prerequisites

- Kora engine running (via Docker Compose or K8s)
- Python 3.10+

### Install

```bash
cd mcp-server
pip install -e .
```

### Configure Claude Code

Add to `~/.claude.json`:

```json
{
  "mcpServers": {
    "kora": {
      "command": "kora-mcp",
      "env": {
        "KORA_HTTP_URL": "http://localhost:8080",
        "KORA_API_KEY": "<your key: go run ./cmd/cli keys generate>",
        "KORA_PROJECT": "my-project"
      }
    }
  }
}
```

### Configure Cursor

Add to Cursor settings (MCP section):

```json
{
  "mcpServers": {
    "kora": {
      "command": "kora-mcp",
      "env": {
        "KORA_HTTP_URL": "http://localhost:8080",
        "KORA_API_KEY": "<your key: go run ./cmd/cli keys generate>"
      }
    }
  }
}
```

## Available Tools

| Tool | Description |
|------|-------------|
| `memory_store` | Store a fact, event, or procedure |
| `memory_query` | Search memories by natural language |
| `memory_extract` | Auto-extract memories from a conversation |
| `memory_profile` | Get aggregated user/project profile |
| `memory_connect` | Create relationship between memories |
| `memory_delete` | Delete a memory |
| `memory_graph` | View subgraph around a memory |

## Available Resources

| Resource | Description |
|----------|-------------|
| `memory://health` | Engine health and statistics |
| `memory://profile/{project_id}` | User/project profile |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KORA_HTTP_URL` | `http://localhost:8080` | Kora REST API URL |
| `KORA_API_KEY` | (empty) | API key for authentication |
| `KORA_PROJECT` | `default` | Default project ID |

## Run Standalone

```bash
# stdio (for Claude Code, Cursor)
kora-mcp

# HTTP transport (for remote clients)
kora-mcp --transport http --port 8000
```

## Development

```bash
# Install with dev deps
pip install -e ".[dev]"

# Test the server interactively
fastmcp dev kora_mcp/server.py
```

### Tests

The suite runs against a real engine rather than mocks, because what breaks
here is the boundary: a route that moved, a response field that was renamed,
or the memory-type mapping drifting from the API's enum. None of that is
visible to a mocked test.

```bash
# With an engine reachable (docker compose up, or make deploy)
KORA_HTTP_URL=http://localhost:8080 \
KORA_API_KEY="$(go run ../cmd/cli keys generate)" \
  python3 tests/test_e2e.py
```

CI runs this against the kind cluster on every push.
