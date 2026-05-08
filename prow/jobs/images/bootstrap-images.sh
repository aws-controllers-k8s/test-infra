#!/usr/bin/env bash
# Bootstrap script: builds and pushes the prow-image-builder to public ECR.
# The remaining images are built by a Kubernetes Job in the cluster.
#
# Usage:
#   ./bootstrap-images.sh
#
# Environment variables:
#   AWS_ACCOUNT_ID:     AWS account ID (required)
#   AWS_REGION:         AWS region (default: us-west-2)
#   PROW_IMAGE_REPO_URI: Full public ECR repository URI (required)
#   VERSION:            Image tag version (default: git describe)
#   GO_VERSION:         Go version for builds (default: 1.22.5)

set -eo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
# test-infra root (where go.mod lives)
TEST_INFRA_ROOT="$(cd "$DIR/../../.." && pwd)"

AWS_ACCOUNT_ID="${AWS_ACCOUNT_ID:?AWS_ACCOUNT_ID is required}"
AWS_REGION="${AWS_REGION:-us-west-2}"
PROW_IMAGE_REPO_URI="${PROW_IMAGE_REPO_URI:?PROW_IMAGE_REPO_URI is required}"
VERSION="${VERSION:-$(cd "$TEST_INFRA_ROOT" && git describe --tags --always --dirty 2>/dev/null || echo "latest")}"
GO_VERSION="${GO_VERSION:-"1.22.5"}"

BUILDER_IMAGE="prow/build-prow-images"
BUILDER_TAG="${PROW_IMAGE_REPO_URI}:prow-build-prow-images-${VERSION}"

echo "=== Prow Images Bootstrap ==="
echo "Repository: ${PROW_IMAGE_REPO_URI}"
echo "Version:    ${VERSION}"
echo ""

# --- Step 1: Login to public ECR ---
echo "Logging in to public ECR..."
aws ecr-public get-login-password --region us-east-1 | \
  docker login -u AWS --password-stdin public.ecr.aws

# --- Step 2: Build and push the builder image ---
echo ""
echo "Building builder image locally..."
docker build --platform="linux/amd64" \
  -f "${DIR}/Dockerfile.build-prow-images" \
  --build-arg "GO_VERSION=${GO_VERSION}" \
  -t "${BUILDER_IMAGE}" \
  "${TEST_INFRA_ROOT}"

echo "Pushing builder image..."
docker tag "${BUILDER_IMAGE}" "${BUILDER_TAG}"
docker push "${BUILDER_TAG}"
# Also push as latest for the in-cluster build job
docker tag "${BUILDER_IMAGE}" "${PROW_IMAGE_REPO_URI}:prow-build-prow-images-latest"
docker push "${PROW_IMAGE_REPO_URI}:prow-build-prow-images-latest"
echo "  ✓ Pushed: ${BUILDER_TAG}"

echo ""
echo "=== Bootstrap complete ==="
echo "The builder image is now available. Deploy a Kubernetes Job in the"
echo "cluster to build and push all remaining Prow images."
