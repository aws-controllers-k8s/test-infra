#!/usr/bin/env bash

set -eo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

USAGE="
Usage:
  $(basename "$0") <service>

<service> should be an AWS service for which you wish to run tests -- e.g.
's3' 'sns' or 'sqs'
"

if [ $# -ne 1 ]; then
    echo "ERROR: $(basename "$0") only accepts a single parameter" 1>&2
    echo "$USAGE"
    exit 1
fi

AWS_SERVICE="$1"

# Source code for the controller will be in a separate repo, typically in
# $GOPATH/src/github.com/aws-controllers-k8s/$AWS_SERVICE-controller/
DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}
SERVICE_CONTROLLER_E2E_TEST_PATH="${SERVICE_CONTROLLER_SOURCE_PATH}/test/e2e"

KUBECONFIG_LOCATION="${KUBECONFIG:-"$HOME/.kube/config"}"

e2e_test_dockerfile="${SCRIPTS_DIR}/Dockerfile"

# Move into the aws-controllers-k8s/ context
# This will provide access to both the service-controller and the test-infra directory
pushd "${ROOT_DIR}/.." 1> /dev/null
  echo "Building e2e test container for $AWS_SERVICE"
  # Build using the e2e test Dockerfile
  TEST_DOCKER_SHA="$(docker build --file "${e2e_test_dockerfile}" --build-arg AWS_SERVICE="${AWS_SERVICE}" --quiet . )"
popd 1>/dev/null

echo "Running e2e test container $TEST_DOCKER_SHA"
# Ensure it can connect to KIND cluster on host device by running on host 
# network. 
# Pass AWS credentials and kubeconfig through to Dockerfile.
docker run --rm -t \
    --network="host" \
    -v $KUBECONFIG_LOCATION:/root/.kube/config:z \
    -v $HOME/.aws/credentials:/root/.aws/credentials:z \
    -v $THIS_DIR:/root/tests:z \
    -e SERVICE_CONTROLLER_E2E_TEST_PATH="." \
    -e RUN_PYTEST_LOCALLY="true" \
    -e PYTEST_LOG_LEVEL \
    -e PYTEST_NUM_THREADS \
    -e AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-"us-west-2"}" \
    -e AWS_ACCESS_KEY_ID \
    -e AWS_SECRET_ACCESS_KEY \
    -e AWS_SESSION_TOKEN \
    $TEST_DOCKER_SHA
