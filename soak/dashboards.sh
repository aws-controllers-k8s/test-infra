#!/usr/bin/env bash

set -o pipefail

USAGE="
Usage:
  $(basename "$0")

Discovers all running ACK soak test clusters (named ack-soak-*), port-forwards
each Grafana dashboard on an auto-assigned port, and prints links to open in
a browser.

Environment variables:
  DEPLOY_REGION:    The AWS region to scan for soak clusters.
                    Default: us-west-2
  BASE_PORT:        Starting port number for Grafana port-forwards.
                    Default: 3000
"

DEPLOY_REGION=${DEPLOY_REGION:-"us-west-2"}
BASE_PORT=${BASE_PORT:-3000}

# Kill any existing port-forwards on our port range to avoid conflicts
pkill -f "kubectl.*port-forward.*grafana" 2>/dev/null || true
sleep 1

# Find all EKS clusters matching the soak naming convention
# Excludes the legacy "ack-soak-test" cluster name
echo "Scanning for soak test clusters in $DEPLOY_REGION ..."
ALL_CLUSTERS=$(aws eks list-clusters --region $DEPLOY_REGION --query "clusters[?starts_with(@, 'ack-soak-')]" --output text 2>/dev/null)
CLUSTERS=""
for c in $ALL_CLUSTERS; do
    # Skip the old hardcoded name
    [ "$c" = "ack-soak-test" ] && continue
    CLUSTERS="$CLUSTERS $c"
done
CLUSTERS=$(echo "$CLUSTERS" | xargs)

if [ -z "$CLUSTERS" ]; then
    echo "No soak test clusters found (looking for clusters named ack-soak-*)."
    exit 0
fi

PORT=$BASE_PORT
echo ""
echo "========================================"
echo " ACK Soak Test Dashboards"
echo "========================================"
echo ""

for CLUSTER in $CLUSTERS; do
    # Extract service name from cluster name (ack-soak-<service>)
    SERVICE=$(echo "$CLUSTER" | sed 's/^ack-soak-//')

    # Update kubeconfig for this cluster
    aws eks update-kubeconfig --name "$CLUSTER" --region "$DEPLOY_REGION" --alias "$CLUSTER" > /dev/null 2>&1

    # Find the Grafana service — try service-specific namespace first, then fallback
    PROM_NS="prometheus-${SERVICE}"
    GRAFANA_SVC=$(kubectl --context "$CLUSTER" get svc -n "$PROM_NS" -o name 2>/dev/null | grep grafana | head -1)

    # Fallback: check the "prometheus" namespace (for clusters created with old naming)
    if [ -z "$GRAFANA_SVC" ]; then
        PROM_NS="prometheus"
        GRAFANA_SVC=$(kubectl --context "$CLUSTER" get svc -n "$PROM_NS" -o name 2>/dev/null | grep grafana | head -1)
    fi

    if [ -z "$GRAFANA_SVC" ]; then
        echo "  ⚠  $CLUSTER: Grafana service not found, skipping"
        echo ""
        continue
    fi

    # Get password from the grafana secret
    SECRET_NAME=$(kubectl --context "$CLUSTER" get secrets -n "$PROM_NS" -o name 2>/dev/null | grep grafana | head -1)
    PASSWORD=""
    if [ -n "$SECRET_NAME" ]; then
        PASSWORD=$(kubectl --context "$CLUSTER" get "$SECRET_NAME" -n "$PROM_NS" -o jsonpath='{.data.admin-password}' 2>/dev/null | base64 -d 2>/dev/null)
    fi
    if [ -z "$PASSWORD" ]; then
        PASSWORD="(could not retrieve)"
    fi

    # Port-forward in background
    kubectl --context "$CLUSTER" port-forward -n "$PROM_NS" "$GRAFANA_SVC" "$PORT:80" --address='0.0.0.0' >/dev/null 2>&1 &

    # Check soak test job status — try service-named job, fallback to listing
    JOB_STATUS=$(kubectl --context "$CLUSTER" get jobs -n ack-system -o jsonpath='{.items[0].status.conditions[0].type}' 2>/dev/null || echo "Running")

    echo "  $SERVICE"
    echo "    Cluster:   $CLUSTER"
    echo "    Dashboard: http://localhost:${PORT}/"
    echo "    Creds:     admin / $PASSWORD"
    echo "    Soak Job:  $JOB_STATUS"
    echo ""

    PORT=$((PORT + 1))
done

echo "========================================"
echo " All dashboards are port-forwarded."
echo " Stop with: pkill -f 'kubectl port-forward.*grafana'"
echo "========================================"
