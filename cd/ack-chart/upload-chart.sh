#!/usr/bin/env bash

# upload-chart.sh handles taking the newest version of the parent chart and
# uploading it into its respective ECR public repositories.

# Example usage:
# CONTAINER_BUILDER=docker ./upload-chart.sh

set -Eeo pipefail

USAGE="
Usage:
  $(basename "$0")

Environment variables:
  CONTAINER_BUILDER:    The name of a CLI tool used to build and push
                        containers.
                        Defaults to 'buildah'
  HELM_REGISTRY:        ECR public URL of the registry where the Helm chart will
                        be published. Defaults to
                        'public.ecr.aws/aws-controllers-k8s'
"

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
CD_DIR=$THIS_DIR/..
TEST_INFRA_DIR=$CD_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..

ACK_CHART_DIR=$WORKSPACE_DIR/ack-chart

TEST_INFRA_LIB_DIR="$TEST_INFRA_DIR/scripts/lib"

DEFAULT_CONTAINER_BUILDER="buildah"
CONTAINER_BUILDER=${CONTAINER_BUILDER:-$DEFAULT_CONTAINER_BUILDER}

DEFAULT_HELM_REGISTRY="public.ecr.aws/aws-controllers-k8s"
HELM_REGISTRY=${HELM_REGISTRY:-$DEFAULT_HELM_REGISTRY}

PACKAGE_DIR="$ACK_CHART_DIR/build/"
PARENT_CHART_CONFIG="$ACK_CHART_DIR/Chart.yaml"

source "$TEST_INFRA_LIB_DIR/common.sh"

_get_chart_version() {
    local chart_version
    chart_version="$(yq '.version' "$PARENT_CHART_CONFIG")"
    echo "$chart_version"
}

_get_chart_name() {
    local chart_name
    chart_name="$(yq '.name' "$PARENT_CHART_CONFIG")"
    echo "$chart_name"
}

_assume_publisher_role() {
    local ecr_publish_arn
    if ! ecr_publish_arn="$(aws ssm get-parameter --name /ack/prow/cd/public_ecr/publish_role --query Parameter.Value --output text 2>/dev/null)"; then
        echo "Could not find the IAM role to publish images to the public ECR repository"
        exit 1
    fi

    local assume_command
    assume_command=$(aws sts assume-role --role-arn "$ecr_publish_arn" --role-session-name 'publish-parent-chart' --duration-seconds 3600 | jq -r '.Credentials | "export AWS_ACCESS_KEY_ID=\(.AccessKeyId)\nexport AWS_SECRET_ACCESS_KEY=\(.SecretAccessKey)\nexport AWS_SESSION_TOKEN=\(.SessionToken)\n"')
    eval "$assume_command"

    local ecr_pw
    ecr_pw=$(aws ecr-public get-login-password --region us-east-1)
    echo "$ecr_pw" | $CONTAINER_BUILDER login -u AWS --password-stdin public.ecr.aws 2>/dev/null 1>&2
    echo "$ecr_pw" | HELM_EXPERIMENTAL_OCI=1 helm registry login --username AWS --password-stdin public.ecr.aws 2>/dev/null
}

_package_chart() {
    local __version=$1
    local __output_dir=$2

    echo "Packaging Helm chart package"
    helm package --dependency-update "$ACK_CHART_DIR" -d "$__output_dir" 2>/dev/null
}

_upload_chart() {
    local __name=$1
    local __version=$2
    local __output_dir=$3

    local chart_package
    chart_package="$__output_dir/$__name-$__version.tgz"
    helm push "$chart_package" "oci://$HELM_REGISTRY" 2>/dev/null
}

run() {
    local name
    local version
    name="$(_get_chart_name)"
    version="$(_get_chart_version)"
    echo "Detected chart $name@$version"

    _assume_publisher_role
    echo "Successfully assumed the publisher role"

    _package_chart "$version" "$PACKAGE_DIR"

    _upload_chart "$name" "$version" "$PACKAGE_DIR"

    exit 0
}

ensure_binaries() {
    check_is_installed "aws"
    check_is_installed "helm"
    check_is_installed "jq"
    check_is_installed "yq"
    check_is_installed "$CONTAINER_BUILDER"
}

ensure_binaries

# The purpose of the `return` subshell command in this script is to determine
# whether the script was sourced, or whether it is being executed directly.
# https://stackoverflow.com/a/28776166
(return 0 2>/dev/null) || run