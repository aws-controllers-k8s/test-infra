#!/usr/bin/env bash

# iam-policy-test-runner.sh contains functions used to run the ACK recommended
# IAM policy tests

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

source "$SCRIPTS_DIR/lib/aws.sh"
source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

assert_iam_policies() {
    local recommended_policy_relative_path="config/iam/recommended-policy-arn"
    local recommended_inline_policy_relative_path="config/iam/recommended-inline-policy"

    local recommended_policy_path="${SERVICE_CONTROLLER_SOURCE_PATH}/${recommended_policy_relative_path}"
    local recommended_inline_policy_path="${SERVICE_CONTROLLER_SOURCE_PATH}/${recommended_inline_policy_relative_path}"
    echo $recommended_policy_path
    echo $recommended_inline_policy_path

    info_msg "Checking presence of recommended-policy-arn ..."

    if [[ ! -f $recommended_policy_path ]]; then
        debug_msg "Unable to find recommended-policy-arn file"
        info_msg "Checking presence of recommended-inline-policy ..."

        if [[ ! -f $recommended_inline_policy_path ]]; then
            error_msg "Unable to find recommended-inline-policy file"
            exit 1
        fi

        # Assert contents of recommended inline policy
        info_msg "Checking contents of recommended-inline-policy ..."
        if [[ ! -s $recommended_inline_policy_path ]]; then
            error_msg "recommended_inline_policy_path is empty"
            exit 1
        fi

        return 0
    fi

    info_msg "Validating contents of recommended-policy-arn ..."
    for policy_arn in $(awk NF=NF FS="\n" $recommended_policy_path); do
        debug_msg "Validating \"$policy_arn\" ..."
        if ! (daws iam get-policy --policy-arn "$policy_arn" >/dev/null); then
            error_msg "\"$policy_arn\" is not a valid managed IAM policy ARN"
            exit 1
        fi
    done
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_inputs