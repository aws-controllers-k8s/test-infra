#!/bin/bash
# Creates a namespace if it does not already exist.
#
# Usage: bootstrap-namespaces.sh <cluster-name> <region> <namespace>
set -euo pipefail

CLUSTER="$1"
REGION="$2"
NAMESPACE="$3"

# Use a temporary kubeconfig to avoid concurrent writes to ~/.kube/config
# when multiple provisioners run in parallel.
KUBECONFIG_TMP="$(mktemp)"
trap 'rm -f "$KUBECONFIG_TMP"' EXIT

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" --kubeconfig "$KUBECONFIG_TMP" 2>/dev/null

export KUBECONFIG="$KUBECONFIG_TMP"

if kubectl get namespace "$NAMESPACE" &>/dev/null; then
  echo "Namespace $NAMESPACE already exists."
else
  echo "Creating namespace $NAMESPACE..."
  kubectl create namespace "$NAMESPACE"
fi
