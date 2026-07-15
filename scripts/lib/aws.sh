#!/usr/bin/env bash

# aws.sh contains functions for running common AWS command line methods.

LIB_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

source "$LIB_DIR/common.sh"
source "$LIB_DIR/config.sh"
source "$LIB_DIR/logging.sh"

AWS_CLI_VERSION=${DEFAULT_AWS_CLI_VERSION:-"2.0.52"}

# daws() executes the AWS Python CLI tool from a Docker container.
#
# Instead of relying on developers having a particular version of the AWS
# Python CLI tool, this method allows a specific version of the CLI tool to be
# executed within a Docker container.
#
# You call the daws function just like you were calling the `aws` CLI tool.
#
# Usage:
#
#   daws SERVICE COMMAND [OPTIONS]
#
# Example:
#
#   daws ecr describe-repositories --repository-name my-repo
#
# To use a specific version of the AWS CLI, set the ACK_AWS_CLI_IMAGE_VERSION
# environment variable, otherwise the value of DEFAULT_AWS_CLI_VERSION is used.
daws() {
    local profile="$(get_aws_profile)"
    local identity_file="$(get_aws_token_file)"
    local default_region="$(get_aws_region)"

    aws_cli_profile_env=$([ ! -z "$profile" ] && echo "--env AWS_PROFILE=$profile")
    aws_cli_web_identity_env="$([ ! -z "$identity_file" ] && \
        echo "--env AWS_WEB_IDENTITY_TOKEN_FILE=/root/aws_token --env AWS_ROLE_ARN -v $identity_file:/root/aws_token:ro" )"
    aws_cli_img="amazon/aws-cli:$AWS_CLI_VERSION"

    # Pass static credentials if available (e.g., from wrapper.sh role assumption)
    # When static creds exist, skip web identity and AWS config mount to avoid
    # conflicts with pod identity configuration in nested containers
    aws_cli_static_creds_env=""
    aws_config_mount="-v ~/.aws:/root/.aws:z"
    if [ -n "${AWS_ACCESS_KEY_ID:-}" ]; then
        aws_cli_static_creds_env="--env AWS_ACCESS_KEY_ID --env AWS_SECRET_ACCESS_KEY --env AWS_SESSION_TOKEN"
        # Clear web identity env to prevent Docker from using pod identity
        aws_cli_web_identity_env=""
        # Don't mount ~/.aws which may contain pod identity config
        aws_config_mount=""
    fi

    docker run --rm $(echo $aws_config_mount) $(echo $aws_cli_profile_env) $(echo $aws_cli_web_identity_env) $(echo $aws_cli_static_creds_env) --env AWS_DEFAULT_REGION=$default_region -v $(pwd):/aws "$aws_cli_img" "$@"
}

# ensure_aws_credentials() calls the STS::GetCallerIdentity API call and
# verifies that there is a local identity for running AWS commands
ensure_aws_credentials() {
    daws sts get-caller-identity --query "Account" >/dev/null ||
        ( error_msg "No AWS credentials found. Please run \`aws configure\` to set up the CLI for your credentials" && exit 1)
}

# refresh_assumed_role_creds() re-mints the static credentials in the current
# shell by assuming the service team role again from Pod Identity.
#
# wrapper.sh assumes the role once at job start; that credential is capped at 1
# hour. In the code-generator core-validator the unit, e2e and helm phases run
# back-to-back in a single pod, so a late phase (helm) can inherit an expired
# credential. This re-mints from Pod Identity -- reachable only from the pod,
# and non-expiring -- using the host aws CLI rather than daws, which runs in a
# nested container that cannot reach the Pod Identity endpoint.
#
# No-op outside Prow, where the preserved Pod Identity reference is absent.
refresh_assumed_role_creds() {
    [[ -z "${ACK_POD_IDENTITY_CREDENTIALS_FULL_URI:-}" || -z "${ASSUMED_ROLE_ARN:-}" ]] && return 0

    local json
    json=$(env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
           AWS_CONTAINER_CREDENTIALS_FULL_URI="${ACK_POD_IDENTITY_CREDENTIALS_FULL_URI}" \
           AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE="${ACK_POD_IDENTITY_AUTHORIZATION_TOKEN_FILE}" \
           aws sts assume-role \
             --role-arn "${ASSUMED_ROLE_ARN}" \
             --role-session-name "${PROW_JOB_ID:-refresh}" \
             --duration-seconds 3600 \
             --output json) || { error_msg "Unable to refresh assumed-role credentials"; return 1; }

    export AWS_ACCESS_KEY_ID=$(echo "${json}" | jq --raw-output ".Credentials.AccessKeyId")
    export AWS_SECRET_ACCESS_KEY=$(echo "${json}" | jq --raw-output ".Credentials.SecretAccessKey")
    export AWS_SESSION_TOKEN=$(echo "${json}" | jq --raw-output ".Credentials.SessionToken")
    info_msg "Refreshed assumed-role credentials"
}

# generate_aws_temp_creds function will generate temporary AWS credentials which
# are valid for 3600 seconds
aws_generate_temp_creds() {
    local __role_suffix=""
    # If CARM_TESTS_ENABLED is set, do not inject a uuid into the role name
    if [[ ! "$CARM_TESTS_ENABLED" = "true" && ! "$IRS_TESTS_ENABLED" = "true" ]]; then
        echo "CARM_TESTS_ENABLED/IRS_TESTS_ENABLED is not set, injecting uuid into role name"
        __role_suffix="-$(uuidgen | cut -d'-' -f1 | tr '[:upper:]' '[:lower:]')"
    fi

    local assumed_role=$(get_assumed_role_arn)

    local json=$(daws sts assume-role \
           --role-arn "$assumed_role"  \
           --role-session-name tmp-role"$__role_suffix" \
           --duration-seconds 3600 \
           --output json || exit 1)

    AWS_ACCESS_KEY_ID=$(echo "${json}" | jq --raw-output ".Credentials[\"AccessKeyId\"]")
    AWS_SECRET_ACCESS_KEY=$(echo "${json}" | jq --raw-output ".Credentials[\"SecretAccessKey\"]")
    AWS_SESSION_TOKEN=$(echo "${json}" | jq --raw-output ".Credentials[\"SessionToken\"]")
}

aws_account_id() {
    local json=$(daws sts get-caller-identity --output json || exit 1)
    echo "${json}" | jq --raw-output ".Account"
}

ensure_binaries() {
    check_is_installed "docker"
    check_is_installed "jq"
    check_is_installed "uuidgen"
}

ensure_binaries