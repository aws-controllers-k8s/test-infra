#!/usr/bin/env bash

# open_gh_issue first queries for already open issues with 'ISSUE_TITLE' in 'ORG_REPO'.
# If an issue is already present, it updates the body of issue with contents of
# 'ISSUE_BODY_FILE_PATH'. If no such issue is found, this function will create a
# new GitHub issue.
# Usage:
#
# open_gh_issue "ORG_REPO" "ISSUE_TITLE" "ISSUE_BODY_FILE_PATH"
#
# If 'GITHUB_LABEL' environment variable is present, this function will add those
# label for list and create issue arguments of gh cli.
open_gh_issue() {
  if [[ $# -ne 3 ]]; then
    echo "gh.sh][ERROR] open_gh_issue requires three arguments, 'ORG_REPO', 'ISSUE_TITLE' and 'ISSUE_BODY_FILE_PATH'. But $# arguments were passed"
    return 1
  fi

  local __org_repo=$1
  local __issue_title=$2
  local __issue_body_file_path=$3
  local __label_arg=""

  if [[ -n $GITHUB_LABEL ]]; then
    __label_arg="-l $GITHUB_LABEL"
  fi

  echo -n "gh.sh][INFO] Querying already open GitHub issue ... "
  local __issue_number=$(gh issue list -R "$__org_repo" -L 1 -s open --json number -S "$__issue_title" --jq '.[0].number' -A @me $__label_arg)
  if [[ $? -ne 0 ]]; then
    echo ""
    echo "gh.sh][ERROR] Unable to query open github issues."
  else
    echo "ok"
  fi

  if [[ -z $__issue_number ]]; then
    echo -n "gh.sh][INFO] No open issues exist. Creating a new GitHub issue inside $__org_repo ... "
    if ! gh issue create -R "$__org_repo" -t "$__issue_title" -F "$__issue_body_file_path" $__label_arg >/dev/null ; then
      echo ""
      echo "gh.sh][ERROR] Unable to create GitHub issue"
    else
      echo "ok"
    fi
  else
    echo -n "gh.sh][INFO] Updating the body of existing issue#$__issue_number inside $__org_repo ... "
    if ! gh issue edit "$__issue_number" -R "$__org_repo" -F "$__issue_body_file_path" >/dev/null; then
      echo ""
      echo "gh.sh][ERROR] Unable to edit GitHub issue$ISSUE_NUMBER"
    else
      echo "ok"
    fi
  fi
}

# open_pull_request first queries for already open PRs with title 'COMMIT_MSG'.
# If a PR is already present in 'ORG_REPO', it updates the body of PR with the
# contents of 'PR_BODY_FILE_PATH'. If no such PR is found, this function will
# create a new pull request.
# Usage:
#
# open_pull_request ORG_REPO COMMIT_MSG PR_BODY_FILE_PATH
#
# If 'GITHUB_LABEL' environment variable is present, this function will add those
# label for list and create pr arguments of gh cli.
#
# Environment variables PR_SOURCE_BRANCH and PR_TARGET_BRANCH can be used to set
# source and target GitHub branches for PR. Default value for both is 'main'
open_pull_request() {
  if [[ $# -ne 3 ]]; then
    echo "gh.sh][ERROR] open_pull_request requires three arguments, 'ORG_REPO', 'COMMIT_MSG' and 'PR_BODY_FILE_PATH'. But $# arguments were passed"
    return 1
  fi

  local __org_repo=$1
  local __commit_msg=$2
  local __pr_body_file_path=$3
  local __source_branch=${PR_SOURCE_BRANCH:-"main"}
  local __target_branch=${PR_TARGET_BRANCH:-"main"}
  local __label_arg=""
  if [[ -n $GITHUB_LABEL ]]; then
    __label_arg="-l $GITHUB_LABEL"
  fi

  echo -n "gh.sh][INFO] Finding existing open pull requests ... "
  local __pr_number=$(gh pr list -R "$__org_repo" -A @me -L 1 -s open --json number -S "$__commit_msg" --jq '.[0].number' $__label_arg)
  if [[ $? -ne 0 ]]; then
    echo ""
    echo "gh.sh][ERROR] Failed to query for an existing pull request for $__org_repo , from $__source_branch -> $__target_branch branch"
  else
    echo "ok"
  fi

  if [[ -z $__pr_number ]]; then
    echo -n "gh.sh][INFO] No Existing PRs found. Creating a new pull request for $__org_repo , from $__source_branch -> $__target_branch branch... "
    if ! gh pr create -R "$__org_repo" -t "$__commit_msg" -F "$__pr_body_file_path" -B "$__target_branch" $__label_arg >/dev/null ; then
      echo ""
      echo "gh.sh][ERROR] Failed to create pull request. Exiting... "
      return 1
    fi
    echo "ok"
  else
    echo "gh.sh][INFO] PR#$__pr_number already exists for $__org_repo , from $__source_branch -> $__target_branch branch"
    echo -n "gh.sh][INFO] Updating PR body... "
    if ! gh pr edit "$__pr_number" -R "$__org_repo" -F "$__pr_body_file_path" >/dev/null ; then
      echo ""
      echo "gh.sh][ERROR] Failed to update pull request"
      return 1
    fi
    echo "ok"
  fi
  return 0
}