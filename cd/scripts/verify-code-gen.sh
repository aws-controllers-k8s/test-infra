#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Regenerates controller code for the specified service and verifies that the
generated code matches what is currently committed. This ensures that generated
code has not been manually modified.

Environment variables:
  SERVICE:   The name of the service (e.g., s3, ec2, etc.)
"

# Check if SERVICE is set
if [ -z "$SERVICE" ]; then
    echo "ERROR: SERVICE environment variable must be set"
    echo "$USAGE"
    exit 1
fi

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
SCRIPTS_DIR=$DIR
CD_DIR=$DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CODEGEN_DIR=$WORKSPACE_DIR/code-generator

TARGET_NAME="${SERVICE}-controller"
TARGET_DIR="$WORKSPACE_DIR/$TARGET_NAME"

source "$TEST_INFRA_DIR/scripts/lib/common.sh"
source "$TEST_INFRA_DIR/scripts/lib/logging.sh"

check_is_installed git
check_is_installed make

# Check if the target directory exists
if [ ! -d "$TARGET_DIR" ]; then
    error_msg "Target directory not found at: $TARGET_DIR"
    exit 1
fi

# Check if the code-generator directory exists
if [ ! -d "$CODEGEN_DIR" ]; then
    error_msg "Code generator directory not found at: $CODEGEN_DIR"
    exit 1
fi

info_msg "Verifying generated code for $TARGET_NAME..."

# Regenerate controller code
cd "$CODEGEN_DIR"
info_msg "Regenerating controller code with 'make build-controller SERVICE=$SERVICE'..."

if ! make build-controller SERVICE="$SERVICE"; then
    error_msg "Failed to regenerate controller code."
    exit 1
fi

# Check for differences using git diff
cd "$TARGET_DIR"

info_msg "Checking for differences..."

# Get the diff and filter out ack_generate_info changes (build_date, build_hash, go_version, version)
# These metadata fields always change during regeneration
DIFF_OUTPUT=$(git --no-pager diff | grep -v '^\(---\|+++\) .*ack-generate-metadata.yaml' | \
    grep -vE '^\-  (build_date|build_hash|go_version|version):' | \
    grep -vE '^\+  (build_date|build_hash|go_version|version):' || true)

if [ -z "$DIFF_OUTPUT" ]; then
    info_msg "Success: Generated code matches the committed code for $TARGET_NAME."
    info_msg "No manual modifications detected."
    exit 0
else
    error_msg "Generated code differs from the committed code for $TARGET_NAME."
    error_msg "This indicates that generated code may have been manually modified."
    echo ""
    echo "Differences found:"
    echo "=================="
    echo "$DIFF_OUTPUT"
    echo "=================="
    echo ""
    exit 1
fi
