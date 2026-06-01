#!/usr/bin/env bash
#
# Bootstraps the Terraform S3 state backend.
#
# Looks for an existing bucket with prefix "ack-test-infra-terraform-state".
# If none exists, creates one with a random suffix for global uniqueness.
# Outputs a templated backend.tf file.
#
# Usage: ./scripts/bootstrap-backend.sh [region]
#
# Defaults:
#   region: us-west-2
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP_DIR="${SCRIPT_DIR}/.."
REGION="${1:-us-west-2}"
BUCKET_PREFIX="ack-test-infra-terraform-state"

echo "=== Bootstrapping Terraform State Backend ==="
echo "  Region: ${REGION}"
echo "  Prefix: ${BUCKET_PREFIX}"
echo ""

# Check for an existing bucket with the prefix
EXISTING_BUCKET=$(aws s3api list-buckets --query "Buckets[?starts_with(Name, '${BUCKET_PREFIX}')].Name | [0]" --output text 2>/dev/null)

if [ -n "$EXISTING_BUCKET" ] && [ "$EXISTING_BUCKET" != "None" ]; then
  BUCKET="$EXISTING_BUCKET"
  echo "  Found existing bucket: ${BUCKET}"
else
  # Generate a random 8-character suffix
  SUFFIX=$(head -c 4 /dev/urandom | xxd -p)
  BUCKET="${BUCKET_PREFIX}-${SUFFIX}"
  echo "  No existing bucket found. Creating: ${BUCKET}"

  aws s3api create-bucket \
    --bucket "$BUCKET" \
    --region "$REGION" \
    --create-bucket-configuration LocationConstraint="$REGION"
fi

# Enable versioning
echo "  Enabling versioning..."
aws s3api put-bucket-versioning \
  --bucket "$BUCKET" \
  --versioning-configuration Status=Enabled

# Enable default encryption (SSE-KMS with bucket key)
echo "  Enabling encryption..."
aws s3api put-bucket-encryption \
  --bucket "$BUCKET" \
  --server-side-encryption-configuration '{
    "Rules": [{"ApplyServerSideEncryptionByDefault": {"SSEAlgorithm": "aws:kms"}, "BucketKeyEnabled": true}]
  }'

# Block all public access
echo "  Blocking public access..."
aws s3api put-public-access-block \
  --bucket "$BUCKET" \
  --public-access-block-configuration \
    BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

# Write backend.tf
BACKEND_FILE="${BOOTSTRAP_DIR}/backend.tf"
cat > "$BACKEND_FILE" <<EOF
terraform {
  backend "s3" {
    bucket       = "${BUCKET}"
    key          = "bootstrap/terraform.tfstate"
    region       = "${REGION}"
    use_lockfile = true
    encrypt      = true
  }
}
EOF

echo ""
echo "=== Backend ready ==="
echo ""
echo "  Bucket:  ${BUCKET}"
echo "  Written: bootstrap/backend.tf"
echo ""
echo "Next steps:"
echo "  terraform init"
echo "  terraform plan -var-file=environment/dev.tfvars"
