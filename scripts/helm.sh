#!/usr/bin/env bash

# helm.sh contains functions used to run the ACK Helm tests

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

HELM_DIR="$SERVICE_CONTROLLER_SOURCE_PATH/helm"

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

source "$SCRIPTS_DIR/controller-setup.sh"

_assert_pod_running() {
    local __chart_namespace=$1

    local controller_pod_name=$(kubectl get pods -n $__chart_namespace -ojson | jq -r ".items[0].metadata.name")
    [[ -z "$controller_pod_name" ]] && { error_msg "Unable to find controller pod"; return 1; }

    debug_msg "ACK $AWS_SERVICE controller pod name is $__chart_namespace/$controller_pod_name"
    info_msg "Verifying that pod status is in Running state ... "

    local pod_status=$(kubectl get pod/"$controller_pod_name" -n $__chart_namespace -ojson | jq -r ".status.phase")
    [[ $pod_status != Running ]] && { error_msg "Pod is in status '$pod_status'. Expected 'Running' "; return 1; }

    info_msg "Verifying that there are no ERROR in controller logs ... "

    local controller_logs=$(kubectl logs pod/"$controller_pod_name" -n $__chart_namespace)
    [[ -z "$controller_logs" ]] && { error_msg "Unable to find controller logs"; return 1; }

    if (echo "$controller_logs" | grep -q "ERROR"); then
        error_msg "Found following ERROR statements in controller logs:"
        error_msg "$(echo $controller_logs | grep "ERROR")"
        return 1
    fi
}

run_helm_tests() {
    local __chart_namespace=$1

    _assert_pod_running "$__chart_namespace"
}

ensure_helm_directory_exists() {
    [[ ! -d "$HELM_DIR" ]] && { error_msg "Helm directory does not exist for the service controller "; exit 1; } || :
    [[ ! -f "$HELM_DIR/Chart.yaml" ]] && { error_msg "Helm chart does not exist for the service controller "; exit 1; } || :
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "helm"
    check_is_installed "jq"
    check_is_installed "kubectl"
}

ensure_inputs
ensure_binaries