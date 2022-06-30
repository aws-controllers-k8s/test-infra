#!/usr/bin/env bash

# helm-image-runner.sh contains functions used to run the ACK Helm tests

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

install_chart_and_run_tests() {
    local __chart_namespace=$1
    local __image_tag=$2

    local chart_name="ack-$AWS_SERVICE-helm-test"

    # Cut the repository and tag out of the build tag
    local image_repo=$(echo "$__image_tag" | cut -d":" -f1)
    local image_tag=$(echo "$__image_tag" | cut -d":" -f2)

    info_msg "Installing the controller Helm charts ..."
    _cleanup_helm_chart "$__chart_namespace" "$chart_name"
    _helm_install "$__chart_namespace" "$chart_name" "$image_repo" "$image_tag"

    # Wait for the controller to start
    sleep 10
    info_msg "Running Helm chart tests ..."
    _assert_pod_running "$__chart_namespace"

    _cleanup_helm_chart "$__chart_namespace" "$chart_name"
}

_helm_install() {
    local __chart_namespace=$1
    local __chart_name=$2
    local __image_repository=$3
    local __image_tag=$4
    
    local region=$(get_aws_region)

    helm install --create-namespace \
        --namespace "$__chart_namespace" \
        --set aws.region="$region" \
        --set image.repository="$__image_repository" \
        --set image.tag="$__image_tag" \
        --set installScope="namespace" \
        --set metrics.service.create=true \
        "$__chart_name" $HELM_DIR

    local controller_deployment_name=$(kubectl get deployments -n $__chart_namespace -ojson | jq -r ".items[0].metadata.name")
    [[ -z "$controller_deployment_name" ]] && { error_msg "Unable to find Helm deployment"; exit 1; }

    rotate_temp_creds "$__chart_namespace" "$controller_deployment_name" false
}

_cleanup_helm_chart() {
    local __chart_namespace=$1
    local __chart_name=$2

    set +e
    helm uninstall --namespace "$__chart_namespace" "$__chart_name" > /dev/null 2>&1

    kubectl delete namespace "$__chart_namespace" > /dev/null 2>&1
    kubectl delete clusterrolebinding  "ack-$AWS_SERVICE-controller-rolebinding" > /dev/null 2>&1
    kubectl delete clusterrole  "ack-$AWS_SERVICE-controller" > /dev/null 2>&1
    set -e
}

_assert_pod_running() {
    local __chart_namespace=$1

    local controller_pod_name=$(kubectl get pods -n $__chart_namespace -ojson | jq -r ".items[0].metadata.name")
    [[ -z "$controller_pod_name" ]] && { error_msg "Unable to find controller pod"; exit 1; }

    debug_msg "ACK $AWS_SERVICE controller pod name is $__chart_namespace/$controller_pod_name"
    info_msg "Verifying that pod status is in Running state ... "

    local pod_status=$(kubectl get pod/"$controller_pod_name" -n $__chart_namespace -ojson | jq -r ".status.phase")
    [[ $pod_status != Running ]] && { error_msg "Pod is in status '$pod_status'. Expected 'Running' "; exit 1; }

    info_msg "Verifying that there are no ERROR in controller logs ... "

    local controller_logs=$(kubectl logs pod/"$controller_pod_name" -n $__chart_namespace)
    [[ -z "$controller_logs" ]] && { error_msg "Unable to find controller logs"; exit 1; }

    if (echo "$controller_logs" | grep -q "ERROR"); then
        error_msg "Found following ERROR statements in controller logs:"
        error_msg "$(echo $controller_logs | grep "ERROR")"
        exit 1
    fi
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