#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

This script validates that file config/iam/recommended-policy-arn exists for a
service controller repository.

Creating an IAM Role with IAM Policy mentioned in recommended-policy-arn file is
a required step for ACK installation guide.

Environment variables:
  SERVICE: Name of the AWS service
"

# find out the service name from the prow environment variables.
AWS_SERVICE=$(echo "$SERVICE" | tr '[:upper:]' '[:lower:]')
SCRIPTS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
TEST_INFRA_DIR="$SCRIPTS_DIR/.."
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CONTROLLER_NAME="$AWS_SERVICE"-controller
SERVICE_CONTROLLER_DIR="$WORKSPACE_DIR/$CONTROLLER_NAME"
RECOMMENDED_POLICY_RELATIVE_PATH="config/iam/recommended-policy-arn"

source "$TEST_INFRA_DIR"/scripts/lib/common.sh
check_is_installed tr
check_is_installed aws

cd "$SERVICE_CONTROLLER_DIR"

echo -n "test-recommended-policy.sh][INFO] Checking presence of recommended-policy-arn ... "
if [[ ! -f $RECOMMENDED_POLICY_RELATIVE_PATH ]]; then
  echo ""
  echo "test-recommended-policy.sh][ERROR] Missing $RECOMMENDED_POLICY_RELATIVE_PATH for $CONTROLLER_NAME. Exiting"
  exit 1
fi
echo "ok"

RECOMMENDED_POLICY_ARN=$(tr -d '[:space:]' < $RECOMMENDED_POLICY_RELATIVE_PATH)
echo -n "test-recommended-policy.sh][INFO] Validating that recommended policy is an actual AWS IAM policy ... "
if ! aws iam get-policy --policy-arn "$RECOMMENDED_POLICY_ARN" >/dev/null; then
  echo ""
  echo "test-recommended-policy.sh][ERROR] $RECOMMENDED_POLICY_RELATIVE_PATH should contain only single valid IAM policy"
  print_line_separation
  echo "test-recommended-policy.sh][INFO] Current content of config/iam/recommended-policy-arn"
  cat $RECOMMENDED_POLICY_RELATIVE_PATH
  print_line_separation
  exit 1
fi
echo "ok"
