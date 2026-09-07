#!/usr/bin/env bash

set -euo pipefail

# Quick smoke check for the documented local setup flow.
#
# This is intentionally narrow and fast. It verifies that:
# - the documented one-command setup succeeds
# - the main pods become ready
# - API credentials can write one memory through the API
#
# It is safe for CI because it runs in a dedicated namespace inside a dedicated
# cluster and always cleans both up.

CLUSTER_NAME="${K8S_SETUP_VERIFY_CLUSTER:-kora-k8s-setup}"
NAMESPACE="${K8S_SETUP_VERIFY_NAMESPACE:-kora}"
RELEASE="${K8S_SETUP_VERIFY_RELEASE:-kora}"
HELM_VALUES_FILE="${K8S_SETUP_VERIFY_HELM_VALUES_FILE:-./charts/kora/values-kind.yaml}"
K8S_SETUP_HELM_EXTRA_FLAGS="${K8S_SETUP_VERIFY_HELM_EXTRA_FLAGS:-}"

API_IMAGE_REPO="${K8S_SETUP_VERIFY_API_IMAGE_REPO:-kora/kora}"
API_IMAGE_TAG="${K8S_SETUP_VERIFY_API_IMAGE_TAG:-dev}"
PG_IMAGE_REPO="${K8S_SETUP_VERIFY_PG_IMAGE_REPO:-kora/postgres-age-vector}"
PG_IMAGE_TAG="${K8S_SETUP_VERIFY_PG_IMAGE_TAG:-${API_IMAGE_TAG}}"
SKIP_CLEANUP="${K8S_SETUP_VERIFY_SKIP_CLEANUP:-}"

cleanup() {
  if [[ -n "${SKIP_CLEANUP}" ]]; then
    echo "Skipping cleanup due to K8S_SETUP_VERIFY_SKIP_CLEANUP."
    return
  fi

  KIND_CLUSTER="$CLUSTER_NAME" bash scripts/teardown.sh || true
  rm -f .dev-credentials
}
trap cleanup EXIT

if ! docker image inspect "$API_IMAGE_REPO:$API_IMAGE_TAG" >/dev/null 2>&1; then
  echo "image not found: $API_IMAGE_REPO:$API_IMAGE_TAG" >&2
  exit 1
fi
if ! docker image inspect "$PG_IMAGE_REPO:$PG_IMAGE_TAG" >/dev/null 2>&1; then
  echo "image not found: $PG_IMAGE_REPO:$PG_IMAGE_TAG" >&2
  exit 1
fi

echo "Running Kora setup smoke check in cluster '$CLUSTER_NAME', namespace '$NAMESPACE'."
KIND_CLUSTER="$CLUSTER_NAME" \
  HELM_NAMESPACE="$NAMESPACE" \
  HELM_RELEASE_NAME="$RELEASE" \
  K8S_SETUP_HELM_VALUES_FILE="$HELM_VALUES_FILE" \
  K8S_SETUP_DOCKER_IMAGE="$API_IMAGE_REPO" \
  K8S_SETUP_DOCKER_TAG="$API_IMAGE_TAG" \
  K8S_SETUP_HELM_EXTRA_FLAGS="--set api.image.repository=$API_IMAGE_REPO --set api.image.tag=$API_IMAGE_TAG --set postgres.image.repository=$PG_IMAGE_REPO --set postgres.image.tag=$PG_IMAGE_TAG --set web.enabled=false ${K8S_SETUP_HELM_EXTRA_FLAGS}" \
  bash scripts/setup-k8s.sh

kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=kora-api --timeout=180s >/dev/null
kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=postgres-age --timeout=180s >/dev/null

api_pod="$(kubectl get pod -n "$NAMESPACE" -l app=kora-api \
  -o jsonpath='{.items[0].metadata.name}')"

kubectl exec -n "$NAMESPACE" "$api_pod" -- \
  wget -q -O- --timeout=5 \
    http://localhost:8080/readyz >/dev/null

# Reuse the generated local credentials for one live write.
source ./.dev-credentials
kubectl exec -n "$NAMESPACE" "$api_pod" -- \
  sh -c "wget -q -O- --header='X-API-Key: ${DEV_API_KEY}' --header='Content-Type: application/json' \
    --post-data='{\"content\":\"smoke check\",\"type\":2,\"project_id\":\"smoke\"}' \
    http://localhost:8080/v1/memories" >/dev/null

echo "k8s setup smoke check passed."
