# Concepts

Four ideas cover most of how kora thinks about memory. They are stable even while the API is not, so they are worth reading before the [API reference](api.md).

## Memory

A memory is one thing worth remembering, stored as a node in a graph. It carries the text itself, the project it belongs to, free-form tags, when it was created, how often it has been retrieved, and a decay score.

Memories are typed, and the type is not decoration: it decides how the memory ages and how it is ranked.

| Type | Enum value | What it holds | Example |
| --- | --- | --- | --- |
| Semantic | `2` | Durable facts and preferences | "The API is written in Go" |
| Episodic | `1` | Something that happened, tied to a moment | "We debugged the connection pool on Tuesday" |
| Procedural | `3` | Learned processes and how-to knowledge | "Deploys go out via `make deploy` after CI is green" |

The REST API takes these as integers, because the gateway maps the protobuf enum directly. The [SDK and CLI](clients.md) take the names.

A semantic fact should still be true in six months. An episodic memory is expected to matter less as it recedes. Storing "we switched to PostgreSQL last week" as semantic rather than episodic is the most common modelling mistake, and it makes the memory age wrongly.

## Relationship

A relationship is a directed, weighted edge between two memories. Three types exist:

| Type | Enum value | Meaning |
| --- | --- | --- |
| `RELATES_TO` | `1` | A general semantic association |
| `SUPERSEDES` | `2` | The source replaces or updates the target |
| `CAUSED_BY` | `3` | The source happened as a consequence of the target |

The weight, between 0 and 1, is the strength of the connection, and traversal prefers stronger edges.

Edges are created two ways: automatically, by extraction, when a conversation implies one; and explicitly, through `/v1/memories/connect`, when your application knows something the text does not say.

## Supersede

This is the idea that most distinguishes kora from a vector store, so it is worth being precise about.

When new information contradicts old, the obvious move is to overwrite. kora does not. The old memory stays, and an edge is drawn from the new one saying it supersedes the old:

```
"We use MySQL"  <--supersedes--  "We migrated to PostgreSQL"
   (superseded)                        (current)
```

Queries return the current memory. The superseded one stays in the graph, which means:

- **The current answer is unambiguous.** An agent asking "what database?" gets one answer, not two contradictory ones ranked by cosine similarity.
- **The history is queryable.** You can still ask what was true in March, or how a decision changed, which is impossible once a row has been updated in place.
- **Nothing is silently lost.** A wrong supersede can be inspected and corrected. A wrong overwrite is gone.

The cost is that the graph grows, which is what [consolidation](operations.md) exists to manage.

## Profile

A profile is the aggregate view of a project or user, assembled from its memories rather than stored separately:

- **Static profile**: facts and preferences, the stable knowledge that holds across sessions.
- **Dynamic profile**: recent events, defaulting to the last 7 days.

This split matters in practice. The static half belongs in an agent's system prompt, where it is worth the tokens on every request. The dynamic half is what makes the agent aware of what just happened without re-reading the whole history.

## How retrieval works

A query is not a vector search with extra steps. Three signals are combined:

1. **Vector similarity** between the query and each memory's embedding, which is what finds "what database" matching "Project uses PostgreSQL 18".
2. **Graph proximity**, so a memory one hop from a strong match surfaces even if its own text matches poorly. This is what a flat vector store cannot do.
3. **Decay**, the freshness score, so a stale memory ranks below a live one of equal relevance.

Results come back as ranked memories, each with the edges that connect it to its neighbours. That last part is what lets an agent explain its recall rather than assert it.

## Next

- [API reference](api.md) - the endpoints these ideas map onto
- [Architecture](architecture.md) - how the retrieval path is actually implemented
