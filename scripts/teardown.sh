#!/usr/bin/env bash
set -euo pipefail

# Tear down Kora demo environment.

CLUSTER_NAME="kora-dev"

echo "Deleting kind cluster '$CLUSTER_NAME'..."
kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
echo "Done."
