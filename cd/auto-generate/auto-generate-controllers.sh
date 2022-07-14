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

GITHUB_ISSUE_ORG_REPO="$GITHUB_ORG/$GITHUB_ISSUE_REPO"

DEFAULT_GITHUB_LABEL="ack-bot-autogen"
GITHUB_LABEL=${GITHUB_LABEL:-$DEFAULT_GITHUB_LABEL}

DEFAULT_GITHUB_LABEL_COLOR="3C6110"
GITHUB_LABEL_COLOR=${GITHUB_LABEL_COLOR:-$DEFAULT_GITHUB_LABEL_COLOR}

RUNTIME_MISSING_VERSION="missing-runtime-dependency"
MISSING_GIT_TAG="missing-git-tag"

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

# Find the runtime semver from the code-generator repo
cd "$CODEGEN_DIR"
ACK_RUNTIME_VERSION=$(go list -m -f '{{ .Version }}' github.com/aws-controllers-k8s/runtime 2>/dev/null || echo "$RUNTIME_MISSING_VERSION")
if [[ $ACK_RUNTIME_VERSION == $RUNTIME_MISSING_VERSION ]]; then
  echo "auto-generate-controllers.sh][ERROR] Unable to determine ACK runtime version from code-generator/go.mod file. Exiting"
  exit 1
else
  echo "auto-generate-controllers.sh][INFO] ACK runtime version for new controllers will be $ACK_RUNTIME_VERSION"
fi

# Find the code-gen semver from the latest tag on the code-generator repo
ACK_CODE_GEN_VERSION=$(git describe --tags --always --dirty)

GO_VERSION_IN_GO_MOD=$(grep -E "^go [0-9]+\.[0-9]+$" go.mod | cut -d " " -f2)
if [[ -z $GO_VERSION_IN_GO_MOD ]]; then
  echo "auto-generate-controllers.sh][ERROR] Unable to determine go version from code-generator/go.mod file. Exiting"
  exit 1
else
  echo "auto-generate-controllers.sh][INFO] go version in code-generator/go.mod file is $GO_VERSION_IN_GO_MOD"
fi

DEFAULT_PR_SOURCE_BRANCH="ack-bot/rt-$ACK_RUNTIME_VERSION-codegen-$ACK_CODE_GEN_VERSION"
PR_SOURCE_BRANCH=${PR_SOURCE_BRANCH:-$DEFAULT_PR_SOURCE_BRANCH}

# find all the directories whose name ends with 'controller'
pushd "$WORKSPACE_DIR" >/dev/null
  CONTROLLER_NAMES=$(find . -maxdepth 1 -name "*-controller" -type d | cut -d"/" -f2)
popd >/dev/null

for CONTROLLER_NAME in $CONTROLLER_NAMES; do
  SERVICE_NAME=$(echo "$CONTROLLER_NAME"| sed 's/-controller$//g')
  CONTROLLER_DIR="$WORKSPACE_DIR/$CONTROLLER_NAME"
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

    if [[ $SERVICE_RUNTIME_VERSION == $RUNTIME_MISSING_VERSION ]]; then
      echo "auto-generate-controllers.sh][ERROR] Unable to determine ACK runtime version from $CONTROLLER_NAME/go.mod file. Skipping $CONTROLLER_NAME"
      continue
    fi

    # If the current runtime version is the same as latest ACK runtime version, skip over runtime updates.
    if [[ $SERVICE_RUNTIME_VERSION == $ACK_RUNTIME_VERSION ]]; then
      echo "auto-generate-controllers.sh][INFO] $CONTROLLER_NAME already has the latest ACK runtime version $ACK_RUNTIME_VERSION"
    else
      echo "auto-generate-controllers.sh][INFO] ACK runtime version for new controller will be $ACK_RUNTIME_VERSION. Current version is $SERVICE_RUNTIME_VERSION"
      echo -n "auto-generate-controllers.sh][INFO] Updating 'go.mod' file for $CONTROLLER_NAME with ACK runtime $ACK_RUNTIME_VERSION ... "
      if ! go get -u github.com/aws-controllers-k8s/runtime@"$ACK_RUNTIME_VERSION" >/dev/null; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Unable to update go.mod file with ACK runtime version $ACK_RUNTIME_VERSION"
        continue
      fi

      echo -n "auto-generate-controllers.sh][INFO] Updating 'go.mod' file for $CONTROLLER_NAME with go version $GO_VERSION_IN_GO_MOD ... "
      if ! go mod edit -go="$GO_VERSION_IN_GO_MOD" >/dev/null; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Unable to update go.mod file with go version $GO_VERSION_IN_GO_MOD"
        continue
      fi
      echo "ok"

      # go dependencies need to be updated otherwise 'make build-controller' command will fail
      echo -n "auto-generate-controllers.sh][INFO] Executing 'go mod download' for $CONTROLLER_NAME after 'go.mod' updates ... "
      if ! go mod download >/dev/null; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Unable to perform 'go mod download' for $CONTROLLER_NAME"
        continue
      fi
      echo "ok"

      echo -n "auto-generate-controllers.sh][INFO] Executing 'go mod tidy' for $CONTROLLER_NAME after 'go.mod' updates ... "
      if ! go mod tidy >/dev/null; then
        echo ""
        echo "auto-generate-controllers.sh][ERROR] Unable to perform 'go mod tidy' for $CONTROLLER_NAME"
        continue
      fi
      echo "ok"
    fi

    SERVICE_AVAILABLE_API_VERSION=$(yq e '.api_versions[] | select(.status == "available") | .api_version' metadata.yaml)
    SERVICE_CODE_GEN_VERSION=$(yq e '.ack_generate_info.version' apis/$SERVICE_AVAILABLE_API_VERSION/ack-generate-metadata.yaml)
    # If the current version was generated with the latest ACK code-gen binary version, skip over the controller entirely
    if [[ "$SERVICE_CODE_GEN_VERSION" == "$ACK_CODE_GEN_VERSION" ]]; then
      echo "auto-generate-controllers.sh][INFO] $CONTROLLER_NAME already has the latest ACK code-gen version $ACK_CODE_GEN_VERSION. Skipping ... "
      continue
    else
      echo "auto-generate-controllers.sh][INFO] ACK code-gen version for new controller will be $ACK_CODE_GEN_VERSION. Current version is $SERVICE_CODE_GEN_VERSION"
    fi
  popd >/dev/null

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
    LATEST_TAG=$(git describe --abbrev=0 --tags 2>/dev/null || echo "$MISSING_GIT_TAG")
    if [[ $LATEST_TAG == $MISSING_GIT_TAG ]]; then
      echo "auto-generate-controllers.sh][INFO] Unable to find latest git tag for $CONTROLLER_NAME"
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
    ISSUE_TITLE="Errors while generating \`$CONTROLLER_NAME\` for ACK runtime \`$ACK_RUNTIME_VERSION\`, code-generator \`$ACK_CODE_GEN_VERSION\`"

    # Capture 'make build-controller' command output & error, then persist
    # in '$GITHUB_ISSUE_BODY_FILE'
    MAKE_BUILD_OUTPUT=$(cat "$MAKE_BUILD_OUTPUT_FILE")
    MAKE_BUILD_ERROR_OUTPUT=$(cat "$MAKE_BUILD_ERROR_FILE")
    GITHUB_ISSUE_BODY_TEMPLATE_FILE="$THIS_DIR/gh_issue_body_template.txt"
    GITHUB_ISSUE_BODY_FILE=/tmp/"SERVICE_NAME"_gh_issue_body
    eval "echo \"$(cat "$GITHUB_ISSUE_BODY_TEMPLATE_FILE")\"" > $GITHUB_ISSUE_BODY_FILE

    open_gh_issue "$GITHUB_ISSUE_ORG_REPO" "$ISSUE_TITLE" "$GITHUB_ISSUE_BODY_FILE"
    # Skip creating PR for this service controller after updating GitHub issue.
    continue
  fi

  # Since there are no failures, print make build output in prowjob logs
  cat "$MAKE_BUILD_OUTPUT_FILE"
  pushd "$CONTROLLER_DIR" >/dev/null
    GITHUB_CONTROLLER_ORG_REPO="$GITHUB_ORG/$CONTROLLER_NAME"

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
    COMMIT_MSG="Update to ACK runtime \`$ACK_RUNTIME_VERSION\`, code-generator \`$ACK_CODE_GEN_VERSION\`"
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
    # fetch all remotes to bring changes locally
    git fetch --all >/dev/null
    # local branch name cannot be 'main' otherwise PR creation to target 'main' branch will fail
    # checkout new local branch from remote PR source
    git checkout -b runtime-"$ACK_RUNTIME_VERSION" origin/"$PR_SOURCE_BRANCH" >/dev/null
    # sync local branch with the origin, if there is a diff the gh pr command
    # prompts for user input
    git pull --rebase >/dev/null

    # Capture 'make build-controller' command output, then persist
    # in '$GITHUB_PR_BODY_FILE'
    MAKE_BUILD_OUTPUT=$(cat "$MAKE_BUILD_OUTPUT_FILE")
    PR_BODY_TEMPLATE_FILE_NAME=$([[ -z "$RELEASE_VERSION" ]] && echo "gh_pr_body_template.txt" || echo "gh_pr_body_new_release_template.txt")
    GITHUB_PR_BODY_TEMPLATE_FILE="$THIS_DIR/$PR_BODY_TEMPLATE_FILE_NAME"
    GITHUB_PR_BODY_FILE=/tmp/"$SERVICE_NAME"_gh_pr_body
    eval "echo \"$(cat "$GITHUB_PR_BODY_TEMPLATE_FILE")\"" > $GITHUB_PR_BODY_FILE

    open_pull_request "$GITHUB_CONTROLLER_ORG_REPO" "$COMMIT_MSG" "$GITHUB_PR_BODY_FILE"
    echo "auto-generate-controllers.sh][INFO] Done :) "
    # PRs created from this script trigger the presubmit prowjobs.
    # To control the number of presubmit prowjobs that will run in parallel,
    # adding sleep of 2 minutes. This will help distribute the load on prow
    # cluster.
    echo "auto-generate-controllers.sh][INFO] Sleeping for 2 minutes before generating next service controller."
    sleep 120
  popd >/dev/null
done
