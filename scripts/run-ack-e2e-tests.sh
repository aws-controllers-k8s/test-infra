#!/usr/bin/env bash

# Entrypoint for the `ack` CLI's cluster e2e suite: stands up a KIND cluster, installs a
# released ACK controller chart, and runs the CLI's e2e tests against it. A released chart
# rather than a source build, because the catalog is generated from release tags.

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-s3}" | tr '[:upper:]' '[:lower:]')

ACK_CLI_SOURCE_PATH=${ACK_CLI_SOURCE_PATH:-"$ROOT_DIR/../ackctl"}
CONTROLLER_NAMESPACE=${CONTROLLER_NAMESPACE:-"ack-system"}
CREDS_SECRET_NAME="ack-e2e-creds"
CHART_RELEASE_NAME="ack-$AWS_SERVICE-e2e"

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"
source "$SCRIPTS_DIR/kind.sh"

# The chart tag is the controller release without its leading "v", so the highest tag in the
# registry is the newest release. Read over the anonymous OCI API because the ecr-public
# control plane can only describe repositories in the caller's own registry.
_chart_version() {
    if [[ -n "${CONTROLLER_CHART_VERSION:-}" ]]; then
        echo "$CONTROLLER_CHART_VERSION"
        return
    fi

    local repo="aws-controllers-k8s/$AWS_SERVICE-chart"
    local token
    token=$(curl -fsS \
        "https://public.ecr.aws/token/?scope=repository:$repo:pull&service=public.ecr.aws" |
        jq -r '.token')
    curl -fsS -H "Authorization: Bearer $token" \
        "https://public.ecr.aws/v2/$repo/tags/list" | jq -r '.tags[]' |
        { grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' || :; } | sort -V | tail -1
}

# Writes the environment's credentials as the shared credentials file the charts expect, with
# tracing off so that enabling it later cannot copy the secret key into a public CI log. The
# session token is included because temporary credentials are rejected without it.
_install_credentials() {
    { set +x; } 2>/dev/null
    local creds_file
    creds_file=$(mktemp)
    cat <<EOF >"$creds_file"
[default]
aws_access_key_id = $AWS_ACCESS_KEY_ID
aws_secret_access_key = $AWS_SECRET_ACCESS_KEY
aws_session_token = $AWS_SESSION_TOKEN
EOF
    kubectl -n "$CONTROLLER_NAMESPACE" delete secret "$CREDS_SECRET_NAME" \
        --ignore-not-found 1>/dev/null
    kubectl -n "$CONTROLLER_NAMESPACE" create secret generic "$CREDS_SECRET_NAME" \
        --from-file=credentials="$creds_file" 1>/dev/null
    rm -f "$creds_file"
}

install_released_controller() {
    local chart_version
    chart_version=$(_chart_version) || :
    [[ -z "$chart_version" ]] && {
        error_msg "Could not resolve a released $AWS_SERVICE-chart version"; return 1
    } || :
    local region
    region=$(get_aws_region)

    info_msg "Installing released $AWS_SERVICE controller chart $chart_version ..."
    aws ecr-public get-login-password --region us-east-1 |
        helm registry login --username AWS --password-stdin public.ecr.aws 1>/dev/null

    kubectl create namespace "$CONTROLLER_NAMESPACE" 2>/dev/null || true
    _install_credentials

    helm upgrade --install "$CHART_RELEASE_NAME" \
        "oci://public.ecr.aws/aws-controllers-k8s/$AWS_SERVICE-chart" \
        --version "$chart_version" \
        --namespace "$CONTROLLER_NAMESPACE" \
        --set aws.region="$region" \
        --set aws.credentials.secretName="$CREDS_SECRET_NAME" \
        --set aws.credentials.secretKey=credentials \
        --set aws.credentials.profile=default \
        --set featureGates.ResourceAdoption=true \
        --set featureGates.ReadOnlyResources=true \
        --set log.level=debug \
        --wait --timeout 5m 1>/dev/null

    kubectl -n "$CONTROLLER_NAMESPACE" rollout status deployment \
        --timeout=5m 1>/dev/null
    info_msg "Controller is running"
}

run_cli_tests() {
    local exit_code=0
    pushd "$ACK_CLI_SOURCE_PATH" 1>/dev/null
        set +e
        AWS_REGION=$(get_aws_region) make test-e2e
        exit_code=$?
        set -e
    popd 1>/dev/null
    return $exit_code
}

dump_logs_on_failure() {
    error_msg "e2e tests failed, dumping controller logs ..."
    kubectl -n "$CONTROLLER_NAMESPACE" logs deployment \
        --all-containers --tail=200 2>&1 || true
}

run() {
    ensure_aws_credentials

    local cluster_name
    cluster_name=$(_get_kind_cluster_name)
    info_msg "Creating KIND cluster ..."
    setup_kind_cluster "$cluster_name" "$CONTROLLER_NAMESPACE"

    install_released_controller

    local exit_code=0
    run_cli_tests || exit_code=$?
    [[ $exit_code -ne 0 ]] && dump_logs_on_failure
    exit $exit_code
}

ensure_inputs() {
    [[ ! -d "$ACK_CLI_SOURCE_PATH" ]] && {
        error_msg "Expected the ack CLI checkout at $ACK_CLI_SOURCE_PATH"; exit 1
    } || :
}

ensure_binaries() {
    check_is_installed "kubectl"
    check_is_installed "helm"
    check_is_installed "go"
}

ensure_inputs
ensure_binaries

(return 0 2>/dev/null) || run
