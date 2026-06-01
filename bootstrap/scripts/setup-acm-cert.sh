#!/usr/bin/env bash
# setup-acm-cert.sh — Request and validate an ACM certificate for the Prow domain.
#
# Usage: ./scripts/setup-acm-cert.sh <domain> <region>
#
# Prerequisites:
#   - The Route53 hosted zone for <domain> must already exist
#     (created by ACK via flux/ack/prow/prow-dns.yaml)
#   - AWS credentials with acm:* and route53:* permissions
#
# The ALB controller auto-discovers issued ACM certs matching the ingress host.
# This script is idempotent — re-running it skips already-issued certs.

set -euo pipefail

DOMAIN="${1:?Usage: $0 <domain> <region>}"
REGION="${2:?Usage: $0 <domain> <region>}"

echo "=== ACM Certificate Setup for ${DOMAIN} ==="

# Check if a valid cert already exists
EXISTING_ARN=$(aws acm list-certificates --region "${REGION}" \
  --query "CertificateSummaryList[?DomainName=='${DOMAIN}' && Status!='FAILED'].CertificateArn | [0]" \
  --output text 2>/dev/null || echo "None")

if [ "${EXISTING_ARN}" != "None" ] && [ -n "${EXISTING_ARN}" ]; then
  STATUS=$(aws acm describe-certificate --certificate-arn "${EXISTING_ARN}" --region "${REGION}" \
    --query "Certificate.Status" --output text)
  if [ "${STATUS}" = "ISSUED" ]; then
    echo "Certificate already issued: ${EXISTING_ARN}"
    exit 0
  fi
  echo "Found existing certificate (${STATUS}): ${EXISTING_ARN}"
  CERT_ARN="${EXISTING_ARN}"
else
  echo "Requesting new certificate..."
  CERT_ARN=$(aws acm request-certificate \
    --domain-name "${DOMAIN}" \
    --validation-method DNS \
    --region "${REGION}" \
    --query "CertificateArn" --output text)
  echo "Certificate requested: ${CERT_ARN}"
  sleep 5
fi

# Wait for validation record to appear
echo "Retrieving DNS validation record..."
for i in $(seq 1 12); do
  VALIDATION=$(aws acm describe-certificate --certificate-arn "${CERT_ARN}" --region "${REGION}" \
    --query "Certificate.DomainValidationOptions[0].ResourceRecord" --output json 2>/dev/null)
  if [ "${VALIDATION}" != "null" ] && [ -n "${VALIDATION}" ]; then
    break
  fi
  echo "  Waiting for validation record (attempt ${i}/12)..."
  sleep 5
done

if [ "${VALIDATION}" = "null" ] || [ -z "${VALIDATION}" ]; then
  echo "ERROR: Could not retrieve validation record after 60s"
  exit 1
fi

RECORD_NAME=$(echo "${VALIDATION}" | python3 -c "import json,sys; print(json.load(sys.stdin)['Name'])")
RECORD_VALUE=$(echo "${VALIDATION}" | python3 -c "import json,sys; print(json.load(sys.stdin)['Value'])")
echo "Validation CNAME: ${RECORD_NAME} -> ${RECORD_VALUE}"

# Find the hosted zone
ZONE_ID=$(aws route53 list-hosted-zones-by-name --dns-name "${DOMAIN}" \
  --query "HostedZones[?Name=='${DOMAIN}.'].Id" --output text | head -1 | sed 's|/hostedzone/||')

if [ -z "${ZONE_ID}" ]; then
  echo "ERROR: Hosted zone for ${DOMAIN} not found."
  echo "Wait for the ack-prow kustomization to become Ready, then re-run."
  exit 1
fi
echo "Hosted zone: ${ZONE_ID}"

# Create the validation CNAME
echo "Creating DNS validation record..."
aws route53 change-resource-record-sets --hosted-zone-id "${ZONE_ID}" \
  --change-batch "{
    \"Changes\": [{
      \"Action\": \"UPSERT\",
      \"ResourceRecordSet\": {
        \"Name\": \"${RECORD_NAME}\",
        \"Type\": \"CNAME\",
        \"TTL\": 300,
        \"ResourceRecords\": [{\"Value\": \"${RECORD_VALUE}\"}]
      }
    }]
  }" --region "${REGION}" > /dev/null

# Wait for issuance
echo "Waiting for certificate validation (up to 5 minutes)..."
if aws acm wait certificate-validated --certificate-arn "${CERT_ARN}" --region "${REGION}"; then
  echo "=== Certificate issued: ${CERT_ARN} ==="
else
  echo "WARNING: Certificate not yet validated. ACM may need a few more minutes."
  echo "The ALB will pick it up automatically once issued."
  exit 0
fi
