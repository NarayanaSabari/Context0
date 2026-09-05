# Quick start

The fastest path to a running kora with a memory in it. Docker Compose, one machine, no Kubernetes.

For a real deployment, see [Installation](installation.md).

## Prerequisites

- Docker and Docker Compose
- Go 1.26, only to generate an API key offline

## 1. Generate credentials

kora ships no default password and no default API key, and refuses to start without them. A default credential in a public repository is a published credential, shared by every install that never changed it.

```bash
git clone https://github.com/NarayanaSabari/Kora.git
cd Kora

cat > .env <<EOF
POSTGRES_PASSWORD=$(openssl rand -hex 16)
KORA_API_KEYS=$(go run ./cmd/cli keys generate)
EOF
```

`.env` is gitignored. The server stores only a hash of the key, so this file is the only copy: keep it.

## 2. Start it

```bash
docker compose up
```

That brings up PostgreSQL with Apache AGE and pgvector, the kora API, and the web UI. The API is on `http://localhost:8080` (REST) and `localhost:50051` (gRPC); the UI is on `http://localhost:3000`.

If something already holds those ports:

```bash
echo "API_HTTP_PORT=18080" >> .env   # default 8080
echo "WEB_PORT=13000"      >> .env   # default 3000
```

## 3. Check it is healthy

```bash
curl http://localhost:8080/v1/health
```

Health needs no API key, along with `/livez`, `/readyz`, `/startupz`, and `/metrics`: a probe has to answer before any credential exists. Note that `/v1/health` reports graph totals, so it is the one public endpoint that discloses anything at all, and only counts. Keep it off the public internet.

Everything below does need the key:

```bash
export API_KEY=$(grep KORA_API_KEYS .env | cut -d= -f2)
```

## 4. Store a memory

```bash
curl -X POST http://localhost:8080/v1/memories \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{
    "content": "Project uses PostgreSQL 18 with Apache AGE",
    "type": 2,
    "project_id": "my-project",
    "tags": ["database", "postgres"]
  }'
```

`"type": 2` is `MEMORY_TYPE_SEMANTIC`, a durable fact. The other types are in [Concepts](concepts.md).

## 5. Let it extract memories for you

Storing memories by hand does not scale past a demo. The point of kora is that you hand it a raw conversation and it decides what was worth keeping:

```bash
curl -X POST http://localhost:8080/v1/memories/extract \
  -H "Content-Type: application/json" \
  -H "X-API-Key: $API_KEY" \
  -d '{
    "conversation": "user: We use PostgreSQL for the database\nuser: I prefer Go for backend services\nuser: We switched from MySQL last week",
    "project_id": "my-project"
  }'
```

The response holds the memories it created and a count of the relationships it drew between them and the existing graph. That third sentence is the interesting one: it produces an edge saying the PostgreSQL memory *supersedes* the MySQL one, rather than leaving two contradictory facts side by side.

## 6. Query by meaning

```bash
curl "http://localhost:8080/v1/memories/query?query=what+database&project_id=my-project&top_k=5" \
  -H "X-API-Key: $API_KEY"
```

Note that "what database" matches nothing lexically: no stored memory contains that phrase. Results come back ranked by a combination of vector similarity, graph proximity, and decay, each with the edges explaining why it surfaced.

## 7. Read the profile

```bash
curl "http://localhost:8080/v1/profiles/my-project" \
  -H "X-API-Key: $API_KEY"
```

A profile is the aggregate view: the static half is stable facts and preferences, the dynamic half is what has happened recently. This is the endpoint to put in an agent's system prompt.

## Next

- [Agent integration](agent-integration.md) - where recall and store belong in an agent loop
- [Razorpay agent](razorpay-agent.md) - a complete, measurable agent built on Kora
- [Concepts](concepts.md) - what the types and edges mean, and why superseding matters
- [Clients](clients.md) - the Python SDK and the CLI, instead of curl
- [Installation](installation.md) - Helm, kind, and a deployment that survives a restart
