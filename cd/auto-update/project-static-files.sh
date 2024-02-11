#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  PR_SOURCE_BRANCH:    Name of the GitHub branch where auto-updated project description
                       files are pushed. Defaults to 'prow/auto-update'
  PR_TARGET_BRANCH:    Name of the GitHub branch where the PR should merge the
                       code. Defaults to 'main'
  GITHUB_ORG:          Name of the GitHub organisation where GitHub issues will
                       be created when auto-update of project description files
                       for service controller fails. Defaults to 'aws-controllers-k8s'
  GITHUB_ISSUE_REPO:   Name of the GitHub repository where GitHub issues will
                       be created when auto-update of project description files
                       for service controller fails. Defaults to 'community'
  GITHUB_LABEL:        Label to add to issue and pull requests.
                       Defaults to 'prow/auto-gen'
  GITHUB_LABEL_COLOR:  Color for GitHub label. Defaults to '3C6110'
  GITHUB_ACTOR:        Name of the GitHub account creating the issues & PR.
  GITHUB_DOMAIN:       Domain for GitHub. Defaults to 'github.com'
  GITHUB_EMAIL_PREFIX: The 7 digit unique id for no-reply email of
                       '$GITHUB_ACTOR'
  GITHUB_TOKEN:        Personal Access Token for '$GITHUB_ACTOR'
"

# Important Directory references based on prowjob configuration.
THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CONTROLLER_BOOTSTRAP_DIR=$WORKSPACE_DIR/controller-bootstrap

DEFAULT_PR_TARGET_BRANCH="main"
PR_TARGET_BRANCH=${PR_TARGET_BRANCH:-$DEFAULT_PR_TARGET_BRANCH}

LOCAL_GIT_BRANCH="main"

DEFAULT_GITHUB_ISSUE_ORG="aws-controllers-k8s"
GITHUB_ORG=${GITHUB_ORG:-$DEFAULT_GITHUB_ISSUE_ORG}

DEFAULT_GITHUB_ISSUE_REPO="community"
GITHUB_ISSUE_REPO=${GITHUB_ISSUE_REPO:-$DEFAULT_GITHUB_ISSUE_REPO}

GITHUB_ISSUE_ORG_REPO="$GITHUB_ORG/$GITHUB_ISSUE_REPO"

DEFAULT_GITHUB_LABEL="prow/auto-gen"
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

# Find the controller-bootstrap semver from the latest tag on the controller-bootstrap repo
ACK_CONTROLLER_BOOTSTRAP_VERSION=$(git describe --tags --always --dirty)

DEFAULT_PR_SOURCE_BRANCH="ack-bot/controller-bootstrap-$ACK_CONTROLLER_BOOTSTRAP_VERSION"
PR_SOURCE_BRANCH=${PR_SOURCE_BRANCH:-$DEFAULT_PR_SOURCE_BRANCH}

# find all the directories whose name ends with 'controller'
pushd "$WORKSPACE_DIR" >/dev/null
  CONTROLLER_NAMES=$(find . -maxdepth 1 -name "*-controller" -type d | cut -d"/" -f2)
popd >/dev/null

for CONTROLLER_NAME in $CONTROLLER_NAMES; do
  SERVICE_NAME=$(echo "$CONTROLLER_NAME"| sed 's/-controller$//g')
  CONTROLLER_DIR="$WORKSPACE_DIR/$CONTROLLER_NAME"
  cd "$CONTROLLER_BOOTSTRAP_DIR"

  # Prow will sync the labels... so we don't need to check if the label exists

  echo "project-static-files.sh][INFO] Updating project description files of existing controller using command 'make run'"
  export SERVICE=$SERVICE_NAME
  MAKE_RUN_OUTPUT_FILE=/tmp/"$SERVICE_NAME"_make_run_output
  MAKE_RUN_ERROR_FILE=/tmp/"$SERVICE_NAME"_make_run_error
  if ! make run > "$MAKE_RUN_OUTPUT_FILE" 2> "$MAKE_RUN_ERROR_FILE"; then
    cat "$MAKE_RUN_ERROR_FILE"

    echo "project-static-files.sh][ERROR] Failure while executing 'make run' command. Creating/Updating GitHub issue"
    ISSUE_TITLE="Errors while updating project description files for $CONTROLLER_NAME"

    # Capture 'make run' command output & error, then persist
    # in '$GITHUB_ISSUE_BODY_FILE'
    MAKE_RUN_OUTPUT=$(cat "$MAKE_RUN_OUTPUT_FILE")
    MAKE_RUN_ERROR_OUTPUT=$(cat "$MAKE_RUN_ERROR_FILE")
    GITHUB_ISSUE_BODY_TEMPLATE_FILE="$THIS_DIR/gh_issue_update_controller_template.txt"
    GITHUB_ISSUE_BODY_FILE=/tmp/"$SERVICE_NAME"_gh_issue_update_controller
    eval "echo \"$(cat "$GITHUB_ISSUE_BODY_TEMPLATE_FILE")\"" > $GITHUB_ISSUE_BODY_FILE

    open_gh_issue "$GITHUB_ISSUE_ORG_REPO" "$ISSUE_TITLE" "$GITHUB_ISSUE_BODY_FILE"
    # Skip creating PR for this service controller after updating GitHub issue.
    continue
  fi

  # Since there are no failures, print make run output in prowjob logs
  cat "$MAKE_RUN_OUTPUT_FILE"
  pushd "$CONTROLLER_DIR" >/dev/null
    GITHUB_CONTROLLER_ORG_REPO="$GITHUB_ORG/$CONTROLLER_NAME"

    # add git remote
    echo -n "project-static-files.sh][INFO] Adding git remote ... "
    if ! git remote add origin "https://github.com/$GITHUB_ORG/$CONTROLLER_NAME.git" >/dev/null; then
      echo ""
      echo "project-static-files.sh][ERROR] Unable to add git remote. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # Ensure there are changes in project description file(s) to commit
    fc=$(git diff --name-only | cat | wc -l | tr -d ' ')
    if [[ $fc -eq 0 ]]; then
        echo "project-static-files.sh][ERROR] no changes to commit for $CONTROLLER_NAME"
        continue
    fi

    # Add all the files & create a GitHub commit
    git add .
    COMMIT_MSG="Update project description files"
    echo -n "project-static-files.sh][INFO] Adding commit with message: '$COMMIT_MSG' ... "
    if ! git commit -m "$COMMIT_MSG" >/dev/null; then
      echo ""
      echo "project-static-files.sh][ERROR] Failed to add commit message for $CONTROLLER_NAME repository. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # Force push the new changes into '$PR_SOURCE_BRANCH'
    echo -n "project-static-files.sh][INFO] Pushing changes to branch '$PR_SOURCE_BRANCH' ... "
    if ! git push --force "https://$GITHUB_TOKEN@github.com/$GITHUB_ORG/$CONTROLLER_NAME.git" "$LOCAL_GIT_BRANCH:$PR_SOURCE_BRANCH" >/dev/null 2>&1; then
      echo ""
      echo "project-static-files.sh][ERROR] Failed to push the latest changes into remote repository. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"
    # fetch all remotes to bring changes locally
    git fetch --all >/dev/null
    # local branch name cannot be 'main' otherwise PR creation to target 'main' branch will fail
    # checkout new local branch from remote PR source
    git checkout -b controller-bootstrap-"$ACK_CONTROLLER_BOOTSTRAP_VERSION" origin/"$PR_SOURCE_BRANCH" >/dev/null
    # sync local branch with the origin, if there is a diff the gh pr command
    # prompts for user input
    git pull --rebase >/dev/null

    # Capture 'make run' command output, then persist
    # in '$GITHUB_PR_BODY_FILE'
    MAKE_RUN_OUTPUT=$(cat "$MAKE_RUN_OUTPUT_FILE")
    GITHUB_PR_BODY_TEMPLATE_FILE="$THIS_DIR/gh_pr_body_template.txt"
    GITHUB_PR_BODY_FILE=/tmp/"$SERVICE_NAME"_gh_pr_body_update_controller
    eval "echo \"$(cat "$GITHUB_PR_BODY_TEMPLATE_FILE")\"" > $GITHUB_PR_BODY_FILE

    open_pull_request "$GITHUB_CONTROLLER_ORG_REPO" "$COMMIT_MSG" "$GITHUB_PR_BODY_FILE"
    echo "project-static-files.sh][INFO] Done :) "
    # PRs created from this script trigger the presubmit prowjobs.
    # To control the number of presubmit prowjobs that will run in parallel,
    # adding sleep of 2 minutes. This will help distribute the load on prow
    # cluster.
    echo "project-static-files.sh][INFO] Sleeping for 2 minutes before updating project description files for next service controller."
    sleep 120
  popd >/dev/null
done
