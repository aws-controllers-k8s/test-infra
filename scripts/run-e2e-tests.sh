#!/usr/bin/env bash

# ./scripts/run-e2e-tests.sh is the entrypoint for ACK integration testing. It
# ensures that a K8s cluster is acsessible, then configures the ACK controller
# under test onto the cluster before finally running the Python tests.

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

CONTROLLER_NAMESPACE=${CONTROLLER_NAMESPACE:-"ack-system"}

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

VERSION=$(git --git-dir=$SERVICE_CONTROLLER_SOURCE_PATH/.git describe --tags --always --dirty || echo "unknown")
CONTROLLER_IMAGE_TAG="aws-controllers-k8s:${AWS_SERVICE}-${VERSION}"

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

source "$SCRIPTS_DIR/controller-setup.sh"
source "$SCRIPTS_DIR/kind.sh"
source "$SCRIPTS_DIR/pytest-image-runner.sh"

ensure_cluster() {
    local cluster_create="$(get_cluster_create)"
    if [[ "$cluster_create" == true ]]; then
        local cluster_name=$(_get_kind_cluster_name)

        info_msg "Creating KIND cluster ..."
        setup_kind_cluster "$cluster_name" "$CONTROLLER_NAMESPACE"

        build_and_install_controller "$cluster_name" "$CONTROLLER_NAMESPACE" "$CONTROLLER_IMAGE_TAG"
    else
        info_msg "Testing connection to existing cluster ..."
        _ensure_existing_context
    fi
}

ensure_debug_mode() {
    local debug_enabled="$(get_debug_enabled)"
    if [[ "$debug_enabled" == true && ${ACK_TEST_DEBUGGING_MODE-""} == "" ]]; then
        export ACK_TEST_DEBUGGING_MODE="true"
        debug_msg "Debug mode enabled"
    fi
}

build_and_run_tests() {
    local run_locally=$(get_run_tests_locally)
    local test_exit_code=0
    if [[ "$run_locally" == true ]]; then
        local region=$(get_aws_region)
        source "$SCRIPTS_DIR/pytest-local-runner.sh"

        set +e
        AWS_DEFAULT_REGION=$region bootstrap_and_run
        test_exit_code=$?
        set -e
    else
        local image_uuid=$(uuidgen | cut -d'-' -f1 | tr '[:upper:]' '[:lower:]')
        local image_name="ack-test-${AWS_SERVICE}-${image_uuid}"

        build_pytest_image "$image_name"

        set +e
        run_pytest_image "$image_name"
        test_exit_code=$?
        set -e
    fi

    local dump_logs=$(get_dump_controller_logs)
    if [[ "$dump_logs" == "true" ]]; then
        dump_controller_logs "$CONTROLLER_NAMESPACE"
    fi

    return $test_exit_code
}

_ensure_existing_context() {
    debug_msg "Calling kubectl get nodes"
    if ! (kubectl get nodes 1> /dev/null 2>& 1); then
        error_msg "Cannot connect to existing cluster"
        exit 1
    fi
}

run() {
    ensure_aws_credentials
    ensure_cluster
    
    build_and_run_tests
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "kubectl"
    check_is_installed "uuidgen"
}

ensure_debug_mode
ensure_inputs
ensure_binaries

# The purpose of the `return` subshell command in this script is to determine
# whether the script was sourced, or whether it is being executed directly.
# https://stackoverflow.com/a/28776166
(return 0 2>/dev/null) || run