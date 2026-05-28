#!/bin/bash
# One-shot bootstrap: creates a Kubernetes Job that builds and pushes all Prow
# images in-cluster using ack-build-tools.
#
# This runs after Flux kustomizations are healthy (pod identities, namespaces,
# and secrets are all in place). The Job uses the builder image previously
# pushed by bootstrap-images.sh.
#
# This script is idempotent — if the Job already completed, it exits early.
#
# Usage: bootstrap-prow-images.sh <cluster-name> <region> <prow-images-repo-uri> <builder-tag>
set -euo pipefail

CLUSTER="$1"
REGION="$2"
PROW_IMAGES_REPO_URI="$3"
BUILDER_TAG="$4"
TIMEOUT=3600
INTERVAL=15

echo "=== Bootstrap Prow Images ==="
echo "  Builder image: ${PROW_IMAGES_REPO_URI}:${BUILDER_TAG}"

aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION" 2>/dev/null

# Exit early if the Job already completed successfully
STATUS=$(kubectl get job prow-build-images -n test-pods -o jsonpath='{.status.succeeded}' 2>/dev/null || echo "")
if [ "${STATUS:-0}" -ge 1 ]; then
  echo "  prow-build-images Job already completed. Nothing to do."
  exit 0
fi

# Read variables from the ConfigMap in flux-system
STACK_NAME=$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.STACK_NAME}')
TEST_INFRA_ORG=$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.TEST_INFRA_ORG}')
TEST_INFRA_REPO=$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.TEST_INFRA_REPO}')
TEST_INFRA_BRANCH=$(kubectl get configmap self-managed-vars -n flux-system -o jsonpath='{.data.TEST_INFRA_BRANCH}')

# Delete any previous failed Job so we can re-create it
kubectl delete job prow-build-images -n test-pods 2>/dev/null || true

# Create the Job
echo "  Creating prow-build-images Job..."
kubectl apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: prow-build-images
  namespace: test-pods
spec:
  backoffLimit: 2
  template:
    spec:
      serviceAccountName: post-submit-service-account
      restartPolicy: Never
      containers:
      - name: builder
        image: ${PROW_IMAGES_REPO_URI}:${BUILDER_TAG}
        command:
        - /bin/bash
        - -c
        - |
          set -euo pipefail
          git clone --branch ${TEST_INFRA_BRANCH} https://github.com/${TEST_INFRA_ORG}/${TEST_INFRA_REPO} /workspace
          cd /workspace
          ./prow/jobs/tools/cmd/build-prow-images.sh
        env:
        - name: PROW_IMAGES_REPO_URI
          value: "${PROW_IMAGES_REPO_URI}"
        - name: PROW_IMAGES_REPO_NAME
          value: "${STACK_NAME}-prow-images"
        - name: TEST_INFRA_ORG
          value: "${TEST_INFRA_ORG}"
        - name: TEST_INFRA_REPO
          value: "${TEST_INFRA_REPO}"
        - name: TEST_INFRA_BRANCH
          value: "${TEST_INFRA_BRANCH}"
        - name: REPO_NAME
          value: "${TEST_INFRA_REPO}"
        - name: GITHUB_TOKEN
          valueFrom:
            secretKeyRef:
              name: prowjob-github-pat-token
              key: token
        volumeMounts:
        - name: secrets
          mountPath: /mnt/secrets
          readOnly: true
        resources:
          requests:
            cpu: "2"
            memory: "4Gi"
          limits:
            cpu: "2"
            memory: "4Gi"
        securityContext:
          privileged: true
      volumes:
      - name: secrets
        csi:
          driver: secrets-store.csi.k8s.io
          readOnly: true
          volumeAttributes:
            secretProviderClass: prow-secrets
EOF

# Wait for Job to complete
echo "  Waiting for prow-build-images Job to complete..."
elapsed=0
while true; do
  SUCCEEDED=$(kubectl get job prow-build-images -n test-pods -o jsonpath='{.status.succeeded}' 2>/dev/null || echo "0")
  FAILED=$(kubectl get job prow-build-images -n test-pods -o jsonpath='{.status.failed}' 2>/dev/null || echo "0")

  if [ "${SUCCEEDED:-0}" -ge 1 ]; then
    echo "  prow-build-images Job completed successfully."
    break
  fi

  if [ "${FAILED:-0}" -ge 3 ]; then
    echo "  ERROR: prow-build-images Job failed after retries."
    kubectl logs job/prow-build-images -n test-pods --tail=50 2>/dev/null || true
    exit 1
  fi

  if [ $elapsed -ge $TIMEOUT ]; then
    echo "  ERROR: Timed out waiting for prow-build-images Job after ${TIMEOUT}s."
    kubectl logs job/prow-build-images -n test-pods --tail=50 2>/dev/null || true
    exit 1
  fi

  echo "  Waiting... (${elapsed}s elapsed)"
  sleep $INTERVAL
  elapsed=$((elapsed + INTERVAL))
done

echo "=== Prow Images Bootstrap complete ==="
