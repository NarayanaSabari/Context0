# Integrate Kora with an agent

Kora sits beside an agent as its long-term memory.
The agent remains responsible for deciding what to do, while Kora stores what happened and retrieves the history that matters to the next decision.

The useful integration is a loop:

```text
start a run -> recall relevant memory -> decide and act -> store the outcome
```

Use the MCP server when the agent already supports MCP.
Use the Python SDK inside a Python application.
Use the REST API from any other language or runtime.

## 1. Choose the project boundary

Every store and query belongs to a Kora project.
Queries never cross projects, but a project is an organisation boundary rather than an authorization boundary.
Every API key in one Kora deployment can read every project in that deployment.

Choose the narrowest scope whose memories are normally recalled together.

| Agent | A useful project ID |
| --- | --- |
| Personal assistant | `assistant-user-<user-id>` |
| Support agent | `support-account-<account-id>` |
| Coding agent | `code-<repository-id>` |
| Receivables agent | `receivables-<merchant-id>-<customer-id>` |

Do not put unrelated users or customers into one large project and rely on `top_k` to separate them.
Relevant memories can be pushed out of the returned window by another subject's history.

If different customers must be security-isolated, give them separate Kora deployments or enforce that boundary in a trusted gateway.
See [Concepts](concepts.md) for the distinction between a deployment and a project.

## 2. Recall before deciding

Ask for the history needed by the current decision, not for every memory in the project.

```python
from kora import KoraClient

memory = KoraClient(
    endpoint="localhost:50051",
    api_key="ctx0_...",
    project="support-account-acme",
)

results = memory.query(
    "What has this account reported about duplicate charges?",
    top_k=8,
)

context = [result.memory.content for result in results]
```

Give `context` to the policy or model that makes the next decision.
Keep source-system facts in their source system: an invoice's paid status belongs in the payment provider, while a customer's promise to pay belongs in Kora.

Each query result also includes a score and relationship context.
Use those fields when the agent needs to explain why a memory was recalled.

## 3. Store outcomes after acting

Write concise statements that will make sense when read without the current prompt.
Include stable identifiers and dates when the memory describes an event.

```python
memory.store(
    "On 2026-09-05, Acme disputed invoice inv_42 because the amount was incorrect.",
    type="episodic",
    tags=["invoice", "dispute", "inv_42"],
)
```

Use the three memory types deliberately:

| Type | Store it for |
| --- | --- |
| `episodic` | Something that happened, with a date or run context |
| `semantic` | A stable fact or current state |
| `procedural` | How the agent or organisation normally works |

For conversational agents, send the transcript to `POST /v1/memories/extract` after the exchange instead of inventing a separate extraction prompt.
Kora can then create memories and relationships, including a `SUPERSEDES` edge when new information replaces an older belief.

## 4. Group work into sessions

Sessions are optional, but they make it possible to trace memories to one continuous agent run.

```python
with memory.session(agent_id="support-agent") as session:
    memory.store(
        "Reviewed duplicate-charge report for ticket tkt_19.",
        type="episodic",
        session_id=session.id,
    )
```

Start one session per conversation, job, or scheduled run.
Do not use a session as the long-term memory boundary; the project provides that boundary.

## Connect an MCP agent

Clone Kora and install its MCP server into the Python environment used by the agent:

```bash
pip install ./mcp-server
```

Add this server to the agent's MCP configuration:

```json
{
  "mcpServers": {
    "kora": {
      "command": "kora-mcp",
      "env": {
        "KORA_HTTP_URL": "http://localhost:8080",
        "KORA_API_KEY": "<your generated key>",
        "KORA_PROJECT": "code-my-repository"
      }
    }
  }
}
```

The exact location of this JSON depends on the agent, but the server definition is the same for Claude Code, Cursor, Windsurf, Cline, and other stdio MCP clients.
The server exposes `memory_store`, `memory_query`, `memory_extract`, `memory_profile`, `memory_connect`, `memory_delete`, and `memory_graph`.

Tell the agent when to use them in its standing instructions:

```text
At the start of a task, query Kora for decisions and procedures relevant to the request.
Before changing an established design, query for earlier decisions about that component.
After completing the task, store durable decisions and user preferences, not transient logs or secrets.
Never store credentials, access tokens, private keys, or raw environment files.
```

Run `kora-mcp --transport http --port 8000` only when the client requires a remote HTTP transport.
Protect that endpoint as carefully as the Kora API because its tools can read and change memory.

## Connect a Python agent

The Python package is currently installed from the repository because it has not been published to PyPI:

```bash
pip install ./sdk/python
```

Hide Kora behind a small interface owned by the agent:

```python
from typing import Protocol

from kora import KoraClient


class AgentMemory(Protocol):
    def recall(self, question: str) -> list[str]: ...
    def remember(self, statement: str, type: str = "episodic") -> None: ...


class KoraMemory:
    def __init__(self, endpoint: str, api_key: str, project: str) -> None:
        self.client = KoraClient(endpoint=endpoint, api_key=api_key, project=project)

    def recall(self, question: str) -> list[str]:
        return [result.memory.content for result in self.client.query(question, top_k=10)]

    def remember(self, statement: str, type: str = "episodic") -> None:
        self.client.store(statement, type=type)
```

This keeps the decision loop independent of Kora's transport and makes degraded behavior explicit.
Decide whether a memory outage should stop the action, retry it, or continue without memory based on the risk of the agent's job.
A customer-contact agent should normally fail closed or hand off when it cannot prove that the customer was not already contacted.

## Connect any other agent

The REST gateway accepts JSON and an `X-API-Key` header.
An integration only needs two calls to begin:

```bash
curl "http://localhost:8080/v1/memories/query?query=customer+payment+history&project_id=receivables-acme-cust_42&top_k=10" \
  -H "X-API-Key: $KORA_API_KEY"

curl -X POST http://localhost:8080/v1/memories \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $KORA_API_KEY" \
  -d '{
    "content": "On 2026-09-05, customer cust_42 promised to pay invoice inv_42 by 2026-09-10.",
    "type": 1,
    "project_id": "receivables-acme-cust_42",
    "tags": ["promise", "inv_42"]
  }'
```

See the [API reference](api.md) for extraction, profiles, graph context, and sessions.

## Production checklist

- Query before the action whose safety depends on memory.
- Store the observed outcome after the action succeeds.
- Use stable project IDs and keep unrelated subjects in separate projects.
- Keep authoritative business state in its source system.
- Put dates and source identifiers into episodic memories.
- Set timeouts and choose an explicit failure policy.
- Do not store secrets or entire payloads when a compact durable statement is enough.
- Check `/v1/health` at startup and monitor request failures and latency.
- Use the self-hosted quality profile when semantic recall matters beyond exact word overlap.

## See a complete agent

The [Razorpay receivables agent](razorpay-agent.md) applies this loop to overdue invoices and includes a deterministic offline simulation, an audit trail, and a live Razorpay test-mode adapter.

