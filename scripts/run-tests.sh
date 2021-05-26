#!/usr/bin/env bash

# This script runs the existing bash tests for a service controller.

set -eo pipefail

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SCRIPTS_DIR/.."

# set environment variables
SKIP_PYTHON_TESTS=${SKIP_PYTHON_TESTS:-"false"}
RUN_PYTEST_LOCALLY=${RUN_PYTEST_LOCALLY:-"false"}
PYTEST_LOG_LEVEL=$(echo "${PYTEST_LOG_LEVEL:-"info"}" | tr '[:upper:]' '[:lower:]')
PYTEST_NUM_THREADS=${PYTEST_NUM_THREADS:-"auto"}

USAGE="
Usage:
  $(basename "$0") <service>

<service> should be an AWS service for which you wish to run tests -- e.g.
's3' 'sns' or 'sqs'

Environment variables:
  DEBUG:                    Set to any value to enable debug logging in the bash tests
  SKIP_PYTHON_TESTS         Whether to skip python tests and run bash tests instead for
                            the service controller (<true|false>)
                            Default: false
  RUN_PYTEST_LOCALLY        If python tests exist, whether to run them locally instead of
                            inside Docker (<true|false>)
                            Default: false
  PYTEST_LOG_LEVEL:         Set to any Python logging level for the Python tests.
                            Default: info
  PYTEST_NUM_THREADS:       Number of threads that the pytest-xdist plugin should use when
                            running the tests. Default of \"auto\" will detect number of
                            cores.
                            Default: auto
"

if [ $# -ne 1 ]; then
    echo "ERROR: $(basename "$0") only accepts a single parameter" 1>&2
    echo "$USAGE"
    exit 1
fi

# construct and validate service directory path
SERVICE="$1"

# Source code for the controller will be in a separate repo, typically in
# $GOPATH/src/github.com/aws-controllers-k8s/$SERVICE-controller/
DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH="$ROOT_DIR/../$SERVICE-controller"
SERVICE_CONTROLLER_SOURCE_PATH=${SERVICE_CONTROLLER_SOURCE_PATH:-$DEFAULT_SERVICE_CONTROLLER_SOURCE_PATH}

DEFAULT_SERVICE_CONTROLLER_E2E_TEST_PATH="${SERVICE_CONTROLLER_SOURCE_PATH}/test/e2e"
SERVICE_CONTROLLER_E2E_TEST_PATH="${SERVICE_CONTROLLER_E2E_TEST_PATH:-$DEFAULT_SERVICE_CONTROLLER_E2E_TEST_PATH}"

if [ ! -d "$SERVICE_CONTROLLER_E2E_TEST_PATH" ]; then
    echo "No tests for service $SERVICE"
    exit 0
fi

# check if python tests exist for the service
[[ -f "$SERVICE_CONTROLLER_E2E_TEST_PATH/__init__.py" ]] && python_tests_exist="true" || python_tests_exist="false"

# run tests
if [[ "$python_tests_exist" == "false" ]] || [[ "$SKIP_PYTHON_TESTS" == "true" ]]; then
  source "$SCRIPTS_DIR/lib/common.sh"

  echo "running bash tests..."
  service_test_files=$( find "$SERVICE_CONTROLLER_E2E_TEST_PATH" -type f -name '*.sh' | sort )
  for service_test_file in $service_test_files; do
      test_name=$( filenoext "$service_test_file" )
      test_start_time=$( date +%s )
      bash $service_test_file
      test_end_time=$( date +%s )
      echo "$test_name took $( expr $test_end_time - $test_start_time ) second(s)"
  done

elif [[ "$RUN_PYTEST_LOCALLY" == "true" ]]; then
  echo "running python tests locally..."

  pushd "${SERVICE_CONTROLLER_E2E_TEST_PATH}" 1> /dev/null
    # Treat e2e directory as a module to run bootstrap as scripts
    export PYTHONPATH=..
    python service_bootstrap.py
    set +e

    pytest -n $PYTEST_NUM_THREADS --dist loadfile -o log_cli=true \
      --log-cli-level "${PYTEST_LOG_LEVEL}" --log-level "${PYTEST_LOG_LEVEL}" .
    test_exit_code=$?

    python service_cleanup.py
    set -eo pipefail
  popd 1> /dev/null

  exit "$test_exit_code"

else
  ${SCRIPTS_DIR}/build-run-test-dockerfile.sh $SERVICE
fi
