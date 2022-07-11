#!/usr/bin/env bash

# pytest-image-runner.sh contains functions used to build and run the ACK Python
# test framework from within a container.

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

DEFAULT_SERVICE_CONTROLLER_E2E_TEST_PATH="${SERVICE_CONTROLLER_SOURCE_PATH}/test/e2e"
SERVICE_CONTROLLER_E2E_TEST_PATH="${SERVICE_CONTROLLER_E2E_TEST_PATH:-$DEFAULT_SERVICE_CONTROLLER_E2E_TEST_PATH}"

# The location of new credential file which will be copied in test container
# Keep this file in same location as '~/.aws/credentials' file
TMP_TEST_AWS_CREDS_FILE_LOCATION="$HOME/.aws/ack-test-credentials"
# The name of the AWS profile inside the container for rotating credentials
TEST_AWS_PROFILE_NAME="ack-test"
# The file path containing a copy of the test config
TMP_TEST_CONFIG_FILE_LOCATION="$HOME/.aws/$AWS_SERVICE-test-config.yaml"

# The following environment variables are injected when running as a Prow job
PROW_JOB_ID=${PROW_JOB_ID:-}
AWS_WEB_IDENTITY_TOKEN_FILE=${AWS_WEB_IDENTITY_TOKEN_FILE:-}

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

build_pytest_image() {
    local __image_tag=$1

    local ack_role_arn=$(get_assumed_role_arn)

    # Path of web-identity-token in test container
    local test_container_web_identity_token_file="/root/web-identity-token"
    # If 'AWS_PROFILE' variable is set, use it as source profile for 'ack-test'
    # profile. Use 'default' as fallback
    local ack_test_source_aws_profile=${AWS_PROFILE:-"default"}
    local aws_creds_file_location="$HOME/.aws/credentials"

    if [[ -n $PROW_JOB_ID ]]; then
        # If this is prowjob, create new aws credentials file and setup two profiles
        # 1. 'prow-irsa' profile which gets aws credentials using web-identity-token
        # 2. 'ack-test' profile which uses 'prow-irsa' as source profile and assumes
        # ack_role_arn for aws credentials
        # NOTE: credentials in both these profiles rotate automatically

        # copy web-identity-token file for use inside test container
        cp "$AWS_WEB_IDENTITY_TOKEN_FILE" "$SCRIPTS_DIR"/web-identity-token >/dev/null

        # generate new aws-credentials file for test container
        eval "echo \"$(cat "$SCRIPTS_DIR/creds-templates/prow-test-aws-creds-template.txt")\"" > "$TMP_TEST_AWS_CREDS_FILE_LOCATION"
    else
        # for local testing, copy existing aws-credentials file and add 'ack-test' alongside
        # other profiles
        local local_aws_creds_content=""
        if [[ -f $aws_creds_file_location ]]; then
            local_aws_creds_content=$(cat "$aws_creds_file_location")
        fi

        # generate new aws-credentials file for test container
        eval "echo \"$(cat "$SCRIPTS_DIR/creds-templates/local-test-aws-creds-template.txt")\"" > "$TMP_TEST_AWS_CREDS_FILE_LOCATION"
    fi

    local e2e_test_dockerfile="${SCRIPTS_DIR}/Dockerfile.pytest-image"

    # Move into the aws-controllers-k8s/ context
    # This will provide access to both the service-controller and the test-infra directory
    pushd "${ROOT_DIR}/.." 1> /dev/null
        info_msg "Building e2e test container for $AWS_SERVICE ..."
        # Build using the e2e test Dockerfile
        local test_docker_sha="$(docker build --file "${e2e_test_dockerfile}" \
        --tag $__image_tag \
        --build-arg AWS_SERVICE="${AWS_SERVICE}" \
        --build-arg WEB_IDENTITY_TOKEN_DEST_PATH="${test_container_web_identity_token_file}" \
        --quiet . )"
        debug_msg "Built PyTest image $__image_tag ($test_docker_sha)"
    popd 1>/dev/null
}

run_pytest_image() {
    local __image_tag=$1

    local region=$(get_aws_region)

    # Copy the test config into the temporary path
    cp "$(get_test_config_path)" $TMP_TEST_CONFIG_FILE_LOCATION

    info_msg "Running e2e test container for $AWS_SERVICE ..."

    docker run --rm -t \
        --network="host" \
        -v "$KUBECONFIG":/root/.kube/config:z \
        -v "$TMP_TEST_AWS_CREDS_FILE_LOCATION":/root/.aws/credentials:z \
        -v "$TMP_TEST_CONFIG_FILE_LOCATION":/root/test-config.yaml:z \
        -e SERVICE_CONTROLLER_E2E_TEST_PATH="." \
        -e ACK_ROLE_ARN \
        -e PYTEST_LOG_LEVEL \
        -e PYTEST_NUM_THREADS \
        -e AWS_DEFAULT_REGION="$region" \
        -e AWS_PROFILE="$TEST_AWS_PROFILE_NAME" \
        "$__image_tag"
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "docker"
}

ensure_inputs
ensure_binaries