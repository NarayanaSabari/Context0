# Context0 MCP Server

MCP server that gives any AI agent persistent memory via the Context0 engine.

Works with **Claude Code, Cursor, Windsurf, Cline**, and any MCP-compatible client.

## Setup

### Prerequisites

- Context0 engine running (via Docker Compose or K8s)
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
    "context0": {
      "command": "context0-mcp",
      "env": {
        "CONTEXT0_URL": "http://localhost:8080",
        "CONTEXT0_API_KEY": "ctx0_dev_key_1",
        "CONTEXT0_PROJECT": "my-project"
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
    "context0": {
      "command": "context0-mcp",
      "env": {
        "CONTEXT0_URL": "http://localhost:8080",
        "CONTEXT0_API_KEY": "ctx0_dev_key_1"
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
| `CONTEXT0_URL` | `http://localhost:8080` | Context0 REST API URL |
| `CONTEXT0_API_KEY` | (empty) | API key for authentication |
| `CONTEXT0_PROJECT` | `default` | Default project ID |

## Run Standalone

```bash
# stdio (for Claude Code, Cursor)
context0-mcp

# HTTP transport (for remote clients)
context0-mcp --transport http --port 8000
```

## Development

```bash
# Install with dev deps
pip install -e ".[dev]"

# Test the server
fastmcp dev context0_mcp/server.py
```
