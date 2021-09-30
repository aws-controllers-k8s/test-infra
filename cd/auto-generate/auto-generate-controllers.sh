#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  PR_SOURCE_BRANCH:    Name of the GitHub branch where auto-generated service
                       controller code is pushed. Defaults to 'ack-bot-autogen'
  PR_TARGET_BRANCH:    Name of the GitHub branch where the PR should merge the
                       code. Defaults to 'main'
  GITHUB_ORG:          Name of the GitHub organisation where GitHub issues will
                       be created when autogeneration of service controller fails.
                       Defaults to 'aws-controllers-k8s'
  GITHUB_ISSUE_REPO:   Name of the GitHub repository where GitHub issues will
                       be created when autogeneration of service controller fails.
                       Defaults to 'community'
  GITHUB_LABEL:        Label to add to issue and pull requests.
                       Defaults to 'ack-bot-autogen'
  GITHUB_LABEL_COLOR:  Color for GitHub label. Defaults to '3C6110'
  GITHUB_ACTOR:        Name of the GitHub account creating the issues & PR.
  GITHUB_DOMAIN:       Domain for GitHub. Defaults to 'github.com'
  GITHUB_EMAIL_PREFIX: The 7 digit unique id for no-reply email of
                       '$GITHUB_ACTOR'
  GITHUB_TOKEN:        Personal Access Token for '$GITHUB_ACTOR'
"

# Important Directory references based on prowjob configuration.
THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
AUTO_GEN_DIR=$THIS_DIR
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
CODEGEN_DIR=$WORKSPACE_DIR/code-generator

DEFAULT_PR_TARGET_BRANCH="main"
PR_TARGET_BRANCH=${PR_TARGET_BRANCH:-$DEFAULT_PR_TARGET_BRANCH}

LOCAL_GIT_BRANCH="main"

DEFAULT_GITHUB_ISSUE_ORG="aws-controllers-k8s"
GITHUB_ORG=${GITHUB_ORG:-$DEFAULT_GITHUB_ISSUE_ORG}

DEFAULT_GITHUB_ISSUE_REPO="community"
GITHUB_ISSUE_REPO=${GITHUB_ISSUE_REPO:-$DEFAULT_GITHUB_ISSUE_REPO}

DEFAULT_GITHUB_LABEL="ack-bot-autogen"
GITHUB_LABEL=${GITHUB_LABEL:-$DEFAULT_GITHUB_LABEL}

DEFAULT_GITHUB_LABEL_COLOR="3C6110"
GITHUB_LABEL_COLOR=${GITHUB_LABEL_COLOR:-$DEFAULT_GITHUB_LABEL_COLOR}

RUNTIME_MISSING_VERSION="missing-runtime-dependency"
MISSING_GITHUB_TAG="missing-github-tag"

# Check all the dependencies are present in container.
source "$TEST_INFRA_DIR"/scripts/lib/common.sh
check_is_installed git
check_is_installed gh

USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${USER_EMAIL}"
fi

# set the GitHub configuration for using GitHub cli.
git config --global user.name "${GITHUB_ACTOR}" >/dev/null
git config --global user.email "${USER_EMAIL}" >/dev/null

# Findout the runtime semver from the code-generator repo
cd "$CODEGEN_DIR"
ACK_RUNTIME_VERSION=$(go list -m -f '{{ .Version }}' github.com/aws-controllers-k8s/runtime 2>/dev/null || echo "$RUNTIME_MISSING_VERSION")
if [[ $ACK_RUNTIME_VERSION == $RUNTIME_MISSING_VERSION ]]; then
  echo "auto-generate-controllers.sh][ERROR] Unable to determine ACK runtime version from code-generator/go.mod file. Exiting"
  exit 1
else
  echo "auto-generate-controllers.sh][INFO] ACK runtime version for new controllers will be $ACK_RUNTIME_VERSION"
fi

DEFAULT_PR_SOURCE_BRANCH="ack-bot/runtime-$ACK_RUNTIME_VERSION"
PR_SOURCE_BRANCH=${PR_SOURCE_BRANCH:-$DEFAULT_PR_SOURCE_BRANCH}

# find all the directories whose name ends with 'controller'
pushd "$WORKSPACE_DIR" >/dev/null
  CONTROLLER_NAMES=$(find . -maxdepth 1 -name "*-controller" -type d | cut -d"/" -f2)
popd >/dev/null

for CONTROLLER_NAME in $CONTROLLER_NAMES; do
  SERVICE_NAME=$(echo "$CONTROLLER_NAME"| sed 's/-controller$//g')
  CONTROLLER_DIR="$WORKSPACE_DIR/$CONTROLLER_NAME"
  print_line_separation
  cd "$CODEGEN_DIR"

  echo "auto-generate-controllers.sh][INFO] ## Generating new controller for $SERVICE_NAME service ##"
  # if the go.mod file is missing in a service controller, skip auto-generation
  if [[ ! -f "$CONTROLLER_DIR/go.mod" ]]; then
    echo "auto-generate-controllers.sh][ERROR] Missing 'go.mod' file. Skipping $CONTROLLER_NAME"
    continue
  fi

  # Find the ACK runtime version in service controller 'go.mod' file
  pushd "$CONTROLLER_DIR" >/dev/null
    SERVICE_RUNTIME_VERSION=$(go list -m -f '{{ .Version }}' github.com/aws-controllers-k8s/runtime 2>/dev/null || echo "$RUNTIME_MISSING_VERSION")
  popd >/dev/null

  if [[ $SERVICE_RUNTIME_VERSION == $RUNTIME_MISSING_VERSION ]]; then
    echo "auto-generate-controllers.sh][ERROR] Unable to determine ACK runtime version from $CONTROLLER_NAME/go.mod file. Skipping $CONTROLLER_NAME"
    continue
  fi

  # If the current version is same as latest ACK runtime version, skip this controller.
  if [[ $SERVICE_RUNTIME_VERSION == $ACK_RUNTIME_VERSION ]]; then
    echo "auto-generate-controllers.sh][INFO] $CONTROLLER_NAME already has the latest ACK runtime version $ACK_RUNTIME_VERSION. Skipping $CONTROLLER_NAME"
    continue
  fi

  echo "auto-generate-controllers.sh][INFO] ACK runtime version for new controller will be $ACK_RUNTIME_VERSION. Current version is $SERVICE_RUNTIME_VERSION"

  echo -n "auto-generate-controllers.sh][INFO] Ensuring that GitHub label $GITHUB_LABEL exists for $GITHUB_ORG/$CONTROLLER_NAME ... "
  if ! gh api repos/"$GITHUB_ORG"/"$CONTROLLER_NAME"/labels/"$GITHUB_LABEL" --silent >/dev/null; then
    echo ""
    echo "auto-generate-controllers.sh][INFO] Could not find label $GITHUB_LABEL in repo $GITHUB_ORG/$CONTROLLER_NAME"
    echo -n "Creating new GitHub label $GITHUB_LABEL ... "
    if ! gh api -X POST repos/"$GITHUB_ORG"/"$CONTROLLER_NAME"/labels -f name="$GITHUB_LABEL" -f color="$GITHUB_LABEL_COLOR" >/dev/null; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Failed to create label $GITHUB_LABEL. Skipping $CONTROLLER_NAME"
      continue
    else
      echo "ok"
    fi
  else
    echo "ok"
  fi

  pushd "$CONTROLLER_DIR" >/dev/null
    echo "auto-generate-controllers.sh][INFO] Finding new release version for $CONTROLLER_NAME"
    # Find the latest tag on repository and only increment patch version
    LATEST_TAG=$(git describe --abbrev=0 --tags 2>/dev/null || echo "$MISSING_GITHUB_TAG")
    if [[ $LATEST_TAG == $MISSING_GITHUB_TAG ]]; then
      echo "auto-generate-controllers.sh][INFO] Unable to find latest tag for $CONTROLLER_NAME"
      unset RELEASE_VERSION
    else
      export RELEASE_VERSION=$(echo "$LATEST_TAG" | awk -F. -v OFS=. '{$NF++;print}')
      echo "auto-generate-controllers.sh][INFO] Using $RELEASE_VERSION as new release version. Previous version: $LATEST_TAG"
    fi
  popd >/dev/null

  echo "auto-generate-controllers.sh][INFO] Generating new controller code using command 'make build-controller'"
  export SERVICE=$SERVICE_NAME
  MAKE_BUILD_OUTPUT_FILE=/tmp/"$SERVICE_NAME"_make_build_output
  MAKE_BUILD_ERROR_FILE=/tmp/"$SERVICE_NAME"_make_build_error
  if ! make build-controller > "$MAKE_BUILD_OUTPUT_FILE" 2>"$MAKE_BUILD_ERROR_FILE"; then
    cat "$MAKE_BUILD_ERROR_FILE"

    echo "auto-generate-controllers.sh][ERROR] Failure while executing 'make build-controller' command. Creating/Updating GitHub issue"
    ISSUE_TITLE="Errors while generating \`$CONTROLLER_NAME\` for ACK runtime \`$ACK_RUNTIME_VERSION\`"

    echo -n "auto-generate-controllers.sh][INFO] Querying already open GitHub issue ... "
    ISSUE_NUMBER=$(gh issue list -R "$GITHUB_ORG/$GITHUB_ISSUE_REPO" -L 1 -s open --json number -S "$ISSUE_TITLE" --jq '.[0].number' -A @me -l "$GITHUB_LABEL")
    if [[ $? -ne 0 ]]; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Unable to query open github issue. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # Capture 'make build-controller' command output & error, then persist
    # in '$GITHUB_ISSUE_BODY_FILE'
    MAKE_BUILD_OUTPUT=$(cat "$MAKE_BUILD_OUTPUT_FILE")
    MAKE_BUILD_ERROR_OUTPUT=$(cat "$MAKE_BUILD_ERROR_FILE")
    GITHUB_ISSUE_BODY_TEMPLATE_FILE="$THIS_DIR/gh_issue_body_template.txt"
    GITHUB_ISSUE_BODY_FILE=/tmp/"SERVICE_NAME"_gh_issue_body
    eval "echo \"$(cat "$GITHUB_ISSUE_BODY_TEMPLATE_FILE")\"" > $GITHUB_ISSUE_BODY_FILE

    # If there is an already existing issue with same title as '$ISSUE_TITLE',
    # update the body of existing issue with latest command output.
    # In case no such issue exist, create a new GitHub issue.
    # Skip PR generation in both cases and continue to next service controller.
    if [[ -z $ISSUE_NUMBER ]]; then
      echo -n "auto-generate-controllers.sh][INFO] No open issues exist. Creating a new GitHub issue inside $GITHUB_ORG/$GITHUB_ISSUE_REPO ... "
      if ! gh issue create -R "$GITHUB_ORG/$GITHUB_ISSUE_REPO" -t "$ISSUE_TITLE" -F "$GITHUB_ISSUE_BODY_FILE" -l "$GITHUB_LABEL" >/dev/null ; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Unable to create GitHub issue for reporting failure. Skipping $CONTROLLER_NAME"
        continue
      fi
      echo "ok"
      continue
    else
      echo -n "auto-generate-controllers.sh][INFO] Updating error output in the body of existing issue#$ISSUE_NUMBER inside $GITHUB_ORG/$GITHUB_ISSUE_REPO ... "
      if ! gh issue edit "$ISSUE_NUMBER" -R "$GITHUB_ORG/$GITHUB_ISSUE_REPO" -F "$GITHUB_ISSUE_BODY_FILE" >/dev/null; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Unable to edit GitHub issue$ISSUE_NUMBER with latest 'make build-controller' error. Skipping $CONTROLLER_NAME"
        continue
      fi
      echo "ok"
      continue
    fi
    # Skip creating PR for this service controller after updating GitHub issue.
    continue
  fi

  # Since there are no failures, print make build output in prowjob logs
  cat "$MAKE_BUILD_OUTPUT_FILE"
  pushd "$CONTROLLER_DIR" >/dev/null
    # After successful 'make build-controller', update go.mod file
    echo -n "auto-generate-controllers.sh][INFO] Updating 'go.mod' file in $CONTROLLER_NAME ... "
    if ! sed -i "s|aws-controllers-k8s/runtime $SERVICE_RUNTIME_VERSION|aws-controllers-k8s/runtime $ACK_RUNTIME_VERSION|" go.mod >/dev/null; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Unable to update go.mod file with latest runtime version. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # perform 'go mod tidy' to remove old ACK runtime dependency
    echo -n "auto-generate-controllers.sh][INFO] Executing 'go mod tidy' to cleanup redundant dependencies for $CONTROLLER_NAME ... "
    if ! go mod tidy >/dev/null; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Unable to execute 'go mod tidy'. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # add git remote
    echo -n "auto-generate-controllers.sh][INFO] Adding git remote ... "
    if ! git remote add origin "https://github.com/$GITHUB_ORG/$CONTROLLER_NAME.git" >/dev/null; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Unable to add git remote. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # Add all the files & create a GitHub commit
    git add .
    COMMIT_MSG="Update ACK runtime to \`$ACK_RUNTIME_VERSION\`"
    echo -n "auto-generate-controllers.sh][INFO] Adding commit with message: '$COMMIT_MSG' ... "
    if ! git commit -m "$COMMIT_MSG" >/dev/null; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Failed to add commit message for $CONTROLLER_NAME repository. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # Force push the new changes into '$PR_SOURCE_BRANCH'
    echo -n "auto-generate-controllers.sh][INFO] Pushing changes to branch '$PR_SOURCE_BRANCH' ... "
    if ! git push --force "https://$GITHUB_TOKEN@github.com/$GITHUB_ORG/$CONTROLLER_NAME.git" "$LOCAL_GIT_BRANCH:$PR_SOURCE_BRANCH" >/dev/null 2>&1; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Failed to push the latest changes into remote repository. Skipping $CONTROLLER_NAME"
      continue
    fi
    echo "ok"

    # If a PR exists from '$PR_SOURCE_BRANCH' to '$PR_TARGET_BRANCH' then
    # update the PR body with latest successful command output.
    # In case no such PR exists, create a new PR.
    echo -n "auto-generate-controllers.sh][INFO] Finding existing open pull requests ... "
    PR_NUMBER=$(gh pr list -R "$GITHUB_ORG/$CONTROLLER_NAME" -A @me -L 1 -s open --json number -S "$COMMIT_MSG" --jq '.[0].number' -l "$GITHUB_LABEL")
    if [[ $? -ne 0 ]]; then
      echo ""
      echo "auto-generate-controllers.sh][ERROR] Failed to query for an existing pull request for $GITHUB_ORG/$CONTROLLER_NAME , from $PR_SOURCE_BRANCH -> $PR_TARGET_BRANCH branch"
    else
      echo "ok"
    fi

    # Capture 'make build-controller' command output, then persist
    # in '$GITHUB_PR_BODY_FILE'
    MAKE_BUILD_OUTPUT=$(cat "$MAKE_BUILD_OUTPUT_FILE")
    PR_BODY_TEMPLATE_FILE_NAME=$([[ -z "$RELEASE_VERSION" ]] && echo "gh_pr_body_template.txt" || echo "gh_pr_body_new_release_template.txt")
    GITHUB_PR_BODY_TEMPLATE_FILE="$THIS_DIR/$PR_BODY_TEMPLATE_FILE_NAME"
    GITHUB_PR_BODY_FILE=/tmp/"SERVICE_NAME"_gh_pr_body
    eval "echo \"$(cat "$GITHUB_PR_BODY_TEMPLATE_FILE")\"" > $GITHUB_PR_BODY_FILE

    if [[ -z $PR_NUMBER ]]; then
      echo -n "auto-generate-controllers.sh][INFO] No Existing PRs found. Creating a new pull request for $GITHUB_ORG/$CONTROLLER_NAME , from $PR_SOURCE_BRANCH -> $PR_TARGET_BRANCH branch ... "
      if ! gh pr create -R "$GITHUB_ORG/$CONTROLLER_NAME" -t "$COMMIT_MSG" -F "$GITHUB_PR_BODY_FILE" -H "$PR_SOURCE_BRANCH" -B "$PR_TARGET_BRANCH" -l "$GITHUB_LABEL" >/dev/null ; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Failed to create pull request. Skipping $CONTROLLER_NAME"
        continue
      fi
      echo "ok"
    else
      echo "auto-generate-controllers.sh][INFO] PR#$PR_NUMBER already exists for $GITHUB_ORG/$CONTROLLER_NAME , from $PR_SOURCE_BRANCH -> $PR_TARGET_BRANCH branch"
      echo -n "auto-generate-controllers.sh][INFO] Updating PR body with latest 'make build-controller' output..."
      if ! gh pr edit "$PR_NUMBER" -R "$GITHUB_ORG/$CONTROLLER_NAME" -F "$GITHUB_PR_BODY_FILE" >/dev/null ; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Failed to update pull request"
        continue
      fi
      echo "ok"
    fi
    echo "auto-generate-controllers.sh][INFO] Done :) "
    # PRs created from this script trigger the presubmit prowjobs.
    # To control the number of presubmit prowjobs that will run in parallel,
    # adding sleep of 2 minutes. This will help distribute the load on prow
    # cluster.
    echo "auto-generate-controllers.sh][INFO] Sleeping for 2 minutes before generating next service controller."
    sleep 120
  popd >/dev/null
done

print_line_separation
