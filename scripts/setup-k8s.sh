#!/usr/bin/env bash

set -euo pipefail

# Local developer bootstrap for kind + Helm.
#
# This keeps the documented setup path in one place and makes the first run
# deterministic: cluster, credentials, chart install, namespace hardening, and a
# short readiness check are all done together.

CLUSTER_NAME="${KIND_CLUSTER:-${KORA_KIND_CLUSTER:-kora-dev}}"
NAMESPACE="${KORA_NAMESPACE:-kora}"
RELEASE_NAME="${KORA_RELEASE_NAME:-kora}"
NAMESPACE="${HELM_NAMESPACE:-${NAMESPACE}}"
RELEASE_NAME="${HELM_RELEASE_NAME:-${RELEASE_NAME}}"
HELM_VALUES_FILE="${K8S_SETUP_HELM_VALUES_FILE:-./charts/kora/values-kind.yaml}"
HELM_EXTRA_FLAGS="${K8S_SETUP_HELM_EXTRA_FLAGS:-}"
DOCKER_IMAGE="${K8S_SETUP_DOCKER_IMAGE:-${DOCKER_IMAGE:-kora/kora}}"
DOCKER_TAG="${K8S_SETUP_DOCKER_TAG:-${DOCKER_TAG:-dev}}"
POSTGRES_DOCKER_IMAGE="${K8S_SETUP_POSTGRES_DOCKER_IMAGE:-${POSTGRES_DOCKER_IMAGE:-kora/postgres-age-vector}}"
POSTGRES_DOCKER_TAG="${K8S_SETUP_POSTGRES_DOCKER_TAG:-${POSTGRES_DOCKER_TAG:-dev}}"

require_tool() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "missing required tool: $tool" >&2
    exit 1
  fi
}

for tool in kind kubectl helm docker go; do
  require_tool "$tool"
done

echo "Using kind cluster '$CLUSTER_NAME'."
if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
  if [[ -f deploy/kind-config.yaml ]]; then
    kind create cluster --name "$CLUSTER_NAME" --config deploy/kind-config.yaml
  else
    kind create cluster --name "$CLUSTER_NAME"
  fi
else
  echo "Reusing existing kind cluster '$CLUSTER_NAME'."
fi

kubectl config use-context "kind-$CLUSTER_NAME"

echo "Preparing namespace '$NAMESPACE' for Kubernetes hardening."
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace "$NAMESPACE" \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest \
  --overwrite

echo "Generating local credentials if needed."
make dev-credentials

if [[ -f "$HELM_VALUES_FILE" ]]; then
  echo "Installing with local values file '$HELM_VALUES_FILE'."
  DEPLOY_FLAGS="-f $HELM_VALUES_FILE --set postgres.image.repository=$POSTGRES_DOCKER_IMAGE --set postgres.image.tag=$POSTGRES_DOCKER_TAG"
  if [[ -n "$HELM_EXTRA_FLAGS" ]]; then
    DEPLOY_FLAGS="${DEPLOY_FLAGS} ${HELM_EXTRA_FLAGS}"
  fi
  make deploy \
    KIND_CLUSTER="$CLUSTER_NAME" \
    HELM_NAMESPACE="$NAMESPACE" \
    HELM_RELEASE_NAME="$RELEASE_NAME" \
    DOCKER_IMAGE="$DOCKER_IMAGE" \
    DOCKER_TAG="$DOCKER_TAG" \
    POSTGRES_DOCKER_IMAGE="$POSTGRES_DOCKER_IMAGE" \
    POSTGRES_DOCKER_TAG="$POSTGRES_DOCKER_TAG" \
    HELM_EXTRA_FLAGS="$DEPLOY_FLAGS"
else
  echo "Local values file '$HELM_VALUES_FILE' not found."
  echo "Falling back to the default chart values."
  make deploy \
    KIND_CLUSTER="$CLUSTER_NAME" \
    HELM_NAMESPACE="$NAMESPACE" \
    HELM_RELEASE_NAME="$RELEASE_NAME" \
    DOCKER_IMAGE="$DOCKER_IMAGE" \
    DOCKER_TAG="$DOCKER_TAG" \
    POSTGRES_DOCKER_IMAGE="$POSTGRES_DOCKER_IMAGE" \
    POSTGRES_DOCKER_TAG="$POSTGRES_DOCKER_TAG"
fi

echo "Waiting for core workloads to become ready."
kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=postgres-age --timeout=180s
kubectl -n "$NAMESPACE" wait --for=condition=ready pod -l app=kora-api --timeout=180s

if kubectl -n kube-system get daemonset kindnet >/dev/null 2>&1; then
  echo "The active cluster uses kindnet."
  echo "Kindnet may not enforce NetworkPolicy by default."
  echo "If you plan to validate policy behavior, check enforcement on your CNI."
fi

echo "Kora local setup complete in namespace '$NAMESPACE'."

. ./.dev-credentials
echo "API key: $DEV_API_KEY"
echo "Release: $RELEASE_NAME"
echo "Use this key against http://localhost:8080/v1/memories and related endpoints."
