#!/usr/bin/env bash

# controller-setup.sh contains functions used to build an ACK service controller
# container image and to run it within a K8s cluster.

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

CODE_GENERATOR_SCRIPTS_DIR="$ROOT_DIR/../code-generator/scripts"

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

build_and_install_controller() {
    local __cluster_name=$1
    local __controller_namespace=$2
    local __img_name=$3    

    info_msg "Building controller image ... "
    _build_controller_image "$__img_name"

    info_msg "Loading image into cluster ... "
    _load_controller_image "$__cluster_name" "$__img_name"

    info_msg "Installing controller deployment ... "
    _install_deployment "$__controller_namespace" "$__img_name"
}

_build_controller_image() {
    local __img_name=$1

    local local_build="$(get_is_local_build)"
    LOCAL_MODULES="$local_build" AWS_SERVICE_DOCKER_IMG="$__img_name" ${CODE_GENERATOR_SCRIPTS_DIR}/build-controller-image.sh ${AWS_SERVICE} 1>/dev/null
}

_load_controller_image() {
    local __cluster_name=$1
    local __img_name=$2

    kind load docker-image --name "$__cluster_name" --nodes="$__cluster_name"-worker,"$__cluster_name"-control-plane "$__img_name"
}

_install_deployment() {
    local __controller_namespace=$1
    local __img_name=$2

    local service_controller_source_dir="$ROOT_DIR/../$AWS_SERVICE-controller"
    local service_config_dir="$service_controller_source_dir/config"
    local test_config_dir="$ROOT_DIR/build/clusters/$cluster_name/config/test"

    # Register the ACK service controller's CRDs in the target k8s cluster
    debug_msg "Loading CRD manifests for $AWS_SERVICE into the cluster ... "
    for crd_file in $service_config_dir/crd/bases; do
        kubectl apply -f "$crd_file" --validate=false 1>/dev/null
    done

    debug_msg "Loading common manifests into the cluster ... "
    for crd_file in $service_config_dir/crd/common/bases; do
        kubectl apply -f "$crd_file" --validate=false 1>/dev/null
    done

    debug_msg "Creating $__controller_namespace namespace"
    kubectl create namespace $__controller_namespace 2>/dev/null || true

    debug_msg "Loading RBAC manifests for $AWS_SERVICE into the cluster ... "
    kustomize build "$service_config_dir"/rbac | kubectl apply -f - 1>/dev/null

    # Create the ACK service controller Deployment in the target k8s cluster
    mkdir -p "$test_config_dir"

    cp "$service_config_dir"/controller/deployment.yaml "$test_config_dir"/deployment.yaml
    cp "$service_config_dir"/controller/service.yaml "$test_config_dir"/service.yaml

    cat <<EOF >"$test_config_dir"/kustomization.yaml
resources:
- deployment.yaml
- service.yaml
EOF

    debug_msg "Loading service controller Deployment for $AWS_SERVICE into the cluster ..."
    pushd $test_config_dir 2>/dev/null 1>& 2
    kustomize edit set image controller="$__img_name"
    popd 2>/dev/null 1>& 2
    kustomize build "$test_config_dir" | kubectl apply -f - 1>/dev/null

    # Generate and pass temporary credentials to controller
    debug_msg "Generating AWS temporary credentials and adding to env vars map ... "
    aws_generate_temp_creds

    local region=$(get_aws_region)

    kubectl -n $__controller_namespace set env deployment/ack-"$AWS_SERVICE"-controller \
        AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
        AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
        AWS_SESSION_TOKEN="$AWS_SESSION_TOKEN" \
        ACK_ENABLE_DEVELOPMENT_LOGGING="true" \
        ACK_LOG_LEVEL="debug" \
        AWS_REGION="$region" 1>/dev/null
        # TODO: Support watch namespace configuration
        # ACK_WATCH_NAMESPACE="$ACK_WATCH_NAMESPACE" \

    # Static sleep to ensure controller is up and running
    sleep 5

    local dump_logs=$(get_dump_controller_logs)

    trap 'kill $(jobs -p)' EXIT SIGINT
    _loop_rotate_temp_creds 3000 "$__controller_namespace" "ack-$AWS_SERVICE-controller" "$dump_logs" &
}

dump_controller_logs() {
    local __controller_namespace=$1

    debug_msg "Dumping controller logs"

    # ARTIFACTS will be defined by Prow
    if [[ ! -d $ARTIFACTS ]]; then
        error_msg "Error evaluating ARTIFACTS environment variable" 
        error_msg "Skipping controller logs capture"
    else
        # Use the first pod in the `ack-system` namespace
        POD=$(kubectl get pods -n $__controller_namespace -o name | grep $AWS_SERVICE-controller | head -n 1)
        kubectl logs -n $__controller_namespace $POD >> $ARTIFACTS/controller_logs
    fi
}

_loop_rotate_temp_creds() {
    local __rotation_time_in_seconds=$1
    local __controller_namespace=$2
    local __deployment_name=$3
    local __dump_logs=$4
    
    while true; do
        info_msg "Sleeping for 50 mins before rotating temporary aws credentials"
        sleep $__rotation_time_in_seconds & wait

        rotate_temp_creds "$__controller_namespace" "$__deployment_name" "$__dump_logs"
    done
}

rotate_temp_creds() {
    local __controller_namespace=$1
    local __deployment_name=$2
    local __dump_logs=$3

    if [[ "$__dump_logs" == true ]]; then
        dump_controller_logs "$__controller_namespace"
    fi

    aws_generate_temp_creds

    kubectl -n $__controller_namespace set env deployment/$__deployment_name \
        AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
        AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
        AWS_SESSION_TOKEN="$AWS_SESSION_TOKEN"  1>/dev/null

    kubectl -n $__controller_namespace rollout restart deployment $__deployment_name >/dev/null
    info_msg "Successfully rotated AWS credentials and restarted controller deployment"
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "kind"
    check_is_installed "kubectl"
    check_is_installed "kustomize"
}

ensure_inputs
ensure_binaries