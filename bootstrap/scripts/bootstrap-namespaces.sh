#!/bin/bash
# Creates a namespace if it does not already exist.
#
# Usage: bootstrap-namespaces.sh <cluster-name> <region> <namespace>
set -euo pipefail

CLUSTER="$1"
REGION="$2"
NAMESPACE="$3"

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" 2>/dev/null

if kubectl get namespace "$NAMESPACE" &>/dev/null; then
  echo "Namespace $NAMESPACE already exists."
else
  echo "Creating namespace $NAMESPACE..."
  kubectl create namespace "$NAMESPACE"
fi
