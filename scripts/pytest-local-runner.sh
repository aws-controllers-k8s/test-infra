#!/usr/bin/env bash

# pytest-local-runner.sh contains functions to bootstrap, run and clean up the
# ACK Python tests for a given controller.

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

AWS_SERVICE=$(echo "${AWS_SERVICE:-""}" | tr '[:upper:]' '[:lower:]')

PYTEST_LOG_LEVEL=$(echo "${PYTEST_LOG_LEVEL:-"info"}" | tr '[:upper:]' '[:lower:]')
PYTEST_NUM_THREADS=${PYTEST_NUM_THREADS:-"auto"}

DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$AWS_SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

DEFAULT_SERVICE_CONTROLLER_E2E_TEST_PATH="${SERVICE_CONTROLLER_SOURCE_PATH}/test/e2e"
SERVICE_CONTROLLER_E2E_TEST_PATH="${SERVICE_CONTROLLER_E2E_TEST_PATH:-$DEFAULT_SERVICE_CONTROLLER_E2E_TEST_PATH}"

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/config.sh"
source "$SCRIPTS_DIR/lib/logging.sh"

bootstrap_tests() {
    pushd "${SERVICE_CONTROLLER_E2E_TEST_PATH}" 1> /dev/null
        PYTHONPATH=.. python service_bootstrap.py
    popd 1> /dev/null
}

cleanup_tests() {
    pushd "${SERVICE_CONTROLLER_E2E_TEST_PATH}" 1> /dev/null
        PYTHONPATH=.. python service_cleanup.py
    popd 1> /dev/null
}

run_python_tests() {
    local markers_args=""
    local method_args=""

    for (( i=0; i<$(get_test_markers | yq '. | length' -); i++)); do
        local test_marker="$(get_test_markers | I=$i yq '.[env(I)]' -)"
        markers_args="$markers_args -m "$test_marker""
    done

    for (( i=0; i<$(get_test_methods | yq '. | length' -); i++)); do
        local test_method="$(get_test_methods | I=$i yq '.[env(I)]' -)"
        method_args="$method_args -k $test_method"
    done

    pushd "${SERVICE_CONTROLLER_E2E_TEST_PATH}" 1> /dev/null
        pytest -n ${PYTEST_NUM_THREADS} --dist no -o log_cli=true ${markers_args} \
            ${method_args} --log-cli-level "${PYTEST_LOG_LEVEL}" \
            --log-level "${PYTEST_LOG_LEVEL}" .
        local test_exit_code=$?
    popd 1> /dev/null
    return $test_exit_code
}

bootstrap_and_run() {
    info_msg "Running test bootstrap ..."
    bootstrap_tests

    info_msg "Running tests ..."
    # Disable exiting or error, so we can catch the pytest result
    set +e

    run_python_tests
    local test_exit_code=$?

    set -Eeo pipefail

    if [[ $test_exit_code -ne 0 ]]; then
        error_msg "Tests failed with exit code $test_exit_code"
    else
        info_msg "Tests succeeded!"
    fi

    info_msg "Running test cleanup ..."
    cleanup_tests

    return $test_exit_code
}

ensure_inputs() {
    [[ -z "$AWS_SERVICE" ]] && { error_msg "Expected \`AWS_SERVICE\` to be defined"; exit 1; } || :
}

ensure_binaries() {
    check_is_installed "python"
    check_is_installed "pytest"
}

ensure_inputs
ensure_binaries
