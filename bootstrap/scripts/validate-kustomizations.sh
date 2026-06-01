#!/bin/bash
# Validates that all Flux Kustomizations in flux-system are Ready.
#
# Polls until every Kustomization reports Ready=True in its status
# conditions, or times out.
set -euo pipefail

CLUSTER="$1"
REGION="$2"
TIMEOUT=900
INTERVAL=15

echo "=== Validating Flux Kustomizations in flux-system ==="

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" 2>/dev/null

elapsed=0
while true; do
  # Get all Kustomizations in flux-system
  KUSTOMIZATIONS=$(kubectl get kustomizations.kustomize.toolkit.fluxcd.io -n flux-system -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)

  if [ -z "$KUSTOMIZATIONS" ]; then
    echo "  No Kustomizations found in flux-system yet. Waiting..."
    if [ $elapsed -ge $TIMEOUT ]; then
      echo "  ERROR: Timed out waiting for Kustomizations after ${TIMEOUT}s."
      exit 1
    fi
    sleep $INTERVAL
    elapsed=$((elapsed + INTERVAL))
    continue
  fi

  ALL_READY=true
  TOTAL=0
  NOT_READY=0

  for ks in $KUSTOMIZATIONS; do
    TOTAL=$((TOTAL + 1))

    READY=$(kubectl get kustomizations.kustomize.toolkit.fluxcd.io "$ks" -n flux-system \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")

    if [ "$READY" != "True" ]; then
      ALL_READY=false
      NOT_READY=$((NOT_READY + 1))
      REASON=$(kubectl get kustomizations.kustomize.toolkit.fluxcd.io "$ks" -n flux-system \
        -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}' 2>/dev/null || echo "Unknown")
      echo "  Not ready: $ks (reason: $REASON)"
    fi
  done

  if [ "$ALL_READY" = true ]; then
    echo "  All $TOTAL Kustomizations are Ready."
    break
  else
    echo "  $NOT_READY/$TOTAL Kustomizations not yet ready."
  fi

  if [ $elapsed -ge $TIMEOUT ]; then
    echo "  ERROR: Timed out waiting for Kustomizations to be ready after ${TIMEOUT}s."
    echo "  $NOT_READY/$TOTAL Kustomizations still not ready."
    exit 1
  fi

  sleep $INTERVAL
  elapsed=$((elapsed + INTERVAL))
done

echo "=== Kustomization validation complete ==="
