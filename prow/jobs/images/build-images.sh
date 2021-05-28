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

for IMAGE_TYPE in deploy test integration unit; do
  docker build -f "$IMAGE_DIR/Dockerfile.$IMAGE_TYPE" --quiet=$QUIET -t "prow/$IMAGE_TYPE" "${IMAGE_DIR}"
done