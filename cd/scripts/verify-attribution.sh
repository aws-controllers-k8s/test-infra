#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Runs attribution-gen on the specified service's or repos go.mod file and compares the output with existing ATTRIBUTION.md file

Environment variables:
  SERVICE:     The name of the service (e.g., s3, ec2, etc....
  REPOSITORY_NAME:   The name of the repository (used if SERVICE is not set)
  DEBUG:       Enable debug mode for attribution-gen (default: false)
  OUTPUT_PATH: Path for the output file (default: ./temp_attribution.md)
"

DEBUG=${DEBUG:-false}
OUTPUT_PATH=${OUTPUT_PATH:-"./temp_attribution.md"}

# Check if either SERVICE or REPOSITORY_NAME is set
if [ -z "$SERVICE" ] && [ -z "$REPOSITORY_NAME" ]; then
    echo "ERROR: Either SERVICE or REPOSITORY_NAME environment variable must be set"
    echo "$USAGE"
    exit 1
fi

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
SCRIPTS_DIR=$DIR
CD_DIR=$DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..

# Determine the target directory based on SERVICE or REPOSITORY_NAME
if [ -n "$SERVICE" ]; then
    TARGET_DIR="$WORKSPACE_DIR/${SERVICE}-controller"
else
    TARGET_DIR="$WORKSPACE_DIR/${REPOSITORY_NAME}"
fi

GOMOD_PATH="$TARGET_DIR/go.mod"

source "$TEST_INFRA_DIR/scripts/lib/common.sh"

error() {
    echo "ERROR: $1" >&2
    exit 1
}

check_is_installed attribution-gen

# Check if the go.mod file exists
if [ ! -f "$GOMOD_PATH" ]; then
    error "go.mod file not found at: $GOMOD_PATH"
fi

GOMOD_PATH=$(realpath "$GOMOD_PATH")
GOMOD_DIR=$(dirname "$GOMOD_PATH")

# Check if ATTRIBUTION.md exists in the same directory as go.mod
if [ ! -f "$GOMOD_DIR/ATTRIBUTION.md" ]; then
    error "ATTRIBUTION.md file not found in the same directory as go.mod"
fi

ATTR_GEN_CMD="attribution-gen --modfile $GOMOD_PATH --output $OUTPUT_PATH"
if [ "$DEBUG" = true ]; then
    ATTR_GEN_CMD+=" --debug"
fi

echo "Running attribution-gen for ${SERVICE:-$REPOSITORY_NAME}..."
$ATTR_GEN_CMD || error "attribution-gen failed to execute successfully."

# Compare the output with the existing ATTRIBUTION.md
if cmp -s "$GOMOD_DIR/ATTRIBUTION.md" "$OUTPUT_PATH"; then
    echo "Success: Generated ATTRIBUTION.md matches the existing file for ${SERVICE:-$REPOSITORY_NAME}."
    rm "$OUTPUT_PATH"
    exit 0
else
    error "Generated ATTRIBUTION.md differs from the existing file for ${SERVICE:-$REPOSITORY_NAME}."
fi