#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Checks CRD files for breaking schema changes against the base git ref.
Uses kro's CRD compatibility checker via ack-generate crd-compat-check.

Environment variables:
  PULL_BASE_SHA:  Git ref to compare against (set by Prow, defaults to 'main')
"

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
SCRIPTS_DIR=$DIR
CD_DIR=$DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CODEGEN_DIR=$WORKSPACE_DIR/code-generator

BASE_REF="${PULL_BASE_SHA:-main}"

source "$TEST_INFRA_DIR/scripts/lib/common.sh"
source "$TEST_INFRA_DIR/scripts/lib/logging.sh"

check_is_installed git
check_is_installed make

if [ ! -d "$CODEGEN_DIR" ]; then
    error_msg "Code generator directory not found at: $CODEGEN_DIR"
    exit 1
fi

info_msg "Building ack-generate..."
make -C "$CODEGEN_DIR" build-ack-generate

info_msg "Checking CRD compatibility against $BASE_REF..."
"$CODEGEN_DIR/bin/ack-generate" crd-compat-check --base-ref="$BASE_REF"
