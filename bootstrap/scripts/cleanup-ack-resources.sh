#!/bin/bash
# Cleans up all ACK custom resources in the ack-system namespace.
# Does NOT delete the namespace itself.
#
# Waits for the ACK controller to fully reconcile deletions (finalizers removed)
# before exiting, ensuring backing AWS resources are cleaned up.
set -euo pipefail

CLUSTER="$1"
REGION="$2"
TIMEOUT=900
INTERVAL=15

echo "=== Cleaning up ACK custom resources in ack-system ==="

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" 2>/dev/null

# Discover all ACK CRDs (they belong to *.services.k8s.aws API groups)
ACK_RESOURCES=$(kubectl api-resources -o name 2>/dev/null | grep '\.services\.k8s\.aws' || true)

if [ -z "$ACK_RESOURCES" ]; then
  echo "  No ACK resource types found. Nothing to clean up."
  exit 0
fi

# Delete all ACK custom resource instances in ack-system
FOUND_ANY=false
for resource in $ACK_RESOURCES; do
  INSTANCES=$(kubectl get "$resource" -n ack-system -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
  if [ -n "$INSTANCES" ]; then
    FOUND_ANY=true
    echo "  Deleting $resource instances: $INSTANCES"
    kubectl delete "$resource" --all -n ack-system --timeout=60s 2>/dev/null || true
  fi
done

if [ "$FOUND_ANY" = false ]; then
  echo "  No ACK custom resource instances found in ack-system. Nothing to clean up."
  exit 0
fi

# Poll until all ACK custom resources are fully removed (finalizers reconciled)
echo "  Waiting for ACK controller to reconcile deletions..."
elapsed=0
while true; do
  REMAINING=0

  for resource in $ACK_RESOURCES; do
    COUNT=$(kubectl get "$resource" -n ack-system -o jsonpath='{.items}' 2>/dev/null | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
    REMAINING=$((REMAINING + COUNT))
  done

  if [ "$REMAINING" -eq 0 ]; then
    echo "  All ACK custom resources have been deleted and reconciled."
    break
  fi

  if [ $elapsed -ge $TIMEOUT ]; then
    echo "  ERROR: Timed out waiting for ACK resource deletion after ${TIMEOUT}s."
    echo "  $REMAINING resources still remaining."
    exit 1
  fi

  echo "  $REMAINING resources still pending deletion... (${elapsed}s elapsed)"
  sleep $INTERVAL
  elapsed=$((elapsed + INTERVAL))
done

echo "=== ACK resource cleanup complete ==="
