#!/usr/bin/env bash
set -euo pipefail

# Tear down Context0 demo environment.

CLUSTER_NAME="context0-dev"

echo "Deleting kind cluster '$CLUSTER_NAME'..."
kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
echo "Done."
