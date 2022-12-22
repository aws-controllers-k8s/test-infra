#!/usr/bin/env bash

# kind.sh contains functions used to setup a KIND cluster and install 
# a number of additional ACK service controllers (from their respective Helm 
# repositories).

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

setup_kind_cluster() {
    local __cluster_name=$1
    local __controller_namespace=$2
    local kind_cluster_build_dir="$ROOT_DIR/build/clusters/$__cluster_name"
    local kubeconfig_path="$kind_cluster_build_dir/kubeconfig"
    local kubecontext="kind-$__cluster_name"
    local kubecontext_path="$kind_cluster_build_dir/kubecontext"

    info_msg "Creating cluster with name \"$__cluster_name\""
    _create_kind_cluster "$__cluster_name" "$kubeconfig_path"

    debug_msg "Creating kubecontext file $kubecontext_path ... "
    cat <<EOF > "$kubecontext_path"
export KUBECONFIG="$kubeconfig_path"
export KUBECONTEXT="$kubecontext"
kubectl config use-context "$kubecontext"
kubectl config set-context --current --namespace=ack-system

controller_pod() {
  local __service_name="\$1"
  if [ -z "\$__service_name" ]; then
    echo "Call controller_pod with the name of the service controller you wish to"
    echo "find an ID for. For example: controller_pod s3"
    return
  fi
  echo \$(kubectl get pods -o json | jq -r ".items[] | select(.metadata.name | test(\"ack-\$__service_name-controller\")).metadata.name")
}
EOF
    info_msg ""
    info_msg "*******************************************"
    info_msg "* To execute kubectl in the test context: *"
    info_msg "*******************************************"
    echo ""
    echo "source $kubecontext_path"
    echo ""

    export KUBECONFIG="$kubeconfig_path"
    _install_additional_controllers "$__controller_namespace"
}

_create_kind_cluster() {
    local __cluster_name=$1
    local __kubeconfig_path=$2

    local config_file_name="$(get_cluster_configuration_file_name)"
    local cluster_version="$(get_cluster_k8s_version)"

    local config_file_path=$SCRIPTS_DIR/kind-configurations/$config_file_name
    
    info_msg "Using configuration \"$config_file_name\""
    debug_msg "Using K8s version \"$cluster_version\""

    for i in $(seq 0 3); do
        if [[ -z $(kind get clusters 2>/dev/null | grep "$__cluster_name") ]]; then
            kind create cluster --name "$__cluster_name" \
                ${cluster_version:+ --image kindest/node:v$cluster_version} \
                --config "$config_file_path" \
                --kubeconfig $__kubeconfig_path 1>&2 || :
        else
            break
        fi
    done
}

_get_kind_cluster_name() {
    local cluster_name=$(get_cluster_name)

    if [[ "$cluster_name" == "" ]]; then
        local name_uuid=$(uuidgen | cut -d'-' -f1 | tr '[:upper:]' '[:lower:]')
        cluster_name="ack-test-${name_uuid}"
    fi
    echo $cluster_name
}

_install_additional_controllers() {
    local __controller_namespace=$1

    local install_region=$(get_aws_region)
    local additional_controllers=( $(get_cluster_additional_controllers | yq -o=j -I=0 '.[]' -) )
    for controller_version_pair in "${additional_controllers[@]}"; do
        local controller_name=$(echo $controller_version_pair | tr -d '"' | cut -d "@" -f 1)
        local controller_version=$(echo $controller_version_pair | tr -d '"' | cut -d "@" -f 2)

        # Strip the `-controller` from the name
        local controller_service=$(echo $controller_name | sed 's/-controller//')

        if (_is_additional_controller_installed "$controller_service" "$__controller_namespace"); then
            info_msg "$controller_name already installed. Skipping"
        else
            info_msg "Installing the $controller_name"
            _install_additional_controller "$controller_service" "$controller_version" "$install_region" "$__controller_namespace"
        fi
    done
}

_is_additional_controller_installed() {
    local __controller_service=$1
    local __controller_namespace=$2
    
    local exists="$(helm list -q -n "$__controller_namespace" --filter "$__controller_service-chart+" 2>/dev/null)"
    [[ "$exists" == "" ]] && return 1 || return 0
}

_install_additional_controller() {
    local __controller_service=$1
    local __controller_version=$2
    local __region=$3
    local __controller_namespace=$4

    debug_msg "Logging into the Helm registry"
    _perform_helm_login

    helm install --create-namespace -n "$__controller_namespace" \
        oci://public.ecr.aws/aws-controllers-k8s/$__controller_service-chart \
        --version=$__controller_version --generate-name --set=aws.region=$__region
}

_perform_helm_login() {
  # ECR Public only exists in us-east-1 so use that region specifically
  daws ecr-public get-login-password --region us-east-1 | helm registry login -u AWS --password-stdin public.ecr.aws
}

ensure_binaries() {
    check_is_installed "helm"
    check_is_installed "kind"
    check_is_installed "uuidgen"
}

ensure_binaries
