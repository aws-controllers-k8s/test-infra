#!/usr/bin/env bash

# config.sh contains functions for extracting configuration elements from an ACK
# test config file, providing sane defaults when possible.

set -Eeo pipefail

LIB_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
SCRIPTS_DIR="$LIB_DIR/.."

TEST_CONFIG_PATH=${TEST_CONFIG_PATH:-"$SCRIPTS_DIR/../test_config.yaml"}

source "$LIB_DIR/common.sh"
source "$LIB_DIR/logging.sh"

_get_config_field() {
    local __field_path="$1"
    local __default="${2:-""}"

    yq "$__field_path // \"$__default\"" $TEST_CONFIG_PATH 2>/dev/null
}

get_cluster_create() { _get_config_field ".cluster.create" "true"; }
get_cluster_name() { _get_config_field ".cluster.name"; }
get_cluster_k8s_version() { _get_config_field ".cluster.k8s_version"; }
get_cluster_configuration_file_name() { _get_config_field ".cluster.configuration.file_name" "kind-two-node-cluster.yaml"; }
get_cluster_additional_controllers() { _get_config_field ".cluster.configuration.additional_controllers"; }
get_aws_profile() { _get_config_field ".aws.profile"; }
get_aws_token_file() { _get_config_field ".aws.token_file"; }
get_aws_region() { _get_config_field ".aws.region" "us-west-2"; }
get_assumed_role_arn() { _get_config_field ".aws.assumed_role_arn"; }
get_test_markers() { _get_config_field ".tests.markers"; }
get_helm_tests_enabled() { _get_config_field ".tests.helm.enabled"; }
get_run_tests_locally() { _get_config_field ".tests.run_locally"; }
get_is_local_build() { _get_config_field ".local_build" false; }
get_debug_enabled() { _get_config_field ".debug.enabled" false; }
get_dump_controller_logs() { _get_config_field ".debug.dump_controller_logs" false; }

get_test_config_path() { echo "$TEST_CONFIG_PATH"; }

ensure_config_file_exists() {
    [[ ! -f "$TEST_CONFIG_PATH" ]] && { error_msg "Config file does not exist at path $TEST_CONFIG_PATH"; exit 1; } || :
}

ensure_required_fields() {
    local required_field_paths=( ".aws.assumed_role_arn" )
    for path in "${required_field_paths[@]}"; do
        [[ -z $(_get_config_field $path) ]] && { error_msg "Required config path \`$path\` not provided"; exit 1; } || :
    done
}

ensure_binaries() {
    check_is_installed "yq"
}

ensure_binaries
ensure_config_file_exists
ensure_required_fields