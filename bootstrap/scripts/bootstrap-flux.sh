#!/bin/bash
# Bootstraps Flux into the cluster.
#
# 1. Installs a temporary Flux instance in bootstrap-flux-system (namespace-scoped)
# 2. Creates the GitRepository + Kustomization pointing to ./flux/flux
# 3. Waits for the vendored Flux to be running in flux-system
# 4. Tears down the bootstrap Flux (no longer needed)
#
# Prerequisites (handled by Terraform before this script runs):
#   - flux-system namespace exists
#   - self-managed-vars and flux-version ConfigMaps exist in flux-system
#
# This script is idempotent — if vendored Flux is already running, it exits early.
#
# Usage: bootstrap-flux.sh <cluster-name> <region> <chart-path>
set -euo pipefail

CLUSTER="$1"
REGION_ARG="$2"
CHART_PATH="$3"
TIMEOUT=600
INTERVAL=10

echo "=== Bootstrapping Flux ==="

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION_ARG" 2>/dev/null

# Exit early if vendored Flux is already running
READY=$(kubectl get deployment source-controller -n flux-system -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
if [ "${READY:-0}" -ge 1 ]; then
  echo "  Vendored Flux already running in flux-system. Nothing to do."
  exit 0
fi

# Read values from the Terraform-managed ConfigMaps in flux-system
echo "  Reading configuration from flux-system ConfigMaps..."
GIT_URL="https://github.com/$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.TEST_INFRA_ORG}')/$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.TEST_INFRA_REPO}')"
GIT_BRANCH=$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.TEST_INFRA_BRANCH}')

# Step 1: Install bootstrap Flux
echo "  Step 1: Installing bootstrap Flux in bootstrap-flux-system..."
kubectl create namespace bootstrap-flux-system --dry-run=client -o yaml | kubectl apply -f - 2>/dev/null

helm upgrade --install bootstrap-flux "$CHART_PATH" \
  --namespace bootstrap-flux-system \
  --set cli.enabled=false \
  --wait --timeout 5m

# Grant bootstrap controllers cluster-admin so they can manage resources in
# flux-system (helm-controller needs to create secrets, kustomize-controller
# needs to apply resources across namespaces).
kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: bootstrap-flux-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: helm-controller
  namespace: bootstrap-flux-system
- kind: ServiceAccount
  name: kustomize-controller
  namespace: bootstrap-flux-system
- kind: ServiceAccount
  name: source-controller
  namespace: bootstrap-flux-system
EOF

# Temporarily override FLUX_IMAGE_REGISTRY in flux-system to use public
# ghcr.io images so the vendored Flux can start without the ECR pull-through
# cache (which is created later by ACK). The original value is restored after
# Flux is running and the pull-through cache is available.
ORIGINAL_REGISTRY=$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.FLUX_IMAGE_REGISTRY}')
kubectl patch configmap self-managed-vars -n flux-system \
  --type merge -p '{"data":{"FLUX_IMAGE_REGISTRY":"ghcr.io/fluxcd"}}'

# Copy ConfigMaps from flux-system to bootstrap-flux-system (already has
# ghcr.io/fluxcd from the patch above).
kubectl get configmap self-managed-vars -n flux-system -o json \
  | jq '.metadata.namespace = "bootstrap-flux-system" | del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp)' \
  | kubectl apply -f -

kubectl get configmap flux-version -n flux-system -o json \
  | jq '.metadata.namespace = "bootstrap-flux-system" | del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp)' \
  | kubectl apply -f -

# Step 2: Create GitRepository + Kustomization for vendored Flux
echo "  Step 2: Creating bootstrap GitRepository + Kustomization..."

kubectl apply -f - <<EOF
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: test-infra
  namespace: bootstrap-flux-system
spec:
  interval: 1m
  url: ${GIT_URL}
  ref:
    branch: ${GIT_BRANCH}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: bootstrap
  namespace: bootstrap-flux-system
spec:
  interval: 5m
  path: ./flux/flux
  prune: false
  sourceRef:
    kind: GitRepository
    name: test-infra
  postBuild:
    substituteFrom:
    - kind: ConfigMap
      name: flux-version
      optional: false
    - kind: ConfigMap
      name: self-managed-vars
      optional: false
EOF

# Step 3: Wait for vendored Flux to be running
echo "  Step 3: Waiting for vendored Flux to be running in flux-system..."
elapsed=0
while true; do
  READY=$(kubectl get deployment source-controller -n flux-system -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
  if [ "${READY:-0}" -ge 1 ]; then
    echo "  Vendored Flux is running."
    break
  fi

  if [ $elapsed -ge $TIMEOUT ]; then
    echo "  ERROR: Timed out waiting for vendored Flux after ${TIMEOUT}s."
    exit 1
  fi

  echo "  Waiting... (${elapsed}s elapsed)"
  sleep $INTERVAL
  elapsed=$((elapsed + INTERVAL))
done

# Wait for the vendored source-controller to serve the GitRepository artifact
# before tearing down bootstrap. This prevents kustomize-controller from
# caching stale URLs pointing to the bootstrap namespace.
echo "  Waiting for GitRepository artifact to be served by vendored Flux..."
elapsed=0
while true; do
  ARTIFACT_URL=$(kubectl get gitrepository test-infra -n flux-system -o jsonpath='{.status.artifact.url}' 2>/dev/null || echo "")
  if echo "$ARTIFACT_URL" | grep -q "source-controller.flux-system"; then
    echo "  GitRepository artifact served by vendored source-controller."
    break
  fi

  if [ $elapsed -ge $TIMEOUT ]; then
    echo "  ERROR: Timed out waiting for GitRepository artifact after ${TIMEOUT}s."
    exit 1
  fi

  sleep $INTERVAL
  elapsed=$((elapsed + INTERVAL))
done

# Step 4: Tear down bootstrap Flux
echo "  Step 4: Removing bootstrap Flux..."


# Strip all finalizers in the namespace so nothing blocks deletion
kubectl get all,gitrepositories.source.toolkit.fluxcd.io,kustomizations.kustomize.toolkit.fluxcd.io,helmreleases.helm.toolkit.fluxcd.io,helmcharts.source.toolkit.fluxcd.io -n bootstrap-flux-system -o name 2>/dev/null | while read -r res; do
  kubectl patch "$res" -n bootstrap-flux-system --type merge -p '{"metadata":{"finalizers":null}}' 2>/dev/null || true
done

# Delete the namespace
kubectl delete namespace bootstrap-flux-system --timeout=30s 2>/dev/null || true

# If still stuck, force-finalize it
if kubectl get ns bootstrap-flux-system 2>/dev/null | grep -q Terminating; then
  kubectl get ns bootstrap-flux-system -o json 2>/dev/null \
    | python3 -c 'import sys,json; ns=json.load(sys.stdin); ns["spec"]["finalizers"]=[]; print(json.dumps(ns))' \
    | kubectl replace --raw "/api/v1/namespaces/bootstrap-flux-system/finalize" -f - 2>/dev/null || true
fi

# Force-delete any pods stuck in Terminating state
kubectl delete pods --all -n bootstrap-flux-system --grace-period=0 --force 2>/dev/null || true

# Clean up cluster-scoped resources
kubectl delete clusterrolebinding bootstrap-flux-cluster-admin 2>/dev/null || true

# Force-reconcile all kustomizations so they pick up the artifact URL from the
# vendored source-controller instead of the now-deleted bootstrap one.
echo "  Forcing reconciliation of all Kustomizations in flux-system..."
kubectl annotate kustomization --all -n flux-system \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite

echo "=== Flux bootstrap complete ==="
