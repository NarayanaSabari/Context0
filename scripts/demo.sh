#!/usr/bin/env bash
set -euo pipefail

# Kora demo: spin up a kind cluster, deploy Kora + PostgreSQL/AGE via Helm,
# and run the E2E suite against it.
#
# This used to `kubectl apply` raw manifests from deploy/. Those were deleted
# in a80537f, which consolidated the Kubernetes topology onto the Helm chart --
# the script kept referencing deploy/namespace.yaml, deploy/postgres-age.yaml,
# and deploy/kora.yaml, none of which have existed since. CONTRIBUTING.md
# points new contributors here, so the documented first run failed on step 4.
#
# It also hardcoded the API key `ctx0_dev_key_1`. That key was a credential
# published in a public repo, removed from the chart in a83af5a; nothing
# accepts it now. Credentials come from `make dev-credentials`, which
# generates them locally and gitignores them.
#
# Deployment goes through `make deploy` rather than a second copy of the Helm
# invocation, so this cannot drift from the maintained path the way the
# manifests did.

CLUSTER_NAME="kora-dev"
NAMESPACE="kora"
DEV_CREDS=".dev-credentials"

echo "=== Kora Demo ==="
echo ""

for cmd in kind kubectl docker go helm; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd is required but not installed." >&2
    exit 1
  fi
done

# --- Step 1: cluster ---
echo "[1/5] Creating kind cluster '$CLUSTER_NAME'..."
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
  echo "  Cluster already exists, reusing."
else
  kind create cluster --name "$CLUSTER_NAME" --config deploy/kind-config.yaml
fi

# --- Step 2: build, load, and deploy ---
# `make deploy` depends on kind-load and dev-credentials, so it builds the
# images, loads them into the cluster, generates credentials if absent, and
# installs the chart.
echo "[2/5] Building images and deploying via Helm..."
make deploy

# --- Step 3: wait for readiness ---
echo "[3/5] Waiting for pods..."
kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=postgres-age --timeout=180s
kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=kora-api --timeout=180s

# --- Step 4: port-forward ---
echo "[4/5] Setting up port-forward..."
kubectl -n "$NAMESPACE" port-forward svc/kora-api 8080:8080 50051:50051 &
PF_PID=$!
# Stop the port-forward however this script exits, rather than leaving a
# background kubectl holding the ports after a failed run.
trap 'kill $PF_PID 2>/dev/null || true' EXIT

# Poll rather than sleeping a fixed interval: the forward is not immediately
# ready, and a fixed sleep is either too short on a cold cluster or wasted time.
for _ in $(seq 1 30); do
  if curl -sf http://localhost:8080/v1/health >/dev/null 2>&1; then break; fi
  sleep 1
done

echo "  Health: $(curl -s http://localhost:8080/v1/health)"

# --- Step 5: E2E ---
# The key is whatever `make dev-credentials` generated for this cluster.
# shellcheck disable=SC1090
. "./$DEV_CREDS"
API_KEY="$DEV_API_KEY"

echo "[5/5] Running E2E tests..."
echo ""
KORA_E2E_HTTP="http://localhost:8080" \
KORA_E2E_API_KEY="$API_KEY" \
  go test ./test/e2e/... -v -tags=e2e -count=1 -timeout=120s

cat <<EOF

=== Demo Complete ===

Kora is running. Your API key is in $DEV_CREDS (gitignored).

  export KEY=\$(. ./$DEV_CREDS && echo \$DEV_API_KEY)

  # Store a memory
  curl -X POST http://localhost:8080/v1/memories \\
    -H 'Content-Type: application/json' \\
    -H "X-API-Key: \$KEY" \\
    -d '{"content":"Project uses Go 1.26","type":"MEMORY_TYPE_SEMANTIC","project_id":"demo","tags":["golang"]}'

  # Query memories
  curl -G http://localhost:8080/v1/memories/query \\
    -H "X-API-Key: \$KEY" \\
    --data-urlencode 'project_id=demo' \\
    --data-urlencode 'query=golang'

  # CLI (project comes from KORA_PROJECT, not a flag)
  KORA_API_KEY="\$KEY" KORA_PROJECT=demo go run ./cmd/cli query 'golang'

Press Ctrl+C to stop the port-forward and exit.
EOF

wait $PF_PID
