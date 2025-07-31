#!/bin/bash

set -eo pipefail


USAGE="
Usage:
  $(basename "$0")

Environment variables:
  GITHUB_ACTOR:        Name of the GitHub account creating the issues & PR.
  GITHUB_TOKEN:        Personal Access Token for '$GITHUB_ACTOR'
  GITHUB_ORG:          Name of the GitHub organization.
  GITHUB_EMAIL_PREFIX  Email prefix to use for GitHub commits.
  PR_TARGET_BRANCH:    Name of the GitHub branch where the PR should merge the
                       code. Defaults to 'main'
"

# Default values
SERVICE=""
RESOURCE=""
SCRIPT_NAME="prow-job.sh"



# Parse command line arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    --service)
      SERVICE="$2"
      shift 2
      ;;
    --resource)
      RESOURCE="$2"
      shift 2
      ;;
    *)
      # Unknown option
      shift
      ;;
  esac
done

# Validate that service and resource are set
if [ -z "$SERVICE" ]; then
  echo "Error: --service argument is required"
  exit 1
fi

if [ -z "$RESOURCE" ]; then
  echo "Error: --resource argument is required"
  exit 1
fi

DEFAULT_PR_TARGET_BRANCH="main"
PR_TARGET_BRANCH=${PR_TARGET_BRANCH:-$DEFAULT_PR_TARGET_BRANCH}
WORKFLOW_DIR=$(pwd)
JOB_USER="prow"
SERVICE_REPO=$SERVICE-controller
ORG_REPO=$GITHUB_ORG/$SERVICE-controller
REPO_ROOT="/home/$JOB_USER/aws-controllers-k8s"
SERVICE_REPO_DIR="$REPO_ROOT/$SERVICE-controller"
LOCAL_GIT_BRANCH=$SERVICE-add-$RESOURCE
PR_SOURCE_BRANCH=$LOCAL_GIT_BRANCH

echo "$SCRIPT_NAME][INFO] Running resource-addition workflow for service: $SERVICE, resource: $RESOURCE"
echo "$SCRIPT_NAME][INFO] Target repository: $GITHUB_ORG/$SERVICE-controller"

USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${USER_EMAIL}"
fi

# set the GitHub configuration for using GitHub cli.
git config --global user.name "${GITHUB_ACTOR}" >/dev/null
git config --global user.email "${USER_EMAIL}" >/dev/null

mkdir -p $REPO_ROOT && cd $REPO_ROOT

# Create a fork of the repository
echo "$SCRIPT_NAME][INFO] forking and cloning $GITHUB_ORG/$SERVICE_REPO... "
if ! gh repo fork "$GITHUB_ORG/$SERVICE_REPO" --clone=true --remote=true >/dev/null; then
echo ""
echo "$SCRIPT_NAME][ERROR] failed to fork and clone $GITHUB_ORG/$SERVICE_REPO. Exiting "
exit 1
fi
echo "ok"

cd $WORKFLOW_DIR

# Run the workflow command
echo "$SCRIPT_NAME][INFO] Starting workflow"
python -m workflows resource-addition --service $SERVICE --resource $RESOURCE
echo "$SCRIPT_NAME][INFO]Resource addition workflow completed successfully"

cd $SERVICE_REPO_DIR

# Create a new branch
echo "$SCRIPT_NAME][INFO] Creating a new branch..."
git checkout -b $LOCAL_GIT_BRANCH >/dev/null

# Commit changes
echo "$SCRIPT_NAME][INFO] Committing changes..."
git add -A  >/dev/null
COMMIT_MSG="Add $RESOURCE to $SERVICE"
git commit -am "$COMMIT_MSG" >/dev/null

# Push changes to the forked repository
echo "$SCRIPT_NAME][INFO] Pushing changes to the forked repository..."
git push --force "https://$GITHUB_TOKEN@github.com/$GITHUB_ACTOR/$SERVICE_REPO.git" \
   "$LOCAL_GIT_BRANCH:$PR_SOURCE_BRANCH" &>/dev/null

# fetch all remotes to bring changes locally
git fetch --all >/dev/null
# set local branch to track origin(PR source)
git branch "$LOCAL_GIT_BRANCH" --set-upstream-to origin/"$PR_SOURCE_BRANCH" >/dev/null
# sync local branch with the origin, if there is a diff the gh pr command
# prompts for user input
git pull --rebase >/dev/null

echo "$SCRIPT_NAME][INFO] Creating a new pull request for $ORG_REPO , from $PR_SOURCE_BRANCH -> $PR_TARGET_BRANCH branch... "
if ! gh pr create -R "$ORG_REPO" -t "$COMMIT_MSG" -b "ACK Agent changes adding $RESOURCE to $SERVICE-controller" -B "$PR_TARGET_BRANCH" >/dev/null ; then
  echo ""
  echo "gh.sh][ERROR] Failed to create pull request. Exiting... "
  return 1
fi
echo "ok"

