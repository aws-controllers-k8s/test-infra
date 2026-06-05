#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Regenerates controllers using the PR's code-generator and checks CRD files
for breaking schema changes against each controller's main branch.

Environment variables:
  SERVICES:   Space-separated list of AWS services to check (e.g., 's3 ecr iam')
"

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
SCRIPTS_DIR=$DIR
CD_DIR=$DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CODEGEN_DIR=$WORKSPACE_DIR/code-generator

source "$TEST_INFRA_DIR/scripts/lib/common.sh"
source "$TEST_INFRA_DIR/scripts/lib/logging.sh"

check_is_installed git
check_is_installed make

if [ -z "$SERVICES" ]; then
    error_msg "SERVICES environment variable must be set"
    echo "$USAGE"
    exit 1
fi

if [ ! -d "$CODEGEN_DIR" ]; then
    error_msg "Code generator directory not found at: $CODEGEN_DIR"
    exit 1
fi

info_msg "Building ack-generate..."
make -C "$CODEGEN_DIR" build-ack-generate

FAILED_SERVICES=()

for SERVICE in $SERVICES; do
    CONTROLLER_DIR="$WORKSPACE_DIR/${SERVICE}-controller"

    if [ ! -d "$CONTROLLER_DIR" ]; then
        error_msg "Controller directory not found: $CONTROLLER_DIR"
        FAILED_SERVICES+=("$SERVICE")
        continue
    fi

    info_msg "Regenerating CRDs for $SERVICE..."
    if ! SERVICE="$SERVICE" make -C "$CODEGEN_DIR" build-controller; then
        error_msg "Failed to regenerate controller for $SERVICE"
        FAILED_SERVICES+=("$SERVICE")
        continue
    fi

    info_msg "Checking CRD compatibility for $SERVICE..."
    pushd "$CONTROLLER_DIR" > /dev/null
    if ! "$CODEGEN_DIR/bin/ack-generate" crd-compat-check --base-ref="main"; then
        error_msg "CRD compatibility check FAILED for $SERVICE"
        FAILED_SERVICES+=("$SERVICE")
    else
        info_msg "CRD compatibility check passed for $SERVICE"
    fi
    popd > /dev/null
done

if [ ${#FAILED_SERVICES[@]} -ne 0 ]; then
    error_msg "CRD compatibility check failed for: ${FAILED_SERVICES[*]}"
    exit 1
fi

info_msg "All CRD compatibility checks passed."
