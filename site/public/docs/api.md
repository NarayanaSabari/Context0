# API reference

kora speaks gRPC, with a REST gateway generated from the same protobuf definitions. Both surfaces expose identical behaviour: REST is grpc-gateway in front of the gRPC service, not a separate implementation.

- **gRPC**: port `50051`
- **REST**: port `8080`
- **Source of truth**: [`api/proto/kora/v1`](https://github.com/NarayanaSabari/Kora/tree/main/api/proto/kora/v1)

> The API is pre-1.0 and may change. The proto files are the definitive reference; this page is the readable version of them.

## Authentication

Every endpoint except the probes takes an API key in the `X-API-Key` header:

```bash
curl http://localhost:8080/v1/memories/query?query=go \
  -H "X-API-Key: $API_KEY"
```

Public, no key required: `/livez`, `/readyz`, `/startupz`, `/metrics`, and `/v1/health`. Health reports graph totals, so it is the one public endpoint that discloses anything; keep the service off the public internet.

Requests are rate limited per key, per replica. See [Configuration](configuration.md).

## Endpoints

| Method | Path | Description |
| --- | --- | --- |
| POST | `/v1/memories` | Store a memory |
| POST | `/v1/memories/extract` | Extract memories from a conversation |
| GET | `/v1/memories/query` | Query memories by meaning |
| POST | `/v1/memories/connect` | Create a relationship between two memories |
| DELETE | `/v1/memories/{id}` | Delete a memory and its edges |
| GET | `/v1/memories/{center_id}/graph` | Get the subgraph around a memory |
| GET | `/v1/profiles/{project_id}` | Get an aggregated profile |
| POST | `/v1/sessions` | Start a session |
| POST | `/v1/sessions/{id}/end` | End a session |
| GET | `/v1/health` | Health, version, and graph counts |
| GET | `/metrics` | Prometheus metrics |

## Store

`POST /v1/memories`

```json
{
  "content": "Project uses PostgreSQL 18 with Apache AGE",
  "type": 2,
  "project_id": "my-project",
  "tags": ["database", "postgres"],
  "session_id": "optional-session-uuid"
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `content` | string | The text of the memory |
| `type` | int | `1` episodic, `2` semantic, `3` procedural. See [Concepts](concepts.md) |
| `project_id` | string | Scopes the memory; queries never cross projects |
| `tags` | string[] | Optional labels for filtering |
| `session_id` | string | Optional, links the memory to a session |

Returns the created `Memory`, including its `id`, `created_at`, `access_count`, and `decay_score`.

## Extract

`POST /v1/memories/extract`

Hand it raw conversation text and it decides what to keep.

```json
{
  "conversation": "user: We use PostgreSQL\nuser: I prefer Go\nuser: We switched from MySQL last week",
  "project_id": "my-project",
  "session_id": "optional-session-uuid"
}
```

Returns the memories it created and `relationships_created`, the number of edges drawn between them and the existing graph. This is where superseding happens automatically: a statement that contradicts a stored memory produces a `SUPERSEDES` edge rather than a second, conflicting fact.

## Query

`GET /v1/memories/query`

| Parameter | Type | Notes |
| --- | --- | --- |
| `query` | string | Natural language; matched by meaning, not keyword |
| `project_id` | string | Which project to search |
| `top_k` | int | Maximum results |
| `max_depth` | int | Graph traversal depth for context edges; `0` disables traversal |
| `types` | int[] | Optional filter by memory type |

```bash
curl "http://localhost:8080/v1/memories/query?query=what+database&project_id=my-project&top_k=5&max_depth=2" \
  -H "X-API-Key: $API_KEY"
```

Each result is a `MemoryWithContext`: the memory, a composite `score` combining vector similarity, decay, and graph proximity, and a `context` array of edges. Each edge carries its `relationship`, the `target_id`, the `target_content` (inlined so you do not need a second round trip), and a `weight`.

That `context` array is the part worth using. It is what lets an agent say *why* it recalled something.

## Connect

`POST /v1/memories/connect`

For relationships your application knows about but the text does not state.

```json
{
  "from_id": "uuid-of-source",
  "to_id": "uuid-of-target",
  "relationship": 2,
  "weight": 0.9
}
```

`relationship` is `1` relates-to, `2` supersedes, `3` caused-by. `weight` runs 0 to 1 and biases traversal.

## Get graph

`GET /v1/memories/{center_id}/graph?depth=2`

Returns every node within `depth` hops of the centre, and the edges among them. This is what the web UI draws.

## Delete

`DELETE /v1/memories/{id}`

Removes the memory and all its edges. Note this is a real delete, not a supersede: use it for something stored in error, not for something that stopped being true. For that, connect a `SUPERSEDES` edge instead and keep the history.

## Profile

`GET /v1/profiles/{project_id}`

Returns the static profile (stable facts and preferences) and the dynamic profile (recent events, last 7 days by default).

## Sessions

`POST /v1/sessions` with `project_id` and `agent_id` opens a session and returns its `id`. `POST /v1/sessions/{id}/end` closes it and returns the session with `ended_at` set.

Sessions group memories by a single stretch of agent activity. They are optional: every memory endpoint works without one.

## Health

`GET /v1/health` returns `status`, `version`, `node_count`, and `edge_count`.

## Next

- [Clients](clients.md) - the Python SDK and CLI, which wrap all of this
- [Concepts](concepts.md) - what the enum values mean
