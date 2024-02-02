#!/usr/bin/env bash

# ./scripts/create-dev-cluster.sh quickly setup cluster for ACK development. It
# ensures that a K8s cluster is accessible, then configures the ACK CRD, RBAC
# but not install the controller

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

CONTROLLER_NAMESPACE=${CONTROLLER_NAMESPACE:-"ack-system"}

CLUSTER_NAME="ack-dev-cluster-$AWS_SERVICE"

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

source "$SCRIPTS_DIR/controller-setup.sh"
source "$SCRIPTS_DIR/kind.sh"
source "$SCRIPTS_DIR/run-e2e-tests.sh"

run() {
    ensure_aws_credentials
    ensure_cluster "$CLUSTER_NAME" false
    local kubeconfig_path="$ROOT_DIR/build/clusters/$CLUSTER_NAME/kubeconfig"
    info_msg "After executing the above source command to set the kubeconfig to environment, "
    info_msg "run the following command to start the controller from the controller repo:"
    echo ""
    echo "go run ./cmd/controller/main.go --aws-region $(get_aws_region) --log-level debug --enable-development-logging"
    echo ""
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
