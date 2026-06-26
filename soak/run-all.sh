#!/bin/bash
set -o pipefail

# Launches soak tests for multiple controllers in parallel.
# Each bootstrap gets its own KUBECONFIG file to avoid context races.
#
# Usage:
#   ./run-all.sh
#
# Environment variables:
#   TEST_DURATION_DAYS:  Duration in days (default: 7)
#   DEPLOY_REGION:       AWS region (default: us-west-2)
#   MAX_PARALLEL:        Max concurrent bootstraps (default: 5)

SOAK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
TEST_DURATION_DAYS=${TEST_DURATION_DAYS:-7}
DEPLOY_REGION=${DEPLOY_REGION:-"us-west-2"}
MAX_PARALLEL=${MAX_PARALLEL:-5}

# Define controllers to soak test: "service:version"
CONTROLLERS=(
  "opensearchserverless:v0.4.3"
  "autoscaling:v0.2.2"
  "emrserverless:v0.2.1"
  "glue:v0.4.1"
  "backup:v0.3.0"
  "quicksight:v0.4.1"
  "mwaa:v0.2.0"
  "firehose:v0.3.1"
  "dsql:v0.1.1"
  "s3files:v0.2.1"
)

LOG_DIR="/tmp/soak-logs"
mkdir -p "$LOG_DIR"

echo "========================================"
echo " ACK Soak Test Batch Runner"
echo " Controllers: ${#CONTROLLERS[@]}"
echo " Duration:    ${TEST_DURATION_DAYS} days"
echo " Parallel:    ${MAX_PARALLEL}"
echo " Logs:        ${LOG_DIR}/"
echo "========================================"
echo ""

running=0
for entry in "${CONTROLLERS[@]}"; do
  svc=$(echo "$entry" | cut -d: -f1)
  ver=$(echo "$entry" | cut -d: -f2)

  echo "[$(date +%H:%M:%S)] Starting: $svc $ver"

  TEST_DURATION_DAYS=$TEST_DURATION_DAYS \
  CONTROLLER_IMAGE_REPO="public.ecr.aws/aws-controllers-k8s/${svc}-controller" \
    "$SOAK_DIR/bootstrap.sh" "$svc" "$ver" > "${LOG_DIR}/${svc}.log" 2>&1 &

  running=$((running + 1))

  # Throttle to MAX_PARALLEL concurrent jobs
  if [ "$running" -ge "$MAX_PARALLEL" ]; then
    wait -n 2>/dev/null || wait  # wait -n requires bash 4.3+; fallback to wait all
    running=$((running - 1))
  fi
done

echo ""
echo "All bootstraps launched. Waiting for remaining jobs to finish..."
wait

echo ""
echo "========================================"
echo " Results"
echo "========================================"
for entry in "${CONTROLLERS[@]}"; do
  svc=$(echo "$entry" | cut -d: -f1)
  if grep -q "Soak test is running" "${LOG_DIR}/${svc}.log" 2>/dev/null; then
    printf "  ✅ %-25s OK\n" "$svc"
  else
    error=$(tail -3 "${LOG_DIR}/${svc}.log" 2>/dev/null | head -1)
    printf "  ❌ %-25s FAILED: %s\n" "$svc" "$error"
  fi
done
echo ""
echo "Logs are in ${LOG_DIR}/"
echo "Run ./dashboards.sh to view all Grafana dashboards."
