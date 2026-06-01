#!/bin/bash
# Waits for the prow-compute NodePool to become ready, then deletes
# the built-in general-purpose NodePool.
#
# Called as the final step in terraform apply to ensure only the custom
# NodePool handles scheduling after bootstrap.
set -euo pipefail

CLUSTER="${1:-ack-test-infra}"
REGION="${2:-us-west-2}"
TIMEOUT=600
INTERVAL=10

echo "=== Waiting for prow-compute NodePool to be ready ==="

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" 2>/dev/null || {
  echo "Cannot connect to cluster. Skipping nodepool swap."
  exit 0
}

elapsed=0
while true; do
  READY=$(kubectl get nodepool prow-compute -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
  if [ "$READY" = "True" ]; then
    echo "  prow-compute NodePool is Ready."
    break
  fi

  if [ $elapsed -ge $TIMEOUT ]; then
    echo "  Timed out waiting for prow-compute NodePool after ${TIMEOUT}s. Skipping."
    exit 0
  fi

  echo "  Waiting... (${elapsed}s elapsed, status: ${READY:-not found})"
  sleep $INTERVAL
  elapsed=$((elapsed + INTERVAL))
done

# Delete the built-in general-purpose NodePool
if kubectl get nodepool general-purpose &>/dev/null; then
  echo "  Deleting general-purpose NodePool..."
  kubectl delete nodepool general-purpose --wait=false
  echo "  general-purpose NodePool deleted."
else
  echo "  general-purpose NodePool already absent."
fi

echo "=== NodePool swap complete ==="
