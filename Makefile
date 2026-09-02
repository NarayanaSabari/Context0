.PHONY: build test test-integration test-golden test-golden-quality postgres-up lint clean docker-build proto-gen run kind-up kind-down deploy eval eval-db-up eval-db-down eval-fixtures demo demo-test

# Compose refuses to start without credentials, so .env always holds a
# generated password. The integration targets below have to use that same
# value: they were written with a literal "kora", which only ever worked on a
# machine whose .env happened to contain it.
POSTGRES_PASSWORD := $(shell grep -E '^POSTGRES_PASSWORD=' .env 2>/dev/null | cut -d= -f2-)
TEST_DATABASE_URL := postgres://kora:$(POSTGRES_PASSWORD)@localhost:5432/kora?sslmode=disable

# Variables
BINARY_SERVER = bin/kora-server
BINARY_CONSOLIDATE = bin/kora-consolidate
BINARY_CLI = bin/kora
DOCKER_IMAGE = kora/kora
DOCKER_TAG = dev
KIND_CLUSTER = kora-dev

# Build
build:
	go build -o $(BINARY_SERVER) ./cmd/server
	go build -o $(BINARY_CONSOLIDATE) ./cmd/consolidate
	go build -o $(BINARY_CLI) ./cmd/cli

# Test
test:
	go test ./... -v -race -cover

# Integration tests for the graph repository. These run real Cypher against a
# real Apache AGE instance, which is the only way to catch queries that compile
# in Go but are malformed openCypher. Skipped by `make test`, which stays
# hermetic.
test-integration: postgres-up
	KORA_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
		go test ./internal/graph/... -count=1 -race -v

# The retrieval regression suite: a fixed corpus, fixed queries, and committed
# floors on recall@10 and MRR. Run it before and after any change to ranking,
# extraction, or the graph. Unlike the benchmark, it needs no credentials and
# no network, and it is deterministic.
test-golden: postgres-up
	KORA_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
		go test ./test/golden/... -count=1 -race -v

# The opt-in quality tier of the golden suite: the same corpus and cases, run
# against a real embedding model, with its own higher floors.
#
# It answers what the gated suite cannot -- whether the vector retriever works
# at all. Verified by deleting vector retrieval: the offline suite stays green,
# this one fails on four assertions.
#
# Needs its own database. The embeddings column is created at the width first
# seen, and nomic-embed-text is 768 where the default is 384, so pointing this
# at the compose database would fail on dimension rather than on retrieval.
#
#   docker run -d --name kora-ollama -p 11435:11434 ollama/ollama
#   docker exec kora-ollama ollama pull nomic-embed-text
#   docker run -d --name kora-golden-pg -e POSTGRES_DB=kora -e POSTGRES_USER=kora \
#     -e POSTGRES_PASSWORD=golden -p 55437:5432 <the postgres-age-vector image>
GOLDEN_QUALITY_DSN ?= postgres://kora:golden@localhost:55437/kora?sslmode=disable
GOLDEN_OLLAMA_URL ?= http://localhost:11435

test-golden-quality:
	@curl -sf $(GOLDEN_OLLAMA_URL)/api/tags >/dev/null || \
		{ echo "no Ollama at $(GOLDEN_OLLAMA_URL); see the comment above this target"; exit 1; }
	KORA_TEST_DATABASE_URL="$(GOLDEN_QUALITY_DSN)" \
		KORA_TEST_EMBEDDING_PROVIDER=ollama \
		KORA_TEST_EMBEDDING_MODEL=nomic-embed-text \
		KORA_TEST_EMBEDDING_BASE_URL=$(GOLDEN_OLLAMA_URL) \
		KORA_TEST_EMBEDDING_DIM=768 \
		go test ./test/golden/... -count=1 -v

# The offline retrieval evaluation. See eval/README.md.
#
# Deterministic and network-free: query and corpus vectors come from the
# committed fixture, the clock is fixed, and the database is recreated empty
# for every run. Two consecutive runs print the same numbers and the same
# digest; if they do not, that is a bug in the engine, not noise.
#
# Needs the LoCoMo dataset in eval/data (CC BY-NC 4.0, so fetched rather than
# committed): `make eval-fixtures` downloads it once. The default corpus is
# the verbatim turns; EVAL_ARGS selects others, e.g.
#   make eval EVAL_ARGS="-corpus extracted"
#   make eval EVAL_ARGS="-embedder bow"
#   make eval EVAL_ARGS="-graph-signals off"
EVAL_ARGS ?=

eval:
	KORA_EVAL_DATABASE_URL="$$(scripts/eval_db.sh up)" go run ./cmd/eval run $(EVAL_ARGS)

eval-db-up:
	@scripts/eval_db.sh up >/dev/null

eval-db-down:
	@scripts/eval_db.sh down

# One-time fixture build. Downloads the dataset if absent and embeds every
# question and turn through the engine's own Ollama client. The only target
# in this file that talks to a model server.
EVAL_OLLAMA_URL ?= http://localhost:11434
eval-fixtures:
	go run ./cmd/eval fixtures -ollama $(EVAL_OLLAMA_URL)

postgres-up:
	docker compose up -d postgres
	@until docker compose exec -T postgres pg_isready -U kora >/dev/null 2>&1; do sleep 1; done

test-coverage:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -html=coverage.out -o coverage.html

# Lint
lint:
	golangci-lint run ./...

# Proto generation
proto-gen:
	@mkdir -p api/gen/kora/v1
	PATH="$(HOME)/go/bin:$(PATH)" protoc \
		--go_out=api/gen --go_opt=paths=source_relative \
		--go-grpc_out=api/gen --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=api/gen --grpc-gateway_opt=paths=source_relative \
		-I api/proto \
		api/proto/kora/v1/memory.proto \
		api/proto/kora/v1/session.proto \
		api/proto/kora/v1/health.proto

# Docker
docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Run locally (requires PostgreSQL + AGE running)
run:
	go run ./cmd/server

# Kind cluster management
kind-up:
	kind create cluster --name $(KIND_CLUSTER) --config deploy/kind-config.yaml
	@echo "Kind cluster '$(KIND_CLUSTER)' created"

kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

kind-load: docker-build
	kind load docker-image $(DOCKER_IMAGE):$(DOCKER_TAG) --name $(KIND_CLUSTER)

# Local dev credentials.
#
# The chart ships no default password or API key -- a default in a public chart
# is a published credential -- so a local deployment has to generate its own.
# They are written to .dev-credentials (gitignored) and reused across deploys,
# because regenerating the Postgres password on every `make deploy` would leave
# it disagreeing with the one already initialised in the PVC.
DEV_CREDS := .dev-credentials

$(DEV_CREDS):
	@umask 077 && { \
	  echo "# Generated by make. Local kind cluster only. Never commit."; \
	  echo "DEV_PG_PASSWORD=$$(openssl rand -hex 16)"; \
	  echo "DEV_API_KEY=ctx0_$$(openssl rand -hex 6)_$$(openssl rand -hex 24)"; \
	} > $@
	@echo "Generated $(DEV_CREDS)"

.PHONY: dev-credentials
dev-credentials: $(DEV_CREDS)
	@cat $(DEV_CREDS)

# Deploy to kind cluster
deploy: kind-load $(DEV_CREDS)
	@. ./$(DEV_CREDS) && \
	helm upgrade --install kora ./charts/kora -n kora --create-namespace \
	  --set postgres.password="$$DEV_PG_PASSWORD" \
	  --set auth.apiKeys="$$DEV_API_KEY"
	@echo "Kora deployed to kind cluster"
	@echo "API key: $$(. ./$(DEV_CREDS) && echo $$DEV_API_KEY)"

undeploy:
	helm uninstall kora -n kora

# Clean
clean:
	rm -rf bin/ coverage.out coverage.html api/gen/

# Help
help:
	@echo "Available targets:"
	@echo "  build          - Build all binaries"
	@echo "  test           - Run all tests"
	@echo "  lint           - Run linter"
	@echo "  proto-gen      - Generate Go code from proto files"
	@echo "  docker-build   - Build Docker image"
	@echo "  run            - Run server locally"
	@echo "  kind-up        - Create kind cluster"
	@echo "  kind-down      - Delete kind cluster"
	@echo "  deploy         - Deploy to kind cluster"
	@echo "  eval           - Offline retrieval evaluation (see eval/README.md)"
	@echo "  demo           - Run the receivables-chaser example (see examples/receivables-chaser)"
	@echo "  demo-test      - Run the receivables-chaser test suite"
	@echo "  clean          - Remove build artifacts"

# ── Receivables chaser example ──────────────────────────────────────────
#
# A judge should be able to run this cold: it works standalone (NullMemory,
# no setup) and picks up a running Kora automatically if KORA_API_KEY is
# already exported, so one target covers both "does it run" and "is it
# actually using memory". See examples/receivables-chaser/README.md.
demo:
	cd examples/receivables-chaser && \
		KORA_URL=$${KORA_URL:-http://localhost:8080} python3 -m chaser run --recorded --days 21

demo-test:
	cd examples/receivables-chaser && python3 -m pytest -q
