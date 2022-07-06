#!/usr/bin/env bash

# ./scripts/start-helm-tests.sh is the entrypoint for ACK Helm test suite. It
# ensures that a K8s cluster is acsessible, then applies the Helm chart in the
# service controller directory to the cluster before validating that the 
# pods are able to run successfully.

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

VERSION=$(git --git-dir=$SERVICE_CONTROLLER_SOURCE_PATH/.git describe --tags --always --dirty || echo "unknown")
CONTROLLER_IMAGE_TAG="aws-controllers-k8s:${AWS_SERVICE}-${VERSION}"

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

source "$SCRIPTS_DIR/start.sh"

run() {
    ensure_aws_credentials

    ensure_cluster

    local helm_test_namespace="$AWS_SERVICE-test"
    install_chart_and_run_tests "$helm_test_namespace" "$CONTROLLER_IMAGE_TAG"
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_debug_mode
ensure_inputs

# The purpose of the `return` subshell command in this script is to determine
# whether the script was sourced, or whether it is being executed directly.
# https://stackoverflow.com/a/28776166
(return 0 2>/dev/null) || run