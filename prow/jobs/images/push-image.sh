#!/usr/bin/env bash

set -eo pipefail

VERSION=${VERSION:-$(git describe --tags --always --dirty || echo "unknown")}

DEFAULT_DOCKER_REPOSITORY="public.ecr.aws/aws-controllers-k8s/prow"
DOCKER_REPOSITORY=${DOCKER_REPOSITORY:-"$DEFAULT_DOCKER_REPOSITORY"}

QUIET=${QUIET:-"false"}

USAGE="
Usage:
  $(basename "$0") IMAGE_TYPE

Pushes all of the tagged 'prow/*' images into a public ECR repository. Use
DOCKER_REPOSITORY to specify the ECR repository URI. Use VERSION to set the 
SemVer value in the image tag.

Example:
$(basename "$0") integration

Environment variables:
  VERSION:                  Provide the version to be inserted into the docker tag
                            Default: $VERSION
  DOCKER_REPOSITORY:        Public repository to push
                            Default: $DEFAULT_DOCKER_REPOSITORY
  QUIET:                    Build container images quietly (<true|false>)
                            Default: false
"

if [ $# -ne 1 ]; then
    echo "IMAGE_TYPE is not defined. Script accepts one parameter, <IMAGE_TYPE> to publish the Docker images of the given type." 1>&2
    echo "${USAGE}"
    exit 1
fi

IMAGE_TYPE=$(echo "$1" | tr '[:upper:]' '[:lower:]')

# Important Directory references
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
IMAGE_DIR=$DIR

docker_login() {
  #ecr-public only exists in us-east-1 so use that region specifically
  local __pw=$(aws ecr-public get-login-password --region us-east-1)
  echo "$__pw" | docker login -u AWS --password-stdin public.ecr.aws
}

image_tag() {
  local __image_type="$1"

  echo "$DOCKER_REPOSITORY:prow-${__image_type}-$VERSION"
}

docker_login

docker tag prow/$IMAGE_TYPE $(image_tag $IMAGE_TYPE)
docker push $(image_tag $IMAGE_TYPE)