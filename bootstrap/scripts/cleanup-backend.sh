#!/usr/bin/env bash
#
# Deletes the Terraform S3 state backend bucket.
# Finds the bucket by prefix, empties it (including versions), and deletes it.
#
# WARNING: This is irreversible. Only run after terraform destroy.
#
# Usage: ./scripts/cleanup-backend.sh [region]
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP_DIR="${SCRIPT_DIR}/.."
REGION="${1:-us-west-2}"
BUCKET_PREFIX="ack-test-infra-terraform-state"

echo "=== Cleaning Up Terraform State Backend ==="

# Find the bucket
BUCKET=$(aws s3api list-buckets --query "Buckets[?starts_with(Name, '${BUCKET_PREFIX}')].Name | [0]" --output text 2>/dev/null)

if [ -z "$BUCKET" ] || [ "$BUCKET" = "None" ]; then
  echo "  No bucket found with prefix '${BUCKET_PREFIX}'. Nothing to clean up."
  exit 0
fi

echo "  Found bucket: ${BUCKET}"
echo ""
read -rp "  This will permanently delete the bucket and all state. Continue? [y/N]: " confirm
if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
  echo "  Aborted."
  exit 0
fi

# Delete all object versions
echo "  Deleting object versions..."
aws s3api list-object-versions \
  --bucket "$BUCKET" \
  --region "$REGION" \
  --query '{Objects: Versions[].{Key:Key,VersionId:VersionId}}' \
  --output json 2>/dev/null | \
  aws s3api delete-objects \
    --bucket "$BUCKET" \
    --region "$REGION" \
    --delete file:///dev/stdin 2>/dev/null || true

# Delete all delete markers
echo "  Deleting delete markers..."
aws s3api list-object-versions \
  --bucket "$BUCKET" \
  --region "$REGION" \
  --query '{Objects: DeleteMarkers[].{Key:Key,VersionId:VersionId}}' \
  --output json 2>/dev/null | \
  aws s3api delete-objects \
    --bucket "$BUCKET" \
    --region "$REGION" \
    --delete file:///dev/stdin 2>/dev/null || true

# Delete the bucket
echo "  Deleting bucket..."
aws s3api delete-bucket --bucket "$BUCKET" --region "$REGION"

# Remove generated backend.tf
if [ -f "${BOOTSTRAP_DIR}/backend.tf" ]; then
  rm -f "${BOOTSTRAP_DIR}/backend.tf"
  echo "  Removed backend.tf"
fi

echo ""
echo "=== Backend cleanup complete ==="
