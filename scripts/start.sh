#!/usr/bin/env bash

# ./scripts/start.sh
# The entrypoint for all of the testing configuration

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=${AWS_SERVICE:-""}

CONTROLLER_NAMESPACE=${CONTROLLER_NAMESPACE:-"ack-system"}

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

source "$SCRIPTS_DIR/controller-setup.sh"
source "$SCRIPTS_DIR/kind-setup.sh"

ensure_cluster() {
    local cluster_create="$(get_cluster_create)"
    if [[ "$cluster_create" == true ]]; then
        local cluster_name=$(_get_kind_cluster_name)

        info_msg "Creating KIND cluster ..."
        setup_kind_cluster $cluster_name $CONTROLLER_NAMESPACE

        build_and_install_controller $cluster_name $CONTROLLER_NAMESPACE
    else
        info_msg "Testing connection to existing cluster ..."
        _ensure_existing_context
    fi
}

ensure_debug_mode() {
    local debug_enabled="$(get_debug_enabled)"
    if [[ "$debug_enabled" == true ]]; then
        export ACK_TEST_DEBUGGING_MODE="true"
        debug_msg "Debug mode enabled"
    fi
}

_ensure_existing_context() {
    debug_msg "Calling kubectl get nodes"
    if ! (kubectl get nodes 1> /dev/null 2>& 1); then
        error_msg "Cannot connect to existing cluster"
        exit 1
    fi
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "kubectl"
}

ensure_debug_mode
ensure_inputs
ensure_binaries

ensure_aws_credentials

ensure_cluster