#!/usr/bin/env bash
set -euo pipefail

# Context0 Demo Script
# Spins up a kind cluster, deploys Context0 + PostgreSQL/AGE, and runs E2E tests.

CLUSTER_NAME="context0-dev"
NAMESPACE="context0"
IMAGE="context0/context0:dev"

echo "=== Context0 Demo ==="
echo ""

# --- Prerequisites ---
for cmd in kind kubectl docker go; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd is required but not installed."
    exit 1
  fi
done

# --- Step 1: Create kind cluster ---
echo "[1/7] Creating kind cluster '$CLUSTER_NAME'..."
if kind get clusters 2>/dev/null | grep -q "$CLUSTER_NAME"; then
  echo "  Cluster already exists, reusing."
else
  kind create cluster --name "$CLUSTER_NAME" --config deploy/kind-config.yaml
fi

# --- Step 2: Build Docker image ---
echo "[2/7] Building Docker image..."
docker build -t "$IMAGE" .

# --- Step 3: Load image into kind ---
echo "[3/7] Loading image into kind cluster..."
kind load docker-image "$IMAGE" --name "$CLUSTER_NAME"

# --- Step 4: Deploy PostgreSQL + AGE ---
echo "[4/7] Deploying PostgreSQL + Apache AGE..."
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/postgres-age.yaml

echo "  Waiting for PostgreSQL to be ready..."
kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=postgres-age --timeout=120s

# --- Step 5: Deploy Context0 API ---
echo "[5/7] Deploying Context0 API..."
kubectl apply -f deploy/context0.yaml

echo "  Waiting for Context0 API to be ready..."
kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=context0-api --timeout=120s

# --- Step 6: Port-forward for local testing ---
echo "[6/7] Setting up port-forward..."
kubectl -n "$NAMESPACE" port-forward svc/context0-api 8080:8080 50051:50051 &
PF_PID=$!
sleep 3

# Verify health.
echo "  Checking health..."
HEALTH=$(curl -s http://localhost:8080/v1/health)
echo "  $HEALTH"

# --- Step 7: Run E2E tests ---
echo "[7/7] Running E2E tests..."
echo ""

CONTEXT0_E2E_HTTP="http://localhost:8080" \
CONTEXT0_E2E_API_KEY="ctx0_dev_key_1" \
go test ./test/e2e/... -v -tags=e2e -count=1

echo ""
echo "=== Demo Complete ==="
echo ""
echo "Context0 is running. Try:"
echo ""
echo "  # Store a memory"
echo "  curl -X POST http://localhost:8080/v1/memories \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -H 'X-API-Key: ctx0_dev_key_1' \\"
echo "    -d '{\"content\":\"Project uses Go 1.23\",\"type\":2,\"project_id\":\"demo\",\"tags\":[\"golang\"]}'"
echo ""
echo "  # Query memories"
echo "  curl 'http://localhost:8080/v1/memories/query?query=golang&project_id=demo&top_k=5' \\"
echo "    -H 'X-API-Key: ctx0_dev_key_1'"
echo ""
echo "  # Check stats"
echo "  curl http://localhost:8080/v1/health"
echo ""
echo "  # CLI tool"
echo "  go run ./cmd/cli stats"
echo "  go run ./cmd/cli store 'test memory' --type semantic --tags test"
echo "  go run ./cmd/cli query 'test'"
echo ""
echo "Press Ctrl+C to stop port-forward and exit."
wait $PF_PID
