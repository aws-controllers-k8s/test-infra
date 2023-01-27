#!/usr/bin/env bash

set -eo pipefail

DEFAULT_SOAK_CONFIG_PATH="$(pwd)/default_soak_config.yaml"
CONTROLLER_SOAK_CONFIG_PATH="$CONTROLLER_E2E_PATH/soak_config.yaml"
DEFAULT_SOAK_DURATION_MINUTES=${DEFAULT_SOAK_DURATION_MINUTES:-"1440"}

if [[ -f "$CONTROLLER_SOAK_CONFIG_PATH" ]]; then
	SOAK_CONFIG_PATH=$CONTROLLER_SOAK_CONFIG_PATH
else
	SOAK_CONFIG_PATH=$DEFAULT_SOAK_CONFIG_PATH
fi

echo "Using soak configuration stored at $SOAK_CONFIG_PATH"

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  START_TIME_EPOCH_SECONDS:   Start time of soak test run in seconds from epoch.
  CONTROLLER_E2E_PATH:        Path where e2e tests reside for ack service controller.
  DEFAULT_SOAK_DURATION_MINUTES: Default duration of soak test execution in minutes.
"

SOAK_DURATION_MINUTES=$(yq eval ".durationMinutes // \"$DEFAULT_SOAK_DURATION_MINUTES\"" "$SOAK_CONFIG_PATH") #default: $DEFAULT_SOAK_DURATION_MINUTES
echo "[INFO] Starting the soak test run."
echo "[INFO] START_TIME_EPOCH_SECONDS is $START_TIME_EPOCH_SECONDS"
((END_TIME_EPOCH_SECONDS= "$START_TIME_EPOCH_SECONDS" + "$SOAK_DURATION_MINUTES"*60))
echo "[INFO] END_TIME_EPOCH_SECONDS is $END_TIME_EPOCH_SECONDS"

pushd "${CONTROLLER_E2E_PATH}" 1>/dev/null
# Treat e2e directory as a module to run bootstrap as scripts
export PYTHONPATH=..
python service_bootstrap.py
set +e
ALL_PYTEST_MARKERS=$(yq eval '.pytestMarkers|keys|.[]' "$SOAK_CONFIG_PATH")
#If current time is less than $END_TIME_EPOCH_SECONDS, keep executing the e2e tests.
while [ "$(date +%s)" -le $END_TIME_EPOCH_SECONDS ]
do
  echo "[INFO] Current time is $(date +%s%3N) and end time is $END_TIME_EPOCH_SECONDS in epoch seconds. Executing e2e tests..."
  for marker in $ALL_PYTEST_MARKERS
  do
	  log_level=$(yq eval ".pytestMarkers.$marker.logLevel // \"info\"" "$SOAK_CONFIG_PATH") #default: info
	  num_threads=$(yq eval ".pytestMarkers.$marker.numThreads // \"auto\"" "$SOAK_CONFIG_PATH") #default: auto
	  dist=$(yq eval ".pytestMarkers.$marker.dist // \"no\"" "$SOAK_CONFIG_PATH") # default: no
	  pytest -m "$marker" -n "$num_threads" --dist "$dist" -o log_cli=true --log-cli-level "$log_level" --log-level "$log_level" .
  done
done

python service_cleanup.py
set -eo pipefail
popd 1> /dev/null

echo "[INFO] END_TIME_EPOCH_SECONDS $END_TIME_EPOCH_SECONDS is less than current time epoch seconds $(date +%s)"
echo "[INFO] Ending the soak test run."
