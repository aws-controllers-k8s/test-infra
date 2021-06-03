#!/usr/bin/env bash

set -eo pipefail

PYTEST_LOG_LEVEL=$(echo "${PYTEST_LOG_LEVEL:-"info"}" | tr '[:upper:]' '[:lower:]')
PYTEST_NUM_THREADS=${PYTEST_NUM_THREADS:-"auto"}
SOAK_TEST_DURATION_MINUTES="${SOAK_TEST_DURATION_MINUTES:-90}"

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  START_TIME_EPOCH_SECONDS:   Start time of soak test run in seconds from epoch.
  SOAK_TEST_DURATION_MINUTES: Number of minutes to run soak test. Default: 90 minutes
  CONTROLLER_E2E_PATH:        Path where e2e tests reside for ack service controller.
  PYTEST_LOG_LEVEL:           Set to any Python logging level for the Python tests.
                              Default: info
"

echo "[INFO] Starting the soak test run."
echo "[INFO] START_TIME_EPOCH_SECONDS is $START_TIME_EPOCH_SECONDS"
((END_TIME_EPOCH_SECONDS= "$START_TIME_EPOCH_SECONDS" + "$SOAK_TEST_DURATION_MINUTES"*60))
echo "[INFO] END_TIME_EPOCH_SECONDS is $END_TIME_EPOCH_SECONDS"

pushd "${CONTROLLER_E2E_PATH}" 1>/dev/null
# Treat e2e directory as a module to run bootstrap as scripts
export PYTHONPATH=..
python service_bootstrap.py
#If current time is less than $END_TIME_EPOCH_SECONDS, keep executing the e2e tests.
while [ $(date +%s) -le $END_TIME_EPOCH_SECONDS ]
do
  echo "[INFO] Current time is $(date +%s%3N) and end time is $END_TIME_EPOCH_SECONDS in epoch seconds. Executing e2e tests..."
  set +e
  pytest -n $PYTEST_NUM_THREADS --dist loadfile -o log_cli=true \
      --log-cli-level "${PYTEST_LOG_LEVEL}" --log-level "${PYTEST_LOG_LEVEL}" .
done
python service_cleanup.py
set -eo pipefail
popd 1> /dev/null

echo "[INFO] END_TIME_EPOCH_SECONDS $END_TIME_EPOCH_SECONDS is less than current time epoch seconds $(date +%s)"
echo "[INFO] Ending the soak test run."
