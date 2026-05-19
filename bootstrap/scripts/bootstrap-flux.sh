#!/bin/bash
# Bootstraps Flux into the cluster.
#
# 1. Creates the flux-system namespace and required ConfigMaps
# 2. Installs a temporary Flux instance in bootstrap-flux-system (namespace-scoped)
# 3. Creates the GitRepository + Kustomization pointing to ./flux/flux
# 4. Waits for the vendored Flux to be running in flux-system
# 5. Tears down the bootstrap Flux (no longer needed)
#
# This script is idempotent — if vendored Flux is already running, it exits early.
#
# Required environment variables (passed from Terraform):
#   STACK_NAME, ACCOUNT_ID, REGION, CLUSTER_SG_ID, VPC_ID,
#   GHCR_PTC_SECRET_ARN, PROW_DOMAIN, PROW_IMAGES_REPO_URI,
#   TEST_INFRA_ORG, TEST_INFRA_REPO, TEST_INFRA_BRANCH,
#   FLUX_VERSION, FLUX_IMAGE_REGISTRY
set -euo pipefail

CLUSTER="$1"
REGION_ARG="$2"
CHART_PATH="$3"
GIT_URL="$4"
GIT_BRANCH="$5"
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

# Step 1: Create flux-system namespace and ConfigMaps
echo "  Step 1: Creating flux-system namespace and ConfigMaps..."
kubectl create namespace flux-system --dry-run=client -o yaml | kubectl apply -f - 2>/dev/null

kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: self-managed-vars
  namespace: flux-system
data:
  STACK_NAME: "${STACK_NAME}"
  ACCOUNT_ID: "${ACCOUNT_ID}"
  REGION: "${REGION}"
  CLUSTER_SG_ID: "${CLUSTER_SG_ID}"
  VPC_ID: "${VPC_ID}"
  GHCR_PTC_SECRET_ARN: "${GHCR_PTC_SECRET_ARN}"
  PROW_DOMAIN: "${PROW_DOMAIN}"
  PROW_IMAGES_REPO_URI: "${PROW_IMAGES_REPO_URI}"
  TEST_INFRA_ORG: "${TEST_INFRA_ORG}"
  TEST_INFRA_REPO: "${TEST_INFRA_REPO}"
  TEST_INFRA_BRANCH: "${TEST_INFRA_BRANCH}"
  FLUX_IMAGE_REGISTRY: "${FLUX_IMAGE_REGISTRY}"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: flux-version
  namespace: flux-system
data:
  FLUX_VERSION: "${FLUX_VERSION}"
EOF

# Step 2: Install bootstrap Flux
echo "  Step 2: Installing bootstrap Flux in bootstrap-flux-system..."
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

# Create ConfigMaps in bootstrap-flux-system so the bootstrap Kustomization
# can resolve postBuild.substituteFrom (namespace-scoped lookup).
# FLUX_IMAGE_REGISTRY uses public ghcr.io images here so the initial install
# doesn't depend on the ECR pull-through cache being warm.
kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: self-managed-vars
  namespace: bootstrap-flux-system
data:
  STACK_NAME: "${STACK_NAME}"
  ACCOUNT_ID: "${ACCOUNT_ID}"
  REGION: "${REGION}"
  CLUSTER_SG_ID: "${CLUSTER_SG_ID}"
  VPC_ID: "${VPC_ID}"
  GHCR_PTC_SECRET_ARN: "${GHCR_PTC_SECRET_ARN}"
  PROW_DOMAIN: "${PROW_DOMAIN}"
  PROW_IMAGES_REPO_URI: "${PROW_IMAGES_REPO_URI}"
  TEST_INFRA_ORG: "${TEST_INFRA_ORG}"
  TEST_INFRA_REPO: "${TEST_INFRA_REPO}"
  TEST_INFRA_BRANCH: "${TEST_INFRA_BRANCH}"
  FLUX_IMAGE_REGISTRY: "ghcr.io/fluxcd"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: flux-version
  namespace: bootstrap-flux-system
data:
  FLUX_VERSION: "${FLUX_VERSION}"
EOF

# Step 3: Create GitRepository + Kustomization for vendored Flux
echo "  Step 3: Creating bootstrap GitRepository + Kustomization..."

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

# Step 4: Wait for vendored Flux to be running
echo "  Step 4: Waiting for vendored Flux to be running in flux-system..."
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

# Step 5: Tear down bootstrap Flux
echo "  Step 5: Removing bootstrap Flux..."

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

# Clean up cluster-scoped resources
kubectl delete clusterrolebinding bootstrap-flux-cluster-admin 2>/dev/null || true

echo "=== Flux bootstrap complete ==="
