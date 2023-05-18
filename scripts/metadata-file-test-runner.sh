#!/usr/bin/env bash

# metadata-file-test-runner.sh contains functions used to test the existance and
# content of metadata.yaml files.

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

assert_metadata_file() {
    local metadata_file_name="metadata.yaml"
    local metadata_file_path="${SERVICE_CONTROLLER_SOURCE_PATH}/${metadata_file_name}"

    info_msg "Checking presence of metadata.yaml ..."

    if [[ ! -f "$metadata_file_path" ]]; then
        debug_msg "Unable to find metada.yaml file"
        exit 1
    fi

    info_msg "Validating metadata names"
    [[ $(yq .service.short_name < "$metadata_file_path") != "" ]] || exit 1
    [[ $(yq .service.full_name < "$metadata_file_path") != "" ]] || exit 1


    info_msg "Validating the existance of metadata URLs."

    ensure_url_healthy "$(yq .service.link < "$metadata_file_path")" || exit 1
    ensure_url_healthy "$(yq .service.documentation < "$metadata_file_path")" || exit 1
}

ensure_url_healthy() {
    __url="$1"
    if [[ $(curl --silent --head -o /dev/null -I -w "%{http_code}" "$__url") != "200" ]]; then
        debug_msg "$__url didn't respond with HTTP/1.1 200 OK"
        return 1
    fi
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "yq"
    check_is_installed "curl"
}

ensure_inputs
ensure_binaries