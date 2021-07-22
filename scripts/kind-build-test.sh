#!/usr/bin/env bash

# A script that builds a single ACK service controller, provisions a KinD
# Kubernetes cluster, installs the built ACK service controller into that
# Kubernetes cluster and runs a set of tests

set -Eeo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

CLUSTER_NAME_BASE=${CLUSTER_NAME_BASE:-"test"}
AWS_ACCOUNT_ID=${AWS_ACCOUNT_ID:-""}
AWS_REGION=${AWS_REGION:-"us-west-2"}
ACK_ROLE_ARN=${ACK_ROLE_ARN:-""}
ACK_ENABLE_DEVELOPMENT_LOGGING="true"
ACK_WATCH_NAMESPACE=${ACK_WATCH_NAMESPACE:-""}
ENABLE_PROMETHEUS=${ENABLE_PROMETHEUS:-"false"}
TEST_HELM_CHARTS=${TEST_HELM_CHARTS:-"true"}
SKIP_PYTHON_TESTS=${SKIP_PYTHON_TESTS:-"false"}
RUN_PYTEST_LOCALLY=${RUN_PYTEST_LOCALLY:="false"}
ACK_LOG_LEVEL="debug"
ACK_RESOURCE_TAGS='services.k8s.aws/managed=true, services.k8s.aws/created=%UTCNOW%, services.k8s.aws/namespace=%KUBERNETES_NAMESPACE%'
DELETE_CLUSTER_ARGS=""
K8S_VERSION=${K8S_VERSION:-"1.16"}
PRESERVE=${PRESERVE:-"false"}
LOCAL_MODULES=${LOCAL_MODULES:-"false"}
START=$(date +%s)
# VERSION is the source revision that executables and images are built from.
VERSION=$(git describe --tags --always --dirty || echo "unknown")

ARTIFACTS=${ARTIFACTS:-""}
DUMP_CONTROLLER_LOGS=${DUMP_CONTROLLER_LOGS:-"false"}

DEFAULT_CODE_GENERATOR_SOURCE_PATH="$ROOT_DIR/../code-generator"
CODE_GENERATOR_SOURCE_PATH=${CODE_GENERATOR_SOURCE_PATH:-$DEFAULT_CODE_GENERATOR_SOURCE_PATH}
CODE_GENERATOR_SCRIPTS_DIR="$CODE_GENERATOR_SOURCE_PATH/scripts"

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/aws.sh"

check_is_installed curl
check_is_installed docker
check_is_installed jq
check_is_installed uuidgen
check_is_installed wget
check_is_installed kind "You can install kind with the helper scripts/install-kind.sh"
check_is_installed kubectl "You can install kubectl with the helper scripts/install-kubectl.sh"
check_is_installed kustomize "You can install kustomize with the helper scripts/install-kustomize.sh"

aws_check_credentials

if [ "z$AWS_ACCOUNT_ID" == "z" ]; then
    AWS_ACCOUNT_ID=$( aws_account_id )
fi

function clean_up {
    if [[ "$PRESERVE" == false ]]; then
        "${SCRIPTS_DIR}"/delete-kind-cluster.sh "$TMP_DIR" || :
        return
    fi
    echo "To resume test with the same cluster use: \" TMP_DIR=$TMP_DIR
    AWS_SERVICE_DOCKER_IMG=$AWS_SERVICE_DOCKER_IMG \""""
}


USAGE="
Usage:
  export ACK_ROLE_ARN=\"\$ROLE_ARN\"
  $(basename "$0") <AWS_SERVICE>

Builds the Docker image for an ACK service controller, loads the Docker image
into a KinD Kubernetes cluster, creates the Deployment artifact for the ACK
service controller and executes a set of tests.

Example: export ACK_ROLE_ARN=\"\$ROLE_ARN\"; $(basename "$0") ecr

<AWS_SERVICE> should be an AWS Service name (ecr, sns, sqs, petstore, bookstore)

Environment variables:
  SERVICE_CONTROLLER_SOURCE_PATH: Path to the service controller source code
                            repository.
                            Default: ../{SERVICE}-controller
  ACK_ROLE_ARN:             Provide AWS Role ARN for functional testing on local KinD Cluster. Mandatory.
  PRESERVE:                 Preserve kind k8s cluster for inspection (<true|false>)
                            Default: false
  LOCAL_MODULES:            Enables use of local modules during AWS Service controller docker image build
                            Default: false
  AWS_SERVICE_DOCKER_IMG:   Provide AWS Service docker image
                            Default: aws-controllers-k8s:$AWS_SERVICE-$VERSION
  TMP_DIR                   Cluster context directory, if operating on an existing cluster
                            Default: $ROOT_DIR/build/tmp-$CLUSTER_NAME
  K8S_VERSION               Kubernetes Version [1.14, 1.15, 1.16, 1.17, and 1.18]
                            Default: 1.16
  TEST_HELM_CHARTS          Whether to run the test-helm.sh script (<true|false>)
                            Default: true
  SKIP_PYTHON_TESTS         Whether to skip python tests and run bash tests instead for
                            the service controller (<true|false>)
                            Default: false
  RUN_PYTEST_LOCALLY        If python tests exist, whether to run them locally instead of
                            inside Docker (<true|false>)
                            Default: false
  ENABLE_PROMETHEUS:        Enables a different cluster config to enable Prometheus support.
                            Default: false
  DUMP_CONTROLLER_LOGS:     Whether to dump the controller pod logs to a local file after finishing tests.
                            Default: false
"

if [ $# -ne 1 ]; then
    echo "AWS_SERVICE is not defined. Script accepts one parameter, <AWS_SERVICE> to build that docker images of that service and load into Kind" 1>&2
    echo "${USAGE}"
    exit 1
fi

AWS_SERVICE=$(echo "$1" | tr '[:upper:]' '[:lower:]')

# Source code for the controller will be in a separate repo, typically in
# $GOPATH/src/github.com/aws-controllers-k8s/$AWS_SERVICE-controller/
DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

if [[ ! -d $SERVICE_CONTROLLER_SOURCE_PATH ]]; then
    echo "Error evaluating SERVICE_CONTROLLER_SOURCE_PATH environment variable:" 1>&2
    echo "$SERVICE_CONTROLLER_SOURCE_PATH is not a directory." 1>&2
    echo "${USAGE}"
    exit 1
fi

if [ -z "$ACK_ROLE_ARN" ]; then
    echo "ACK_ROLE_ARN is not defined. Set <ACK_ROLE_ARN> env variable to indicate the ARN of the IAM Role to use in testing"
    echo "${USAGE}"
    exit  1
fi

if [ -z "$TMP_DIR" ]; then
    TEST_ID=$(uuidgen | cut -d'-' -f1 | tr '[:upper:]' '[:lower:]')
    CLUSTER_NAME_BASE=$(uuidgen | cut -d'-' -f1 | tr '[:upper:]' '[:lower:]')
    CLUSTER_NAME="ack-test-$CLUSTER_NAME_BASE"-"${TEST_ID}"
    TMP_DIR=$ROOT_DIR/build/tmp-$CLUSTER_NAME
    K8S_VERSION="$K8S_VERSION" ENABLE_PROMETHEUS="$ENABLE_PROMETHEUS" $SCRIPTS_DIR/provision-kind-cluster.sh "${CLUSTER_NAME}"
fi
export PATH=$TMP_DIR:$PATH

CLUSTER_NAME=$(cat "$TMP_DIR"/clustername)

## Build and Load Docker Images

if [ -z "$AWS_SERVICE_DOCKER_IMG" ]; then
    DEFAULT_AWS_SERVICE_DOCKER_IMG="aws-controllers-k8s:${AWS_SERVICE}-${VERSION}"
    echo -n "building $DEFAULT_AWS_SERVICE_DOCKER_IMG docker image ... "
    AWS_SERVICE_DOCKER_IMG="${DEFAULT_AWS_SERVICE_DOCKER_IMG}"
    export AWS_SERVICE_DOCKER_IMG
    export LOCAL_MODULES
    ${CODE_GENERATOR_SCRIPTS_DIR}/build-controller-image.sh ${AWS_SERVICE} 1>/dev/null || exit 1
    echo "ok."
else
    debug_msg "skipping building the ${AWS_SERVICE} docker image, since one was specified ${AWS_SERVICE_DOCKER_IMG}"
fi
echo "$AWS_SERVICE_DOCKER_IMG" > "${TMP_DIR}"/"${AWS_SERVICE}"_docker-img

echo -n "loading the images into the cluster ... "
kind load docker-image --quiet --name "${CLUSTER_NAME}" --nodes="${CLUSTER_NAME}"-worker,"${CLUSTER_NAME}"-control-plane "${AWS_SERVICE_DOCKER_IMG}" || exit 1
echo "ok."
if [[ "$ENABLE_PROMETHEUS" == true ]]; then
    echo -n "Loading prometheus image into the cluster ... "
    kind load docker-image --quiet --name "${CLUSTER_NAME}" \
        --nodes="${CLUSTER_NAME}"-worker,"${CLUSTER_NAME}"-control-plane \
        prom/prometheus || exit 1
    echo "ok."
fi

export KUBECONFIG="${TMP_DIR}/kubeconfig"

trap "clean_up" EXIT

export AWS_ACCOUNT_ID
export AWS_REGION
export AWS_ROLE_ARN
export ACK_ROLE_ARN
export ACK_ENABLE_DEVELOPMENT_LOGGING
export ACK_WATCH_NAMESPACE
export ENABLE_PROMETHEUS
export ACK_RESOURCE_TAGS
export ACK_LOG_LEVEL

service_config_dir="$SERVICE_CONTROLLER_SOURCE_PATH/config"

## Register the ACK service controller's CRDs in the target k8s cluster
echo -n "loading CRD manifests for $AWS_SERVICE into the cluster ... "
for crd_file in $service_config_dir/crd/bases; do
    kubectl apply -f "$crd_file" --validate=false 1>/dev/null
done
echo "ok."

echo -n "loading common manifests into the cluster ... "
for crd_file in $service_config_dir/crd/common/bases; do
    kubectl apply -f "$crd_file" --validate=false 1>/dev/null
done
echo "ok."

echo -n "loading RBAC manifests for $AWS_SERVICE into the cluster ... "
kustomize build "$service_config_dir"/rbac | kubectl apply -f - 1>/dev/null
echo "ok."

## Create the ACK service controller Deployment in the target k8s cluster
test_config_dir=$TMP_DIR/config/test
mkdir -p "$test_config_dir"

cp "$service_config_dir"/controller/deployment.yaml "$test_config_dir"/deployment.yaml
cp "$service_config_dir"/controller/service.yaml "$test_config_dir"/service.yaml

cat <<EOF >"$test_config_dir"/kustomization.yaml
resources:
- deployment.yaml
- service.yaml
EOF

echo -n "loading service controller Deployment for $AWS_SERVICE into the cluster ..."
cd "$test_config_dir"
kustomize edit set image controller="$AWS_SERVICE_DOCKER_IMG"
kustomize build "$test_config_dir" | kubectl apply -f - 1>/dev/null
echo "ok."

## Functional tests where we assume role and pass aws temporary credentials as env vars to deployment
echo -n "generating AWS temporary credentials and adding to env vars map ... "
aws_generate_temp_creds
kubectl -n ack-system set env deployment/ack-"$AWS_SERVICE"-controller \
    AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
    AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
    AWS_SESSION_TOKEN="$AWS_SESSION_TOKEN" \
    AWS_ACCOUNT_ID="$AWS_ACCOUNT_ID" \
    ACK_ENABLE_DEVELOPMENT_LOGGING="$ACK_ENABLE_DEVELOPMENT_LOGGING" \
    ACK_WATCH_NAMESPACE="$ACK_WATCH_NAMESPACE" \
    ACK_LOG_LEVEL="$ACK_LOG_LEVEL" \
    ACK_RESOURCE_TAGS="$ACK_RESOURCE_TAGS" \
    AWS_REGION="$AWS_REGION" 1>/dev/null
sleep 10
echo "ok."

echo "======================================================================================================"
echo "To poke around your test cluster manually:"
echo "export KUBECONFIG=$TMP_DIR/kubeconfig"
echo "kubectl get pods -A"
echo "======================================================================================================"

export KUBECONFIG

if [[ "$ENABLE_PROMETHEUS" == true ]]; then
    PROMETHEUS_SETUP_DIR=$SCRIPTS_DIR/prometheus
    PROMETHEUS_SETUP_FILE_PATH=$PROMETHEUS_SETUP_DIR/prometheus-setup.yaml
    echo -n "Deploying prometheus into the cluster ... "
    kubectl apply -f "$PROMETHEUS_SETUP_FILE_PATH" 1>/dev/null
    echo "ok."
    k8_wait_for_pod_status "prometheus-deployment" "Running" 60 || (echo 'FAIL: prometheus-deployment failed to Run' && exit 1)
fi

# TODO(RedbackThomson): Helm scripts rely on building `ack-generate` to produce
# the build artifacts. These need to come from the version used to generate the
# resources, not necessarily the `main` branch of `code-generator`
# if [[ "$TEST_HELM_CHARTS" == true ]]; then
#   $SCRIPTS_DIR/test-helm.sh "$AWS_SERVICE" "$VERSION"
# fi

# run e2e tests
export SKIP_PYTHON_TESTS
export RUN_PYTEST_LOCALLY
$SCRIPTS_DIR/run-tests.sh $AWS_SERVICE

if [[ "$DUMP_CONTROLLER_LOGS" == true ]]; then
    if [[ ! -d $ARTIFACTS ]]; then
        echo "Error evaluating ARTIFACTS environment variable:" 1>&2
        echo "$ARTIFACTS is not a directory." 1>&2
        exit 1
    fi

    # Use the first pod in the `ack-system` namespace
    POD=$(kubectl get pods -n ack-system -o name | grep $AWS_SERVICE-controller | head -n 1)
    kubectl logs -n ack-system $POD > $ARTIFACTS/controller_logs
fi