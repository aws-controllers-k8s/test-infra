#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  REPO_NAME:           Name of the service controller repository. Ex: apigatewayv2-controller
                       This variable is injected into the pod by Prow.
  PULL_BASE_REF:       The value of tag on service controller repository that triggered the
                       postsubmit prowjob. The value will be in the format '^v\d+\.\d+\.\d+$'
                       This variable is injected into the pod by Prow.
  PR_SOURCE_BRANCH:    Name of the GitHub branch where auto-generated olm bundle
                       files are pushed. Defaults to 'olm-bundle-$SERVICE-$OLM_BUNDLE_VERSION'
  PR_TARGET_BRANCH:    Name of the GitHub branch where the PR should merge the
                       code. Defaults to 'main'
  GITHUB_ISSUE_ORG:    Name of the GitHub organisation where GitHub issues will
                       be created when autogeneration of olm bundle fails.
                       Defaults to 'aws-controllers-k8s'
  GITHUB_ISSUE_REPO:   Name of the GitHub repository where GitHub issues will
                       be created when autogeneration of olm bundle fails.
                       Defaults to 'community'
  GITHUB_LABEL:        Label to add to GitHub issue.
                       Defaults to 'ack-bot-olm'
  GITHUB_ACTOR:        Name of the GitHub account creating the issues & PR.
  GITHUB_DOMAIN:       Domain for GitHub. Defaults to 'github.com'
  GITHUB_EMAIL_PREFIX: The 7 digit unique id for no-reply email of
                       '$GITHUB_ACTOR'
  GITHUB_TOKEN:        Personal Access Token for '$GITHUB_ACTOR'
"

# find out the service name and semver tag from the prow environment variables.
CONTROLLER_NAME=$REPO_NAME
SERVICE=$(echo "$REPO_NAME" | rev | cut -d"-" -f2- | rev | tr '[:upper:]' '[:lower:]')
RELEASE_VERSION=$PULL_BASE_REF
# Drop 'v' from controller semver to find olm bundle version
OLM_BUNDLE_VERSION=$(echo "$RELEASE_VERSION" | awk -F v '{print $NF}')
echo "olm-bundle-pr.sh][INFO] olm bundle version is $OLM_BUNDLE_VERSION"
export ACK_GENERATE_OLM=true

# Important Directory references based on prowjob configuration.
THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
OLM_DIR=$THIS_DIR
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CODEGEN_DIR=$WORKSPACE_DIR/code-generator
CONTROLLER_DIR=$WORKSPACE_DIR/$CONTROLLER_NAME

DEFAULT_PR_TARGET_BRANCH="main"
PR_TARGET_BRANCH=${PR_TARGET_BRANCH:-$DEFAULT_PR_TARGET_BRANCH}

LOCAL_GIT_BRANCH="olm-bundle-$SERVICE-$OLM_BUNDLE_VERSION"
PR_SOURCE_BRANCH=$LOCAL_GIT_BRANCH

DEFAULT_GITHUB_ISSUE_ORG="aws-controllers-k8s"
GITHUB_ISSUE_ORG=${GITHUB_ISSUE_ORG:-$DEFAULT_GITHUB_ISSUE_ORG}

DEFAULT_GITHUB_ISSUE_REPO="community"
GITHUB_ISSUE_REPO=${GITHUB_ISSUE_REPO:-$DEFAULT_GITHUB_ISSUE_REPO}

GITHUB_ISSUE_ORG_REPO="$GITHUB_ISSUE_ORG/$GITHUB_ISSUE_REPO"

DEFAULT_GITHUB_LABEL="ack-bot-olm"
GITHUB_LABEL=${GITHUB_LABEL:-$DEFAULT_GITHUB_LABEL}

# Check all the dependencies are present in container.
source "$TEST_INFRA_DIR"/scripts/lib/common.sh
source "$CD_DIR"/lib/gh.sh
check_is_installed git
check_is_installed gh

USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${USER_EMAIL}"
fi

# set the GitHub configuration for using GitHub cli.
git config --global user.name "${GITHUB_ACTOR}" >/dev/null
git config --global user.email "${USER_EMAIL}" >/dev/null

# Skip olm bundle creation if the 'olm/olmconfig.yaml' file is missing from
# service controller
pushd "$CONTROLLER_DIR" >/dev/null
  if [[ ! -f $CONTROLLER_DIR/olm/olmconfig.yaml ]]; then
    echo "olm-bundle-pr.sh][ERROR] olmconfig.yaml file is missing from $CONTROLLER_NAME. Exiting"
    ISSUE_TITLE="Missing \`olmconfig.yaml\` in \`$CONTROLLER_NAME\`"
    GITHUB_ISSUE_BODY_TEMPLATE_FILE="$THIS_DIR/gh_issue_missing_olmconfig_template.txt"
    GITHUB_ISSUE_BODY_FILE_PATH=/tmp/"$SERVICE"_gh_issue_body
    eval "echo \"$(cat "$GITHUB_ISSUE_BODY_TEMPLATE_FILE")\"" > "$GITHUB_ISSUE_BODY_FILE_PATH"
    open_gh_issue "$GITHUB_ISSUE_ORG_REPO" "$ISSUE_TITLE" "$GITHUB_ISSUE_BODY_FILE_PATH"
    # Skip creating PR for this service controller after updating GitHub issue.
    exit 1
  fi
popd >/dev/null

cd "$CODEGEN_DIR"
if ! make build-ack-generate >/dev/null; then
  echo "olm-bundle-pr.sh][ERROR] Failure while executing 'make build-ack-generate'"
  exit 1
fi

echo "olm-bundle-pr.sh][INFO] Generating olm bundle"
OLM_BUNDLE_STDOUT_FILE=/tmp/"$SERVICE"_olm_bundle_output
OLM_BUNDLE_STDERR_FILE=/tmp/"$SERVICE"_olm_bundle_error
if ! ./scripts/build-controller-release.sh "$SERVICE" > "$OLM_BUNDLE_STDOUT_FILE" 2>"$OLM_BUNDLE_STDERR_FILE"; then
  cat "$OLM_BUNDLE_STDERR_FILE"
  echo "olm-bundle-pr.sh][ERROR] Failure while generating olm-bundle. Creating/Updating GitHub issue"
  ISSUE_TITLE="Errors while generating olm bundle for \`$CONTROLLER_NAME-$RELEASE_VERSION\`"
  # Capture 'build-controller-release.sh' command output & error, then persist
  # in '$GITHUB_ISSUE_BODY_FILE_PATH'
  OLM_BUNDLE_STDOUT=$(cat "$OLM_BUNDLE_STDOUT_FILE")
  OLM_BUNDLE_STDERR=$(cat "$OLM_BUNDLE_STDERR_FILE")
  GITHUB_ISSUE_BODY_TEMPLATE_FILE="$THIS_DIR/gh_issue_olm_create_error_template.txt"
  GITHUB_ISSUE_BODY_FILE_PATH=/tmp/"$SERVICE"_gh_issue_body
  eval "echo \"$(cat "$GITHUB_ISSUE_BODY_TEMPLATE_FILE")\"" > "$GITHUB_ISSUE_BODY_FILE_PATH"
  open_gh_issue "$GITHUB_ISSUE_ORG_REPO" "$ISSUE_TITLE" "$GITHUB_ISSUE_BODY_FILE_PATH"
  # Skip creating PR for this service controller after updating GitHub issue.
  exit 1
fi

# OH stands for operator hub
for OH_ORG_REPO in k8s-operatorhub/community-operators redhat-openshift-ecosystem/community-operators-prod
do
  cd "$WORKSPACE_DIR"
  OH_ORG=$(echo "$OH_ORG_REPO" | cut -d"/" -f1)
  OH_REPO=$(echo "$OH_ORG_REPO" | cut -d"/" -f2)
  echo -n "olm-bundle-pr.sh][INFO] forking and cloning $OH_ORG_REPO... "
  if ! gh repo fork "$OH_ORG_REPO" --clone=true --remote=true >/dev/null; then
    echo ""
    echo "olm-bundle-pr.sh][ERROR] failed to fork and clone $OH_ORG_REPO. Exiting "
    exit 1
  fi
  echo "ok"
  cd "$OH_REPO"

  echo -n "olm-bundle-pr.sh][INFO] fetching latest changes from remotes... "
  if ! git fetch --all >/dev/null; then
    echo ""
    echo "olm-bundle-pr.sh][ERROR] failed to fetch latest changes for $OH_REPO"
    exit 1
  fi
  echo "ok"

  echo -n "olm-bundle-pr.sh][INFO] creating new local branch $LOCAL_GIT_BRANCH... "
  if ! git checkout -b "$LOCAL_GIT_BRANCH" upstream/main; then
    echo ""
    echo "olm-bundle-pr.sh][ERROR] failed to create new branch $LOCAL_GIT_BRANCH for $OH_REPO. Exiting "
    exit 1
  fi
  echo "ok"

  if [[ ! -f $WORKSPACE_DIR/$OH_REPO/operators/ack-$CONTROLLER_NAME/ci.yaml ]]; then
    #TODO: Automatically add ci.yaml if missing from the community-operators(-prod)
    # repos instead of creating a GitHub issue for it
    echo "olm-bundle-pr.sh][ERROR] ci.yaml file missing from $OH_ORG_REPO"
    ISSUE_TITLE="Missing ci.yaml file for \`$CONTROLLER_NAME\` in \`$OH_ORG_REPO\`"
    GITHUB_ISSUE_BODY_TEMPLATE_FILE="$THIS_DIR/gh_issue_missing_oh_ci_template.txt"
    GITHUB_ISSUE_BODY_FILE_PATH=/tmp/"$SERVICE"_gh_issue_body
    eval "echo \"$(cat "$GITHUB_ISSUE_BODY_TEMPLATE_FILE")\"" > "$GITHUB_ISSUE_BODY_FILE_PATH"
    open_gh_issue "$GITHUB_ISSUE_ORG_REPO" "$ISSUE_TITLE" "$GITHUB_ISSUE_BODY_FILE_PATH"
    # Skip creating PR for this service controller after updating GitHub issue.
    exit 1
  fi

  mkdir -p "operators/ack-$CONTROLLER_NAME/$OLM_BUNDLE_VERSION"
  cd "$WORKSPACE_DIR/$OH_REPO/operators/ack-$CONTROLLER_NAME/$OLM_BUNDLE_VERSION"
  cp -R "$WORKSPACE_DIR/$CONTROLLER_NAME/olm/bundle/manifests" . >/dev/null
  cp -R "$WORKSPACE_DIR/$CONTROLLER_NAME/olm/bundle/metadata" . >/dev/null
  cp -R "$WORKSPACE_DIR/$CONTROLLER_NAME/olm/bundle/tests" . >/dev/null
  cp "$WORKSPACE_DIR/$CONTROLLER_NAME/olm/bundle.Dockerfile" . >/dev/null

  cd "$WORKSPACE_DIR/$OH_REPO"
  git add . >/dev/null
  COMMIT_MSG="ack-$CONTROLLER_NAME artifacts for version $OLM_BUNDLE_VERSION"
  git commit -m "$COMMIT_MSG" --signoff > /dev/null
  git push --force "https://$GITHUB_TOKEN@github.com/$GITHUB_ACTOR/$OH_REPO.git" \
   "$LOCAL_GIT_BRANCH:$PR_SOURCE_BRANCH" >/dev/null
  # fetch all remotes to bring changes locally
  git fetch --all >/dev/null
  # set local branch to track origin(PR source)
  git branch "$LOCAL_GIT_BRANCH" --set-upstream-to origin/"$PR_SOURCE_BRANCH" >/dev/null
  # sync local branch with the origin, if there is a diff the gh pr command
  # prompts for user input
  git pull --rebase >/dev/null
  # Capture 'build-controller-release.sh' command output, then persist
  # in '$GITHUB_PR_BODY_FILE_PATH'
  GITHUB_PR_BODY_TEMPLATE_FILE="$THIS_DIR/gh_pr_body_template.txt"
  GITHUB_PR_BODY_FILE_PATH=/tmp/"$SERVICE"_gh_pr_body
  eval "echo \"$(cat "$GITHUB_PR_BODY_TEMPLATE_FILE")\"" > "$GITHUB_PR_BODY_FILE_PATH"
  # Because pull request is being opened in operatorhub repositories, 'ack-bot-olm'
  # label does not exist in those repos. Unsetting the variable will not add this
  # label while creating pull requests.
  unset GITHUB_LABEL
  if ! open_pull_request "$OH_ORG_REPO" "$COMMIT_MSG" "$GITHUB_PR_BODY_FILE_PATH"; then
    exit 1
  fi
done
echo "Done :)"
