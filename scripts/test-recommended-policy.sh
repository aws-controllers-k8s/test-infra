#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

This script validates that file config/iam/recommended-policy-arn exists for a
service controller repository and that each of the policies in the file are
valid. If the file does not exist, it will check for the existence of a 
config/iam/recommended-inline-policy file.

Creating an IAM Role with all of the IAM Policies mentioned in
recommended-policy-arn file and the inline Policy in the
recommended-inline-policy is a required step for ACK installation guide.

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
RECOMMENDED_INLINE_POLICY_RELATIVE_PATH="config/iam/recommended-inline-policy"

source "$TEST_INFRA_DIR"/scripts/lib/common.sh
check_is_installed tr
check_is_installed aws

cd "$SERVICE_CONTROLLER_DIR"

# Check for existence of recommended policy ARN
echo -n "test-recommended-policy.sh][INFO] Checking presence of recommended-policy-arn ... "
if [[ ! -f $RECOMMENDED_POLICY_RELATIVE_PATH ]]; then
  echo ""
  echo "test-recommended-policy.sh][INFO] Missing $RECOMMENDED_POLICY_RELATIVE_PATH for $CONTROLLER_NAME"
  
  # Check for existence of recommended inline policy
  echo -n "test-recommended-policy.sh][INFO] Checking presence of recommended-inline-policy ... "
  if [[ ! -f $RECOMMENDED_INLINE_POLICY_RELATIVE_PATH ]]; then
    echo ""
    echo "test-recommended-policy.sh][ERROR] Missing $RECOMMENDED_INLINE_POLICY_RELATIVE_PATH for $CONTROLLER_NAME. Exiting"
    exit 1
  fi
  echo "ok"

  # Check for contents of recommended inline policy
  echo -n "test-recommended-policy.sh][INFO] Checking contents of recommended-inline-policy ... "
  if [[ ! -s $RECOMMENDED_INLINE_POLICY_RELATIVE_PATH ]]; then
    echo ""
    echo "test-recommended-policy.sh][ERROR] $RECOMMENDED_INLINE_POLICY_RELATIVE_PATH for $CONTROLLER_NAME is empty. Exiting"
    exit 1
  fi
  echo "ok"
  exit 0
fi
echo "ok"

# Check for valid contents of recommended policy ARN
echo -n "test-recommended-policy.sh][INFO] Validating that recommended policy file contains valid AWS IAM policy ARNs ... "
for POLICY_ARN in $(awk NF=NF FS="\n" $RECOMMENDED_POLICY_RELATIVE_PATH); do
  if ! aws iam get-policy --policy-arn "$POLICY_ARN" >/dev/null; then
    echo ""
    echo "test-recommended-policy.sh][ERROR] $POLICY_ARN is not a valid managed IAM policy ARN"
    print_line_separation
    echo "test-recommended-policy.sh][INFO] Current contents of config/iam/recommended-policy-arn:"
    cat $RECOMMENDED_POLICY_RELATIVE_PATH
    echo ""
    print_line_separation
    exit 1
  fi
done
echo "ok"
