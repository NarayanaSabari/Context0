.PHONY: build test lint clean docker-build proto-gen run kind-up kind-down deploy

# Variables
BINARY_SERVER = bin/context0-server
BINARY_CONSOLIDATE = bin/context0-consolidate
BINARY_CLI = bin/context0
DOCKER_IMAGE = context0/context0
DOCKER_TAG = dev
KIND_CLUSTER = context0-dev

# Build
build:
	go build -o $(BINARY_SERVER) ./cmd/server
	go build -o $(BINARY_CONSOLIDATE) ./cmd/consolidate
	go build -o $(BINARY_CLI) ./cmd/cli

build-server:
	go build -o $(BINARY_SERVER) ./cmd/server

# Test
test:
	go test ./... -v -race -cover

test-short:
	go test ./... -short -race

test-coverage:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -html=coverage.out -o coverage.html

# Lint
lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .
	goimports -w .

# Proto generation
proto-gen:
	@mkdir -p api/gen/context0/v1
	PATH="$(HOME)/go/bin:$(PATH)" protoc \
		--go_out=api/gen --go_opt=paths=source_relative \
		--go-grpc_out=api/gen --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=api/gen --grpc-gateway_opt=paths=source_relative \
		-I api/proto \
		api/proto/context0/v1/memory.proto \
		api/proto/context0/v1/session.proto \
		api/proto/context0/v1/health.proto

# Docker
docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push:
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

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

# Deploy to kind cluster
deploy: kind-load
	kubectl apply -f deploy/namespace.yaml
	kubectl apply -f deploy/postgres-age.yaml
	kubectl apply -f deploy/context0.yaml
	@echo "Context0 deployed to kind cluster"

undeploy:
	kubectl delete -f deploy/context0.yaml --ignore-not-found
	kubectl delete -f deploy/postgres-age.yaml --ignore-not-found
	kubectl delete -f deploy/namespace.yaml --ignore-not-found

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
	@echo "  clean          - Remove build artifacts"
