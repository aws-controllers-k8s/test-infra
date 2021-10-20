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
AWS_CREDS_FILE_LOCATION="$HOME/.aws/credentials"

# create an empty web-identity-token file inside Docker build context for
# the test container.
# If this script is running as part of prowjob then we will copy web-identity-token
# in this file to be used inside test container
touch "$SCRIPTS_DIR"/web-identity-token >/dev/null

# path of web-identity-token in test container
TEST_CONTAINER_WEB_IDENTITY_TOKEN_FILE="/root/web-identity-token"
# run python tests using new AWS profile to allow credential auto rotation
ACK_TEST_AWS_PROFILE="ack-test"
# If 'AWS_PROFILE' variable is set, use it as source profile for 'ack-test'
# profile. Use 'default' as fallback
ACK_TEST_SOURCE_AWS_PROFILE=${AWS_PROFILE:-"default"}
# location of new credential file which will be copied in test container
# Keep this file in same location as '~/.aws/credentials' file
TEST_AWS_CREDS_FILE_LOCATION="$HOME/.aws/ack-test-credentials"

if [[ -n $PROW_JOB_ID ]]; then
  # If this is prowjob, create new aws credentials file and setup two profiles
  # 1. 'prow-irsa' profile which gets aws credentials using web-identity-token
  # 2. 'ack-test' profile which uses 'prow-irsa' as source profile and assumes
  # ACK_ROLE_ARN for aws credentials
  # NOTE: credentials in both these profiles rotate automatically

  # copy web-identity-token file for use inside test container
  # AWS_WEB_IDENTITY_TOKEN_FILE variable is injected into pod using IRSA
  cp "$AWS_WEB_IDENTITY_TOKEN_FILE" "$SCRIPTS_DIR"/web-identity-token >/dev/null

  # generate new aws-credentials file for test container
  eval "echo \"$(cat "$SCRIPTS_DIR/templates/prow-test-aws-creds-template.txt")\"" > "$TEST_AWS_CREDS_FILE_LOCATION"
else
  # for local testing, copy existing aws-credentials file and add 'ack-test' alongside
  # other profiles
  LOCAL_AWS_CREDS_CONTENT=""
  if [[ -f $AWS_CREDS_FILE_LOCATION ]]; then
    LOCAL_AWS_CREDS_CONTENT=$(cat "$AWS_CREDS_FILE_LOCATION")
  fi

  # generate new aws-credentials file for test container
  eval "echo \"$(cat "$SCRIPTS_DIR/templates/local-test-aws-creds-template.txt")\"" > "$TEST_AWS_CREDS_FILE_LOCATION"
fi

e2e_test_dockerfile="${SCRIPTS_DIR}/Dockerfile"

# Move into the aws-controllers-k8s/ context
# This will provide access to both the service-controller and the test-infra directory
pushd "${ROOT_DIR}/.." 1> /dev/null
  echo "Building e2e test container for $AWS_SERVICE"
  # Build using the e2e test Dockerfile
  TEST_DOCKER_SHA="$(docker build --file "${e2e_test_dockerfile}" \
  --build-arg AWS_SERVICE="${AWS_SERVICE}" \
  --build-arg WEB_IDENTITY_TOKEN_DEST_PATH="${TEST_CONTAINER_WEB_IDENTITY_TOKEN_FILE}" \
  --quiet . )"
popd 1>/dev/null

echo "Running e2e test container $TEST_DOCKER_SHA"

# Ensure it can connect to KIND cluster on host device by running on host 
# network. 
# Pass AWS credentials and kubeconfig through to Dockerfile.
docker run --rm -t \
    --network="host" \
    -v "$KUBECONFIG_LOCATION":/root/.kube/config:z \
    -v "$TEST_AWS_CREDS_FILE_LOCATION":/root/.aws/credentials:z \
    -e SERVICE_CONTROLLER_E2E_TEST_PATH="." \
    -e RUN_PYTEST_LOCALLY="true" \
    -e PYTEST_LOG_LEVEL \
    -e PYTEST_NUM_THREADS \
    -e AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-"us-west-2"}" \
    -e AWS_PROFILE="$ACK_TEST_AWS_PROFILE" \
    "$TEST_DOCKER_SHA"

# remove the web-identity-token file from scripts directory
rm -f "$SCRIPTS_DIR"/web-identity-token >/dev/null
