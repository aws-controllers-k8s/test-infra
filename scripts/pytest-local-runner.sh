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

# With CHAINSAW_TESTS_ENABLED=true, the controller's Chainsaw tests in this
# directory run instead of its Python bootstrap, tests and cleanup. Prow sets it
# in a <service>-chainsaw-e2e job of its own, so pytest keeps running in
# <service>-kind-e2e. A controller without the directory has nothing to run,
# and passes.
CHAINSAW_TEST_PATH="${SERVICE_CONTROLLER_E2E_TEST_PATH}/chainsaw"

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
        # The full test-infra repo is copied into the image as ./acktest (used
        # to pip-install the library). Exclude it from collection so acktest's
        # own unit tests are never discovered as part of a controller's e2e run.
        # The JUnit report goes to the job's artifacts dir, where Prow shows it
        # on the job page.
        pytest -n ${PYTEST_NUM_THREADS} --dist no -o log_cli=true --ignore=acktest ${markers_args} \
            ${method_args} --log-cli-level "${PYTEST_LOG_LEVEL}" \
            --log-level "${PYTEST_LOG_LEVEL}" \
            --junitxml "${ARTIFACTS:-/tmp}/junit_pytest.xml" .
        local test_exit_code=$?
    popd 1> /dev/null
    return $test_exit_code
}

is_chainsaw_enabled() {
    [[ "${CHAINSAW_TESTS_ENABLED:-}" == "true" ]]
}

run_chainsaw_tests() {
    # The suite's own config (timeouts, parallelism, default catch) lives
    # next to its tests
    local config_args=()
    if [[ -f "$CHAINSAW_TEST_PATH/.chainsaw.yaml" ]]; then
        config_args=(--config "$CHAINSAW_TEST_PATH/.chainsaw.yaml")
    fi

    chainsaw test --test-dir "$CHAINSAW_TEST_PATH" "${config_args[@]}" \
        --report-format JUNIT-TEST --report-path "${ARTIFACTS:-/tmp}" --report-name junit_chainsaw
}

run_chainsaw() {
    if [[ ! -d "$CHAINSAW_TEST_PATH" ]]; then
        info_msg "CHAINSAW_TESTS_ENABLED is true, but there are no Chainsaw tests in $CHAINSAW_TEST_PATH. Nothing to run."
        return 0
    fi
    info_msg "Running Chainsaw tests in $CHAINSAW_TEST_PATH (Python bootstrap, tests and cleanup are skipped) ..."
    # Disable exiting on error, so we can catch the Chainsaw result
    set +e

    run_chainsaw_tests
    local test_exit_code=$?

    set -Eeo pipefail

    if [[ $test_exit_code -ne 0 ]]; then
        error_msg "Tests failed with exit code $test_exit_code"
    else
        info_msg "Tests succeeded!"
    fi

    return $test_exit_code
}

bootstrap_and_run() {
    if is_chainsaw_enabled; then
        # run_chainsaw re-enables errexit, so capture its result rather than
        # exiting here, the same way the pytest path returns its exit code
        local test_exit_code=0
        run_chainsaw || test_exit_code=$?
        return $test_exit_code
    fi

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
    if is_chainsaw_enabled; then
        check_is_installed "chainsaw"
        check_is_installed "aws"
        check_is_installed "jq"
        check_is_installed "kubectl"
        return
    fi
    check_is_installed "python"
    check_is_installed "pytest"
}

ensure_inputs
ensure_binaries
