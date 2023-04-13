#!/usr/bin/env bash

# ./scripts/run-dev.sh quickly setup cluster for ACK development. It
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

ensure_cluster() {
      info_msg "Creating KIND cluster ..."
      setup_kind_cluster "$CLUSTER_NAME" "$CONTROLLER_NAMESPACE"

      info_msg "Installing CRDs , common and RBAC manifest..."
      install_crd_and_rbac "$CONTROLLER_NAMESPACE"
}

run() {
    ensure_aws_credentials
    ensure_cluster
    local kubeconfig_path="$ROOT_DIR/build/clusters/$CLUSTER_NAME/kubeconfig"
    info_msg "Before running the controller, you need kubeconfig and aws credentials."
    info_msg "After executing the above source command to set the kubeconfig to environment, "
    info_msg "run the following command to start the controller from the controller repo:"
    echo ""
    echo "go run ./cmd/controller/main.go --aws-region eu-central-1 --log-level debug --enable-development-logging"
    echo ""
    info_msg "if you run the controller from your code editor/IDE, you can set the following environment variables:"
    echo "KUBECONFIG=$kubeconfig_path"
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
