#!/usr/bin/env bash

# ./scripts/run-helm-tests.sh is the entrypoint for ACK Helm test suite. It
# ensures that a K8s cluster is acsessible, then applies the Helm chart in the
# service controller directory to the cluster before validating that the 
# pods are able to run successfully.

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

VERSION=$(git --git-dir=$SERVICE_CONTROLLER_SOURCE_PATH/.git describe --tags --always --dirty || echo "unknown")
CONTROLLER_IMAGE_TAG="aws-controllers-k8s:${AWS_SERVICE}-${VERSION}"

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

source "$SCRIPTS_DIR/helm.sh"
source "$SCRIPTS_DIR/start.sh"

install_chart_and_run_tests() {
    local __chart_namespace=$1
    local __image_tag=$2

    local chart_name="ack-$AWS_SERVICE-helm-test"

    # Cut the repository and tag out of the build tag
    local image_repo=$(echo "$__image_tag" | cut -d":" -f1)
    local image_tag=$(echo "$__image_tag" | cut -d":" -f2)

    info_msg "Installing the controller Helm chart ..."
    _cleanup_helm_chart "$__chart_namespace" "$chart_name"
    _helm_install "$__chart_namespace" "$chart_name" "$image_repo" "$image_tag"

    # Wait for the controller to start
    sleep 10
    info_msg "Running Helm chart tests ..." 
    set +e

    run_helm_tests "$__chart_namespace"
    local test_exit_code=$?

    set -e

    info_msg "Cleaning up Helm chart ..."
    _cleanup_helm_chart "$__chart_namespace" "$chart_name"
    return $test_exit_code
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