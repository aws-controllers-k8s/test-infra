#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

This script is triggered as a postsubmit job when a PR is merged into a
service controller's main branch. It checks whether the merged PR has a
'release/minor' or 'release/patch' label. If so, it computes the next
release version, runs build-controller-release.sh, and opens (or updates)
a release PR.

Environment variables:
  REPO_NAME:           Name of the service controller repository.
                       This variable is injected into the pod by Prow.
  GITHUB_ORG:          Name of the GitHub organisation.
                       Defaults to TEST_INFRA_ORG.
  GITHUB_ACTOR:        Name of the GitHub account creating the PR.
  GITHUB_DOMAIN:       Domain for GitHub. Defaults to 'github.com'
  GITHUB_EMAIL_PREFIX: The 7 digit unique id for no-reply email of '\$GITHUB_ACTOR'
  GITHUB_TOKEN:        Personal Access Token for '\$GITHUB_ACTOR'
"

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CODEGEN_DIR=$WORKSPACE_DIR/code-generator

AWS_SERVICE=$(echo "$REPO_NAME" | rev | cut -d"-" -f2- | rev | tr '[:upper:]' '[:lower:]')
CONTROLLER_NAME="$AWS_SERVICE"-controller
CONTROLLER_DIR="$WORKSPACE_DIR/$CONTROLLER_NAME"

DEFAULT_GITHUB_ORG="${TEST_INFRA_ORG}"
GITHUB_ORG=${GITHUB_ORG:-$DEFAULT_GITHUB_ORG}

PR_TARGET_BRANCH="main"

DEFAULT_GITHUB_LABEL="prow/auto-gen"
GITHUB_LABEL=${GITHUB_LABEL:-$DEFAULT_GITHUB_LABEL}

MISSING_GIT_TAG="missing-git-tag"

source "$TEST_INFRA_DIR"/scripts/lib/common.sh
source "$CD_DIR"/lib/gh.sh
check_is_installed git
check_is_installed gh
check_is_installed yq

USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${USER_EMAIL}"
fi

git config --global user.name "${GITHUB_ACTOR}" >/dev/null
git config --global user.email "${USER_EMAIL}" >/dev/null

cd "$CONTROLLER_DIR"

echo "controller-release-pr.sh][INFO] Determining merged PR number from latest commit"
COMMIT_TITLE=$(git log --format="%s" -n 1 HEAD)
PR_NUMBER=$(extract_pr_number "$COMMIT_TITLE")

if [[ -z "$PR_NUMBER" ]]; then
  echo "controller-release-pr.sh][INFO] Could not extract PR number from commit title: $COMMIT_TITLE"
  echo "controller-release-pr.sh][INFO] Exiting (no-op)"
  exit 0
fi

echo "controller-release-pr.sh][INFO] Merged PR: #$PR_NUMBER"

# Fetch labels from the merged PR
PR_LABELS=$(get_pr_labels "$GITHUB_ORG/$CONTROLLER_NAME" "$PR_NUMBER")
echo "controller-release-pr.sh][INFO] PR labels: $PR_LABELS"

# Determine release type from labels
RELEASE_TYPE=""
case "$PR_LABELS" in
    *"release/patch"*"release/minor"*|*"release/minor"*"release/patch"*)
        echo "controller-release-pr.sh][ERROR] PR has both release/minor and release/patch labels. Exiting."
        exit 1 ;;
    *"release/patch"*)
        RELEASE_TYPE="patch" ;;
    *"release/minor"*)
        RELEASE_TYPE="minor" ;;
    *)
        echo "controller-release-pr.sh][INFO] No release label found on PR #$PR_NUMBER. Exiting (no-op)."
        exit 0 ;;
esac

echo "controller-release-pr.sh][INFO] Release type: $RELEASE_TYPE"

# Compute next version from latest git tag
LATEST_TAG=$(git describe --abbrev=0 --tags 2>/dev/null || echo "$MISSING_GIT_TAG")
if [[ $LATEST_TAG == "$MISSING_GIT_TAG" ]]; then
  echo "controller-release-pr.sh][ERROR] No git tags found for $CONTROLLER_NAME. Cannot determine release version."
  exit 1
fi

RELEASE_VERSION=$(compute_next_version "$LATEST_TAG" "$RELEASE_TYPE")
echo "controller-release-pr.sh][INFO] Latest tag: $LATEST_TAG -> Next version: $RELEASE_VERSION"

PR_SOURCE_BRANCH="ack-bot/release-$RELEASE_VERSION"
export PR_SOURCE_BRANCH
export PR_TARGET_BRANCH

COMMIT_MSG="Release artifacts for release $RELEASE_VERSION"

# Check for existing release PRs with this version
echo "controller-release-pr.sh][INFO] Checking for existing release PRs"
EXISTING_MERGED_PR=$(gh pr list -R "$GITHUB_ORG/$CONTROLLER_NAME" -s merged --head "$PR_SOURCE_BRANCH" -L 1 --json number --jq '.[0].number')
if [[ -n "$EXISTING_MERGED_PR" ]]; then
  echo "controller-release-pr.sh][ERROR] A release PR for $RELEASE_VERSION has already been merged (PR #$EXISTING_MERGED_PR). Exiting."
  exit 1
fi

# Run build-controller-release.sh
echo "controller-release-pr.sh][INFO] Running build-controller-release.sh for $AWS_SERVICE with RELEASE_VERSION=$RELEASE_VERSION"
export RELEASE_VERSION
export SERVICE_CONTROLLER_SOURCE_PATH="$CONTROLLER_DIR"
cd "$CODEGEN_DIR"

if ! make build-ack-generate >/dev/null 2>&1; then
  echo "controller-release-pr.sh][ERROR] Failed to build ack-generate"
  exit 1
fi

./scripts/install-controller-gen.sh

if ! ./scripts/build-controller-release.sh "$AWS_SERVICE"; then
  echo "controller-release-pr.sh][ERROR] build-controller-release.sh failed"
  exit 1
fi

cd "$CONTROLLER_DIR"

# Stage all changes and commit
git add .
echo "controller-release-pr.sh][INFO] Committing release artifacts"
if ! git commit -m "$COMMIT_MSG" >/dev/null; then
  echo "controller-release-pr.sh][ERROR] Nothing to commit — release artifacts unchanged?"
  exit 1
fi

# Push to release branch
echo "controller-release-pr.sh][INFO] Pushing to branch $PR_SOURCE_BRANCH"
if ! git push --force "https://$GITHUB_TOKEN@github.com/$GITHUB_ORG/$CONTROLLER_NAME.git" "HEAD:$PR_SOURCE_BRANCH" >/dev/null 2>&1; then
  echo "controller-release-pr.sh][ERROR] Failed to push to $PR_SOURCE_BRANCH"
  exit 1
fi

# Build list of PRs merged since last tag
PR_BODY_FILE=/tmp/release_pr_body
{
  echo "Releasing changes:"
  git log "$LATEST_TAG"..main --oneline --grep='(#' | grep -oE '\(#[0-9]+\)' | grep -oE '[0-9]+' | while read -r pr_num; do
    echo "* https://github.com/$GITHUB_ORG/$CONTROLLER_NAME/pull/$pr_num"
  done
} > "$PR_BODY_FILE"

open_pull_request "$GITHUB_ORG/$CONTROLLER_NAME" "$COMMIT_MSG" "$PR_BODY_FILE"

echo "controller-release-pr.sh][INFO] Done :)"
