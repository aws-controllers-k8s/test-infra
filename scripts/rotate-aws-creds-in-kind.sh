#!/usr/bin/env bash

SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

source "$SCRIPTS_DIR/lib/common.sh"
source "$SCRIPTS_DIR/lib/aws.sh"

check_is_installed kubectl
check_is_installed aws

USAGE="
Usage:
  $(basename "$0") <AWS_SERVICE>

Generates new AWS temporary credentials by assuming ACK_ROLE_ARN and
then sets them as environment variable in ACK controller deployment for
the KinD test and restarts the deployment.
If DUMP_CONTROLLER_LOGS is true, this script first collect the controller
logs and then restarts the controller deployment

NOTE: This scripts runs with a forever loop and is intended to run as a
background job for kind-build-test.sh

<AWS_SERVICE> should be an AWS Service name (ecr, sns, sqs)

Environment variables:
  ACK_ROLE_ARN:             Provide AWS Role ARN for functional testing on local KinD Cluster. Mandatory.
  DUMP_CONTROLLER_LOGS:     Whether to dump the controller pod logs to a local file after finishing tests.
                            Default: false
  ARTIFACTS:                Directory to store controller logs. This variable is injected by prowjob
"

if [ $# -ne 1 ]; then
    echo "AWS_SERVICE is not defined. Script accepts one parameter, <AWS_SERVICE> to find the deployment name of ACK controller" 1>&2
    echo "${USAGE}"
    exit 1
fi

AWS_SERVICE=$(echo "$1" | tr '[:upper:]' '[:lower:]')

trap 'kill $(jobs -p)' EXIT SIGINT

while true
do
  echo "rotate-aws-creds-in-kind.sh][INFO] sleeping for 50 mins before rotating temporary aws credentials"
  sleep 3000 & wait
  if [[ "$DUMP_CONTROLLER_LOGS" == true ]]; then
    if [[ ! -d $ARTIFACTS ]]; then
      echo "rotate-aws-creds-in-kind.sh][ERROR] Error evaluating ARTIFACTS environment variable" 1>&2
      echo "rotate-aws-creds-in-kind.sh][ERROR] $ARTIFACTS is not a directory" 1>&2
      echo "rotate-aws-creds-in-kind.sh][ERROR] Skipping controller logs capture"
    else
      # Use the first pod in the `ack-system` namespace
      POD=$(kubectl get pods -n ack-system -o name | grep $AWS_SERVICE-controller | head -n 1)
      kubectl logs -n ack-system $POD >> $ARTIFACTS/controller_logs
    fi
  fi

  aws_generate_temp_creds
  kubectl -n ack-system set env deployment/ack-"$AWS_SERVICE"-controller \
    AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
    AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
    AWS_SESSION_TOKEN="$AWS_SESSION_TOKEN"  1>/dev/null

  kubectl -n ack-system rollout restart deployment ack-"$AWS_SERVICE"-controller >/dev/null
  echo "rotate-aws-creds-in-kind.sh][INFO] Successfully rotated AWS credentials and restarted controller deployment"
done