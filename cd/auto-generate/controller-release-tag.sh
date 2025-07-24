#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

This script automatically tags the service controller repository with proper semver
to automate the controller release process.
The script currently only supports automating patch releases.
(TODO: Consider automating Minor and Major release for controllers as well)

The script compares latest git tag on service controller repository with
'image.tag' inside service controller's 'helm/values.yaml' file.
If the 'image.tag' value inside 'helm/values.yaml' file is next patch release,
then this scripts tags the service controller repository with 'image.tag' value
from 'helm/values.yaml' file

This tagging along with GitHub action for creating GitHub
release, kick starts the service controller release process.

Environment variables:
  REPO_NAME:           Name of the service controller repository. Ex: apigatewayv2-controller
                       This variable is injected into the pod by Prow.
  GITHUB_ORG:          Name of the GitHub organisation. Defaults to 'aws-controllers-k8s'
  GITHUB_ACTOR:        Name of the GitHub account creating the issues & PR.
  GITHUB_DOMAIN:       Domain for GitHub. Defaults to 'github.com'
  GITHUB_EMAIL_PREFIX: The 7 digit unique id for no-reply email of '$GITHUB_ACTOR'
  GITHUB_TOKEN:        Personal Access Token for '$GITHUB_ACTOR'
"

#TODO(vijtrip2): remove GitHub Action details from script description and
# onboarding guide once, the GitHub release process is moved completely to Prow

# find out the service name
AWS_SERVICE=$(echo "$REPO_NAME" | rev | cut -d"-" -f2- | rev | tr '[:upper:]' '[:lower:]')
CONTROLLER_NAME="$AWS_SERVICE"-controller

# Important Directory references based on prowjob configuration.
THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
AUTO_GEN_DIR=$THIS_DIR
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CONTROLLER_DIR="$WORKSPACE_DIR/$CONTROLLER_NAME"

DEFAULT_GITHUB_ORG="aws-controllers-k8s"
GITHUB_ORG=${GITHUB_ORG:-$DEFAULT_GITHUB_ORG}

MISSING_GIT_TAG="missing-git-tag"
MISSING_IMAGE_TAG="missing-image-tag"
NON_RELEASE_IMAGE_TAG="v0.0.0-non-release-version"

# Check all the dependencies are present in container.
source "$TEST_INFRA_DIR"/scripts/lib/common.sh
check_is_installed git
check_is_installed yq

USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${USER_EMAIL}"
fi

# set the GitHub configuration for using GitHub cli.
git config --global user.name "${GITHUB_ACTOR}" >/dev/null
git config --global user.email "${USER_EMAIL}" >/dev/null

cd "$CONTROLLER_DIR"

# Find latest Git tag on controller repo
echo "controller-release-tag.sh][INFO] Finding latest Git tag for $CONTROLLER_NAME"
LATEST_GIT_TAG=$(git describe --abbrev=0 --tags || echo "$MISSING_GIT_TAG")
if [[ $LATEST_GIT_TAG == $MISSING_GIT_TAG ]]; then
  echo "controller-release-tag.sh][INFO] No git tag exists for $CONTROLLER_NAME"
fi

# Find the image tag used in helm release artifacts. Prefix the tag with a v so
# it can be compared with the Git tags on the repository.
HELM_IMAGE_TAG="v$(yq eval '.image.tag' helm/values.yaml || echo "$MISSING_IMAGE_TAG")"
if [[ $HELM_IMAGE_TAG == $MISSING_IMAGE_TAG ]]; then
  echo "controller-release-tag.sh][ERROR] Unable to find image tag in helm/values.yaml for $CONTROLLER_NAME. Exiting"
  exit 1
fi

if [[ $HELM_IMAGE_TAG == $NON_RELEASE_IMAGE_TAG ]]; then
  echo "controller-release-tag.sh][INFO] Helm artifacts have $NON_RELEASE_IMAGE_TAG tag. Skipping $CONTROLLER_NAME"
  exit 0
fi

# Currently only supports auto-tagging for patch and minor releases
if [[ $LATEST_GIT_TAG == $MISSING_GIT_TAG ]]; then
  # If no git tag exist for controller, use v0.0.1 or v0.1.0 as next git tag
  NEXT_GIT_PATCH_TAG="v0.0.1"
  NEXT_GIT_MINOR_TAG="v0.1.0"
else
  NEXT_GIT_PATCH_TAG=$(echo "$LATEST_GIT_TAG" | awk -F. -v OFS=. '{$NF++;print}')
  NEXT_GIT_MINOR_TAG=$(echo "$LATEST_GIT_TAG" | awk -F. -v OFS=. '{$2++;$3=0;print}')
fi

# Validate HELM_IMAGE_TAG is either a patch tag or minor tag
if [[ $HELM_IMAGE_TAG != $NEXT_GIT_PATCH_TAG && $HELM_IMAGE_TAG != $NEXT_GIT_MINOR_TAG ]]; then
  echo "controller-release-tag.sh][ERROR] Helm image tag $HELM_IMAGE_TAG is neither the next patch nor the minor release for current $LATEST_GIT_TAG release"
  echo "controller-release-tag.sh][INFO] Not tagging the GitHub repository with $HELM_IMAGE_TAG tag"
  exit 0
fi

## tag the repo with same tag as helm release artifacts
echo -n "controller-release-tag.sh][INFO] Tagging $CONTROLLER_NAME locally with $HELM_IMAGE_TAG tag ... "
if ! git tag -a "$HELM_IMAGE_TAG" -m "$HELM_IMAGE_TAG" >/dev/null; then
  echo ""
  echo "controller-release-tag.sh][ERROR] Failed to tag $CONTROLLER_NAME. Exiting"
  exit 1
fi
echo "ok"

## push the tags to controller-repo
echo -n "controller-release-tag.sh][INFO] Pushing tags to $CONTROLLER_NAME  ... "
if ! git push "https://$GITHUB_TOKEN@github.com/$GITHUB_ORG/$CONTROLLER_NAME.git" --tags &>/dev/null; then
  echo ""
  echo "controller-release-tag.sh][ERROR] Failed to push tags for $CONTROLLER_NAME. Exiting"
  exit 1
fi
echo "ok"

echo "controller-release-tag.sh][INFO] Successfully tagged $CONTROLLER_NAME with $HELM_IMAGE_TAG tag"
echo "controller-release-tag.sh][INFO] GitHub release $HELM_IMAGE_TAG for $CONTROLLER_NAME will be automatically created"
echo "controller-release-tag.sh][INFO] $CONTROLLER_NAME image and chart for $HELM_IMAGE_TAG will also be automatically published"
echo "Done :) "
