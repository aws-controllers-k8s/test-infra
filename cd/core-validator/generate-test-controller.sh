#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  SERVICE: Name of the AWS service. Ex: ecr
"

# Important Directory references based on prowjob configuration.
THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
CORE_VALIDATOR_DIR=$THIS_DIR
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CODEGEN_DIR=$WORKSPACE_DIR/code-generator
CONTROLLER_NAME="$SERVICE"-controller
CONTROLLER_DIR="$WORKSPACE_DIR/$CONTROLLER_NAME"

source "$TEST_INFRA_DIR"/scripts/lib/common.sh
source "$TEST_INFRA_DIR"/scripts/lib/aws.sh

# Update the go.mod file in controller directory
# Findout the runtime semver from the code-generator repo
pushd "$CODEGEN_DIR" >/dev/null
  ACK_RUNTIME_VERSION=$(go list -m -f '{{ .Version }}' github.com/aws-controllers-k8s/runtime)
  if [[ -z $ACK_RUNTIME_VERSION ]]; then
    echo "generate-test-controller.sh][ERROR] Unable to determine ACK runtime version from code-generator/go.mod file. Exiting"
    exit 1
  else
    echo "generate-test-controller.sh][INFO] ACK runtime version in code-generator/go.mod file is $ACK_RUNTIME_VERSION"
  fi
popd >/dev/null

# Update go.mod file
pushd "$CONTROLLER_DIR" >/dev/null
  if [[ ! -f "$CONTROLLER_DIR/go.mod" ]]; then
    echo "generate-test-controller.sh][ERROR] Missing 'go.mod' file in $CONTROLLER_NAME"
    exit 1
  fi

  echo -n "generate-test-controller.sh][INFO] Updating 'go.mod' file for $CONTROLLER_NAME with ACK runtime $ACK_RUNTIME_VERSION ... "
  if ! go get -u github.com/aws-controllers-k8s/runtime@"$ACK_RUNTIME_VERSION" >/dev/null; then
    echo ""
    echo "generate-test-controller.sh][ERROR] Unable to update go.mod file with ACK runtime version $ACK_RUNTIME_VERSION"
    exit 1
  fi
  echo "ok"
popd >/dev/null

print_line_separation
# Use code-generator to generate new version of service controller
pushd "$CODEGEN_DIR" >/dev/null
  echo "generate-test-controller.sh][INFO] Generating new controller code using command 'make build-controller'"
  if ! make build-controller; then
    echo "generate-test-controller.sh][ERROR] Failed to generate new controller. Exiting ..."
    exit 1
  fi
popd >/dev/null

print_line_separation
# Perform unit test for newly generated controller
echo "generate-test-controller.sh][INFO] Performing unit tests"
cd "$CONTROLLER_DIR"
make test

print_line_separation
echo "generate-test-controller.sh][INFO] Performing e2e and helm tests"
# Perform make kind-test for the service controller
cd "$TEST_INFRA_DIR"
make kind-test
