#!/usr/bin/env bash

# ./scripts/run-e2e-tests.sh is the entrypoint for ACK integration testing. It
# ensures that a K8s cluster is acsessible, then configures the ACK controller
# under test onto the cluster before finally running the Python tests.

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

CONTROLLER_NAMESPACE=${CONTROLLER_NAMESPACE:-"ack-system"}

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

source "$SCRIPTS_DIR/controller-setup.sh"
source "$SCRIPTS_DIR/kind.sh"

ensure_cluster() {
    local cluster_create="$(get_cluster_create)"
    if [[ "$cluster_create" == true ]]; then
        local cluster_name="ack-dev-cluster-$AWS_SERVICE"

        info_msg "Creating KIND cluster ..."
        setup_kind_cluster "$cluster_name" "$CONTROLLER_NAMESPACE"

        info_msg "Installing CRDs and RBAC "
        install_crd_and_rbac "$CONTROLLER_NAMESPACE"
    else
        info_msg "Testing connection to existing cluster ..."
        _ensure_existing_context
    fi
}

run() {
    ensure_aws_credentials
    ensure_cluster
    exit $?
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "kubectl"
}

ensure_inputs
ensure_binaries

# The purpose of the `return` subshell command in this script is to determine
# whether the script was sourced, or whether it is being executed directly.
# https://stackoverflow.com/a/28776166
(return 0 2>/dev/null) || run
