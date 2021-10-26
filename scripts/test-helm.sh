#!/usr/bin/env bash

# This test script installs the ACK service controller using the generated Helm
# chart, verifies that service controller started successfully and then
# uninstalls the controller using Helm.
#
# This script should be run as part of `kind-build-test.sh`. If running as
# standalone script, you would need a K8s cluster already created with updated
# KUBECONFIG. See the script "USAGE" for environment variables used
# by the script.

set -eo pipefail

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
SCRIPTS_DIR="$THIS_DIR"
ROOT_DIR="$THIS_DIR/.."

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/aws.sh"

check_is_installed kubectl "You can install kubectl with the helper scripts/install-kubectl.sh"
check_is_installed helm "You can install Helm with the helper scripts/install-helm.sh"

USAGE="
Usage:
  $(basename "$0") <service>

<service> should be an AWS service for which you wish to run tests -- e.g.
's3' 'sns' or 'sqs'

Environment variables:
  K8S_NAMESPACE:                    K8s Namespace for executing Helm Chart tests
                                    Default: ack-system-test-helm
  HELM_CHART_NAME:                  Name of Helm Chart
                                    Default: ack-<$AWS_SERVICE>-controller
  SERVICE_CONTROLLER_SOURCE_PATH:   Path to the ACK service controller. Helm
                                    Chart under test are present at
                                    $SERVICE_CONTROLLER_SOURCE_PATH/helm
  AWS_SERVICE_DOCKER_IMG:           Name of the controller image which will be
                                    used during release test
  AWS_REGION:                       AWS Region to use for installing ACK service
                                    controller. Default: us-west-2
"

if [ $# -ne 1 ]; then
    echo "AWS_SERVICE is not defined. Script accepts one parameter, <AWS_SERVICE> to install, validate and uninstall Helm chart for service controller" 1>&2
    echo "${USAGE}"
    exit 1
fi

AWS_SERVICE=$(echo "$1" | tr '[:upper:]' '[:lower:]')
K8S_NAMESPACE=${K8S_NAMESPACE:-"ack-system-test-helm"}
HELM_CHART_NAME=${HELM_CHART_NAME:-"ack-$AWS_SERVICE-controller"}
AWS_SERVICE_DOCKER_IMG=${AWS_SERVICE_DOCKER_IMG:-""}
if [[ -z $AWS_SERVICE_DOCKER_IMG ]]; then
    echo "AWS_SERVICE_DOCKER_IMG is not defined. Set AWS_SERVICE_DOCKER_IMG environment variable to the controller image which will be installed using Helm chart. Format: <repository>:<tag>"
    exit 1
fi
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

AWS_REGION=${AWS_REGION:-"us-west-2"}

echo "test-helm.sh] Starting Helm Artifacts Test"
HELM_DIR="$SERVICE_CONTROLLER_SOURCE_PATH/helm"
IMAGE_REPO=$(echo "$AWS_SERVICE_DOCKER_IMG" | cut -d":" -f1)
IMAGE_TAG=$(echo "$AWS_SERVICE_DOCKER_IMG" | cut -d":" -f2)

[ ! -d "$HELM_DIR" ] && echo "Helm directory does not exist for the service controller. Exiting... " && exit 1
[ ! -f "$HELM_DIR/Chart.yaml" ] && echo "Helm chart does not exist for the service controller. Exiting... " && exit 1

# do not exit script if following cleanup commands fail. These cleanup are just
# precautionary if kind cluster is being reused locally. Note: Prow jobs do not
# reuse kind cluster.
set +e
# uninstall command in case the kind cluster is being reused.
helm uninstall --namespace "$K8S_NAMESPACE" "$HELM_CHART_NAME" > /dev/null 2>&1
# cleanup to start the test from scratch
kubectl delete namespace "$K8S_NAMESPACE" > /dev/null 2>&1
kubectl delete clusterrolebinding  "ack-$AWS_SERVICE-controller-rolebinding" > /dev/null 2>&1
kubectl delete clusterrole  "ack-$AWS_SERVICE-controller" > /dev/null 2>&1
set -e
pushd "$HELM_DIR" 1> /dev/null
  echo -n "test-helm.sh] installing the Helm Chart $HELM_CHART_NAME in namespace $K8S_NAMESPACE ... "
  # NOTE: Do not skip creation of any k8s resource. This installation should test artifacts of all the
  # k8s resources. https://github.com/aws-controllers-k8s/community/issues/956
  # Install the helm chart with installScope=namespace, so that during local testing, it does not interact with
  # any left over custom resources from previous e2e runs when reusing kind cluster
  helm install --create-namespace \
    --namespace "$K8S_NAMESPACE" \
    --set aws.region="$AWS_REGION" \
    --set image.repository="$IMAGE_REPO" \
    --set image.tag="$IMAGE_TAG" \
    --set installScope="namespace" \
    --set metrics.service.create=true \
    "$HELM_CHART_NAME" . 1>/dev/null || exit 1
  echo "ok."
popd 1> /dev/null
# NOTE: Currently there is only a single deployment. Keeping this logic very simple
# right now.
# Update this logic if multiple deployments are started in single ACK Helm Chart
# installation.
CONTROLLER_DEPLOYMENT_NAME=$(kubectl get deployments -n $K8S_NAMESPACE -ojson | jq -r ".items[0].metadata.name")
if [ -z "$CONTROLLER_DEPLOYMENT_NAME" ]; then
  echo "test-helm.sh] [ERROR] Found empty ACK controller deployment name. Exiting ..."
  exit 1
fi
echo "test-helm.sh] ACK $AWS_SERVICE controller deployment name is $K8S_NAMESPACE/$CONTROLLER_DEPLOYMENT_NAME"
echo -n "test-helm.sh] Generating AWS temporary credentials and adding to env vars map in controller deployment ... "
aws_generate_temp_creds
kubectl -n "$K8S_NAMESPACE" set env deployment/"$CONTROLLER_DEPLOYMENT_NAME" \
    AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
    AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
    AWS_SESSION_TOKEN="$AWS_SESSION_TOKEN" 1>/dev/null
echo "ok."

echo -n "test-helm.sh] waiting 20 seconds for $AWS_SERVICE controller to start ... "
sleep 20
echo "ok"
# NOTE: Currently there is only a single pod. Keeping this logic very simple
# right now.
# Update this logic if multiple pods are started in single ACK Helm Chart
# installation.
CONTROLLER_POD_NAME=$(kubectl get pods -n $K8S_NAMESPACE -ojson | jq -r ".items[0].metadata.name")
if [ -z "$CONTROLLER_POD_NAME" ]; then
  echo "test-helm.sh] [ERROR] Found empty ACK controller pod name. Exiting ..."
  exit 1
fi
echo "test-helm.sh] ACK $AWS_SERVICE controller pod name is $K8S_NAMESPACE/$CONTROLLER_POD_NAME"
echo -n "test-helm.sh] Verifying that pod status is in Running state ... "
POD_STATUS=$(kubectl get pod/"$CONTROLLER_POD_NAME" -n $K8S_NAMESPACE -ojson | jq -r ".status.phase")
[[ $POD_STATUS != Running ]] && echo "pod status is $POD_STATUS . Exiting ... " && exit 1
echo "ok."
echo -n "test-helm.sh] Verifying that there are no ERROR in controller logs ... "
CONTROLLER_LOGS=$(kubectl logs pod/"$CONTROLLER_POD_NAME" -n $K8S_NAMESPACE)
if [ -z "$CONTROLLER_LOGS" ]; then
  echo "test-helm.sh] [ERROR] No controller logs found for pod $CONTROLLER_POD_NAME. Exiting ..."
  exit 1
fi
if echo "$CONTROLLER_LOGS" | grep -q "ERROR"
then
  echo "test-helm.sh] [ERROR] Found following ERROR statements in controller logs."
  print_line_separation
  echo "$CONTROLLER_LOGS" | grep "ERROR"
  print_line_separation
  echo "test-helm.sh] [ERROR] Exiting ..."
  exit 1
fi
echo "ok."
echo -n "test-helm.sh] uninstalling the Helm Chart $HELM_CHART_NAME in namespace $K8S_NAMESPACE ... "
helm uninstall --namespace "$K8S_NAMESPACE" "$HELM_CHART_NAME" 1>/dev/null || exit 1
echo "ok."
echo -n "test-helm.sh] deleting $K8S_NAMESPACE namespace ... "
kubectl delete namespace "$K8S_NAMESPACE" 1>/dev/null || exit 1
echo "ok."
echo "test-helm.sh] Helm Artifacts Test Finished Successfully"
