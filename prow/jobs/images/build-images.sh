#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Builds each of the Dockerfiles used by the CI/CD system.

Example:
$(basename "$0")

Environment variables:
  QUIET:                    Build container images quietly (<true|false>)
                            Default: false
"

# Important Directory references
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
IMAGE_DIR=$DIR

QUIET=${QUIET:-"false"}

# check_is_installed docker

docker build -f "$IMAGE_DIR/Dockerfile.deploy" --quiet=$QUIET -t "prow/deploy" "${IMAGE_DIR}"
docker build -f "$IMAGE_DIR/Dockerfile.docs" --quiet=$QUIET -t "prow/docs" "${IMAGE_DIR}"
docker build -f "$IMAGE_DIR/Dockerfile.olm-test" --quiet=$QUIET -t "prow/olm-test" "${IMAGE_DIR}"
docker build -f "$IMAGE_DIR/Dockerfile.auto-generate-controllers" --quiet=$QUIET -t "prow/auto-generate-controllers" "${IMAGE_DIR}"
docker build -f "$IMAGE_DIR/Dockerfile.controller-release-tag" --quiet=$QUIET -t "prow/controller-release-tag" "${IMAGE_DIR}"
docker build -f "$IMAGE_DIR/Dockerfile.soak" --quiet=$QUIET --build-arg DEPLOY_BASE_TAG="prow/deploy" -t "prow/soak" "${IMAGE_DIR}"

export TEST_BASE_TAG=$(echo "prow/test-$(uuidgen | cut -c1-8)" | tr '[:upper:]' '[:lower:]')
docker build -f "$IMAGE_DIR/Dockerfile.test" --quiet=$QUIET -t $TEST_BASE_TAG "${IMAGE_DIR}"

for IMAGE_TYPE in integration unit; do
  docker build -f "$IMAGE_DIR/Dockerfile.$IMAGE_TYPE" --quiet=$QUIET --build-arg TEST_BASE_TAG -t "prow/$IMAGE_TYPE" "${IMAGE_DIR}"
done