#!/usr/bin/env bash

# A script that provisions a KinD Kubernetes cluster for local development and
# testing

set -eo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

ENABLE_PROMETHEUS=${ENABLE_PROMETHEUS:-"false"}

source "$SCRIPTS_DIR"/lib/common.sh

check_is_installed uuidgen
check_is_installed wget
check_is_installed docker
check_is_installed kind "You can install kind with the helper scripts/install-kind.sh"

if [[ "$ENABLE_PROMETHEUS" == true ]]; then
    KIND_CONFIG_FILE="$SCRIPTS_DIR/kind-two-node-prometheus-cluster.yaml"
else
    KIND_CONFIG_FILE="$SCRIPTS_DIR/kind-two-node-cluster.yaml"
fi

K8_1_24="kindest/node:v1.24.0"
K8_1_23="kindest/node:v1.23.6"
K8_1_22="kindest/node:v1.22.9"
K8_1_21="kindest/node:v1.21.12"
K8_1_20="kindest/node:v1.20.15"
K8_1_19="kindest/node:v1.19.16"

USAGE="
Usage:
  $(basename "$0") CLUSTER_NAME

Provisions a KinD cluster for local development and testing.

Example: $(basename "$0") my-test

Environment variables:
  K8S_VERSION               Kubernetes Version [1.19, 1.20, 1.21, 1.22, 1.23 and 1.24]
                            Default: 1.22
  ENABLE_PROMETHEUS:        Enables a different cluster config to enable Prometheus support.
                            Default: false
"

cluster_name="$1"
if [[ -z "$cluster_name" ]]; then
    echo "FATAL: required cluster name argument missing."
    echo "${USAGE}" 1>&2
    exit 1
fi

# Process K8S_VERSION env var

if [ ! -z ${K8S_VERSION} ]; then
    K8_VER="K8_$(echo "${K8S_VERSION}" | sed 's/\./\_/g')"
    K8_VERSION=${!K8_VER}

    # Check if version is supported:
    if [ -z $K8S_VERSION ]; then
        echo "Version set: $K8_VER"
        echo "K8s version not supported" 1>&2
        exit 2
    fi
else
    K8_VERSION=${K8_1_22}
fi

TMP_DIR=$ROOT_DIR/build/tmp-$cluster_name
mkdir -p "${TMP_DIR}"

debug_msg "kind: using Kubernetes $K8_VERSION"
echo -n "creating kind cluster $cluster_name ... "
for i in $(seq 0 5); do
  if [[ -z $(kind get clusters 2>/dev/null | grep $cluster_name) ]]; then
      kind create cluster --name "$cluster_name" --image $K8_VERSION --config "$KIND_CONFIG_FILE" --kubeconfig $TMP_DIR/kubeconfig 1>&2 || :
  else
      break
  fi
done
echo "ok."

echo "$cluster_name" > $TMP_DIR/clustername
