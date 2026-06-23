#!/bin/bash
set -o pipefail

SOAK_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

for svc in autoscaling backup dsql emrserverless firehose glue mwaa opensearchserverless quicksight s3files; do
  echo "Starting teardown: $svc"
  "$SOAK_DIR/teardown.sh" "$svc" > "/tmp/teardown-${svc}.log" 2>&1 &
done

echo "All teardowns launched. Waiting for completion..."
wait
echo ""
echo "=== Results ==="
for svc in autoscaling backup dsql emrserverless firehose glue mwaa opensearchserverless quicksight s3files; do
  result=$(tail -5 "/tmp/teardown-${svc}.log" 2>/dev/null | grep -o "Teardown complete\|does not exist" | head -1)
  printf "%-25s %s\n" "$svc" "${result:-CHECK LOG}"
done
