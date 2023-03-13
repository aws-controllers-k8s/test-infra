#!/usr/bin/env bash

# update-chart.sh handles creating a new version of the parent chart, updating
# its dependencies and pushing the chart into the repository.

# Example usage:
# GITHUB_TOKEN=<pat> ./update-chart.sh

set -Eeo pipefail

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  COMMIT_TARGET_BRANCH: Name of the GitHub branch where the committed changes
                        will be pushed. Defaults to 'main'
  GITHUB_ORG:           Name of the GitHub organisation where committed changes
                        to the chart will be pushed.
                        Defaults to 'aws-controllers-k8s'
  GITHUB_REPO:          Name of the GitHub repository where committed changes to
                        the chart will be pushed.
                        Defaults to 'ack-chart'
  GITHUB_ACTOR:         Name of the GitHub account creating the issues & PR.
  GITHUB_DOMAIN:        Domain for GitHub. Defaults to 'github.com'
  GITHUB_EMAIL_PREFIX:  The 7 digit unique id for no-reply email of
                        '$GITHUB_ACTOR'
  GITHUB_TOKEN:         Personal Access Token for '$GITHUB_ACTOR'
  REPO_NAME:            The name of the repository that launched the ProwJob
                        running the current script. Prow will automatically
                        inject this variable.
"

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..

ACK_CHART_DIR=$WORKSPACE_DIR/ack-chart

TEST_INFRA_LIB_DIR="$TEST_INFRA_DIR/scripts/lib"

PARENT_CHART_CONFIG="$ACK_CHART_DIR/Chart.yaml"
PARENT_CHART_VALUES="$ACK_CHART_DIR/values.yaml"

GITHUB_USER_EMAIL="${GITHUB_ACTOR}@users.noreply.${GITHUB_DOMAIN:-"github.com"}"
if [ -n "${GITHUB_EMAIL_PREFIX}" ]; then
    GITHUB_USER_EMAIL="${GITHUB_EMAIL_PREFIX}+${GITHUB_USER_EMAIL}"
fi

LOCAL_GIT_BRANCH="main"

DEFAULT_GITHUB_ORG="aws-controllers-k8s"
GITHUB_ORG=${GITHUB_ORG:-$DEFAULT_GITHUB_ORG}

DEFAULT_GITHUB_REPO="ack-chart"
GITHUB_REPO=${GITHUB_REPO:-$DEFAULT_GITHUB_REPO}

DEFAULT_COMMIT_TARGET_BRANCH="main"
COMMIT_TARGET_BRANCH=${COMMIT_TARGET_BRANCH:-$DEFAULT_COMMIT_TARGET_BRANCH}

# DEPENDENCY_DIFF is an enumeration for storing how the chart depedencies have
# changed between the last and current versions of the parent chart. Storing the
# values as integers allows the script to compare values using mathematical
# operators.
DEPENDENCY_DIFF_PATCH="0"
DEPENDENCY_DIFF_MINOR="1"
DEPENDENCY_DIFF_MAJOR="2"

source "$TEST_INFRA_LIB_DIR/common.sh"

_upgrade_dependency_and_chart_versions() {
    local parent_chart_diff="$DEPENDENCY_DIFF_PATCH"

    pushd "$WORKSPACE_DIR" >/dev/null
        local controller_names
        controller_names=$(find . -maxdepth 1 -name "*-controller" -type d | cut -d"/" -f2)
    popd >/dev/null

    for controller_name in $controller_names; do
        local service_name
        service_name="${controller_name//-controller/}"
        local controller_dir="$WORKSPACE_DIR/$controller_name"

        local controller_chart="$controller_dir/helm/Chart.yaml"
        if [[ ! -f "$controller_chart" ]]; then
            >&2 echo "Skipping $controller_name - no Chart.yaml found"
            continue
        fi

        # Determine if the chart is in GA
        # shellcheck disable=SC2016
        chart_major_version="$(yq '.version | split(".") | .[0] | sub("v(\d+)", "$1")' "$controller_chart")"
        if [[ "$chart_major_version" == "0" ]]; then
        >&2 echo "Skipping $controller_name - no GA releases"
            continue
        fi

        chart_name="$(yq '.name' "$controller_chart")"
        chart_version="$(yq '.version' "$controller_chart")"

        local existing_version
        existing_version="$(_get_chart_dependency_version "$chart_name")"
        if [[ "$existing_version" == "" ]]; then
            echo "Adding $chart_name as a new dependency $chart_version"

            _add_chart_dependency "$chart_name" "$chart_version" "$service_name"
            _add_chart_values_section "$service_name"

            # For new chart dependencies, upgrade the minor version
            parent_chart_diff="$DEPENDENCY_DIFF_MINOR"

        elif [[ "$existing_version" != "$chart_version" ]]; then
            echo "Upgrading $chart_name from $existing_version to $chart_version"

            _upgrade_chart_dependency "$chart_name" "$chart_version" "$service_name"

            # Determine the difference in semver
            local semver_diff
            semver_diff="$(_get_semver_diff "$existing_version" "$chart_version")"
            if [[ "$semver_diff" -gt "$parent_chart_diff" ]]; then
                parent_chart_diff="$semver_diff"
            fi
        fi
    done

    # Sort dependencies and values by name
    yq --inplace '.dependencies |= sort_by(.name)' "$PARENT_CHART_CONFIG"
    yq --inplace '. |= sort_keys(.)' "$PARENT_CHART_VALUES" 
    
    local current_chart_version
    local new_chart_version
    current_chart_version="$(yq '.version' "$PARENT_CHART_CONFIG")"
    new_chart_version="$(_increment_chart_version "$current_chart_version" "$parent_chart_diff")"

    echo "Updating ack-chart from version $current_chart_version to $new_chart_version"

    VERSION="$new_chart_version" yq --inplace '.version = env(VERSION)' "$PARENT_CHART_CONFIG"
}

_get_chart_dependency_version() {
    local __dependency_name=$1

    local dependency_version
    dependency_version="$(NAME=$__dependency_name yq '.dependencies[] | select(.name == env(NAME)) | .version' "$PARENT_CHART_CONFIG")"
    echo "$dependency_version"
}

_get_semver_diff() {
    local __current_version=$1
    local __new_version=$2

    local trimmed_current
    local trimmed_new

    # This assumes the version takes the form of `vX.Y.Z`
    trimmed_current="$(echo "$__current_version" | cut -c2-)"
    trimmed_new="$(echo "$__new_version" | cut -c2-)"

    if [[ "$(echo "$trimmed_current" | cut -d"." -f1)" != "$(echo "$trimmed_new" | cut -d"." -f1)" ]]; then
        echo "$DEPENDENCY_DIFF_MAJOR"
    elif [[ "$(echo "$trimmed_current" | cut -d"." -f2)" != "$(echo "$trimmed_new" | cut -d"." -f2)" ]]; then
        echo "$DEPENDENCY_DIFF_MINOR"
    elif [[ "$(echo "$trimmed_current" | cut -d"." -f3)" != "$(echo "$trimmed_new" | cut -d"." -f3)" ]]; then
        echo "$DEPENDENCY_DIFF_PATCH"
    else
        echo ""
    fi
}

_increment_chart_version() {
    local __current_version=$1
    local __patch_diff=$2

    local current_major
    local current_minor
    local current_patch

    current_major="$(echo "$__current_version" | cut -d"." -f1)"
    current_minor="$(echo "$__current_version" | cut -d"." -f2)"
    current_patch="$(echo "$__current_version" | cut -d"." -f3)"

    # Increment the largest diff number and reset any smaller parts
    if [[ "$__patch_diff" == "$DEPENDENCY_DIFF_PATCH" ]]; then
        current_patch="$((current_patch + 1))"
    elif [[ "$__patch_diff" == "$DEPENDENCY_DIFF_MINOR" ]]; then
        current_patch="0"
        current_minor="$((current_minor + 1))"
    elif [[ "$__patch_diff" == "$DEPENDENCY_DIFF_MAJOR" ]]; then
        current_patch="0"
        current_minor="0"
        current_major="$((current_major + 1))"
    fi

    echo "${current_major}.${current_minor}.${current_patch}"
}

_add_chart_dependency() {
    local __dependency_name=$1
    local __dependency_version=$2
    local __service_name=$3

    NAME=$__dependency_name SERVICE_NAME=$__service_name VERSION=$__dependency_version \
    yq --inplace '.dependencies += {
        "name": env(NAME),
        "alias": env(SERVICE_NAME),
        "version": env(VERSION),
        "repository": "oci://public.ecr.aws/aws-controllers-k8s",
        "condition": (env(SERVICE_NAME) + ".enabled")
    }' "$PARENT_CHART_CONFIG"
}

_upgrade_chart_dependency() {
    local __dependency_name=$1
    local __dependency_version=$2
    local __service_name=$3

    NAME=$__dependency_name yq --inplace 'del(.dependencies[] | select(.name == env(NAME)))' "$PARENT_CHART_CONFIG"

    _add_chart_dependency "$__dependency_name" "$__dependency_version" "$__service_name"
}

_rebuild_chart_dependencies() {
    local ecr_pw
    ecr_pw=$(aws ecr-public get-login-password --region us-east-1)
    echo "$ecr_pw" | helm registry login -u AWS --password-stdin public.ecr.aws

    helm dependency update "$ACK_CHART_DIR"
}

_add_chart_values_section() {
    local __service_name=$1

    SERVICE_NAME=$__service_name yq --inplace '.[env(SERVICE_NAME)+"-chart"] += {
        "enabled": false
    }' "$PARENT_CHART_VALUES"
}

_commit_chart_changes() { 
    git config --global user.name "${GITHUB_ACTOR}" >/dev/null
    git config --global user.email "${GITHUB_USER_EMAIL}" >/dev/null

    pushd "$ACK_CHART_DIR" >/dev/null
        echo "Adding git remote ... "
        git remote add upstream "https://$GITHUB_TOKEN@github.com/$GITHUB_ORG/$GITHUB_REPO.git" >/dev/null || :

        git fetch --all >/dev/null
        git checkout -b "$COMMIT_TARGET_BRANCH" "upstream/$COMMIT_TARGET_BRANCH" >/dev/null || :

        # Add all the files & create a GitHub commit
        git add .
        COMMIT_MSG="Updating chart dependencies" # TODO: Add a more descriptive commit message using the version diffs
        echo "Adding commit with message: '$COMMIT_MSG' ... "
        git commit -m "$COMMIT_MSG" >/dev/null

        git pull --rebase upstream "$COMMIT_TARGET_BRANCH"

        echo "Pushing changes to branch '$COMMIT_TARGET_BRANCH' ... "
        git push upstream "$LOCAL_GIT_BRANCH:$COMMIT_TARGET_BRANCH" 2>&1

        local new_chart_version
        new_chart_version="$(yq '.version' "$PARENT_CHART_CONFIG")"

        echo "Pushing tag to upstream ..."
        git tag "$new_chart_version"
        git push upstream "$new_chart_version"
    popd >/dev/null
}

_poll_for_upgraded_chart() {
    local triggering_chart_dir
    triggering_chart_dir="$WORKSPACE_DIR/$REPO_NAME"

    local newest_chart_version
    newest_chart_version="$(yq '.version' "$triggering_chart_dir/helm/Chart.yaml")"

    echo "Fetching ECR public bearer token"

    local tag_list_authentication_token
    tag_list_authentication_token=$(curl -ss -k https://public.ecr.aws/token/ | jq -r '.token')

    local chart_name
    chart_name="aws-controllers-k8s/${REPO_NAME//-controller/-chart}"

    echo "Waiting until $chart_name@$newest_chart_version becomes available on ECR public"
    # Use SECONDS as timeout counter
    until curl -ss -k -H "Authorization: Bearer $tag_list_authentication_token" \
        "https://public.ecr.aws/v2/$chart_name/tags/list" \
        | jq --exit-status --arg CHART_VERSION "$newest_chart_version" \
        'any(.tags[]; . == $CHART_VERSION)' &> /dev/null; do
        
        if (( SECONDS > 60*5 )); then
            >&2 echo "Timed out waiting for chart to become available"
            exit 1
        fi

        sleep 5
    done
}

run() {
    # Poll until the triggering repo has uploaded the latest Helm chart
    _poll_for_upgraded_chart

    # Upgrade all the version numbers
    _upgrade_dependency_and_chart_versions

    # Rebuild the chart
    _rebuild_chart_dependencies

    # Create a new commit and push the changes
    _commit_chart_changes

    exit 0
}

ensure_binaries() {
    check_is_installed "aws"
    check_is_installed "helm"
    check_is_installed "jq"
    check_is_installed "yq"
}

ensure_binaries

# The purpose of the `return` subshell command in this script is to determine
# whether the script was sourced, or whether it is being executed directly.
# https://stackoverflow.com/a/28776166
(return 0 2>/dev/null) || run