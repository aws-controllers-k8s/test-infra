#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0")

Executes the soak test for an ACK service controller release as part of prowjob
when a service controller repository is tagged with a semver format '^v\d+\.\d+\.\d+$'
See: https://github.com/aws-controllers-k8s/test-infra/prow/jobs/jobs.yaml for prowjob configuration.

Environment variables:
  REPO_NAME:                Name of the service controller repository. Ex: ecr-controller
                            This variable is injected into the pod by Prow.
  PULL_BASE_REF:            The value of tag on service controller repository that triggered the
                            postsubmit prowjob. The value will be in the format '^v\d+\.\d+\.\d+$'.
                            This variable is injected into the pod by Prow.
"

# find out the service name and semver tag from the prow environment variables.
AWS_SERVICE=$(echo "$REPO_NAME" | rev | cut -d"-" -f2- | rev | tr '[:upper:]' '[:lower:]')
VERSION=$PULL_BASE_REF

# Important directory references based on prowjob configuration.
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
SCRIPTS_DIR=$DIR
SOAK_PROW_DIR=$DIR/..
SOAK_DIR=$SOAK_PROW_DIR/..
SOAK_HELM_DIR=$SOAK_DIR/helm
TEST_INFRA_DIR=$SOAK_DIR/..
WORKSPACE_DIR=$TEST_INFRA_DIR/..
SERVICE_CONTROLLER_DIR="$WORKSPACE_DIR/$AWS_SERVICE-controller"

# Check all the dependencies are present in container.
source "$TEST_INFRA_DIR"/scripts/lib/common.sh
check_is_installed buildah
check_is_installed aws
check_is_installed helm
check_is_installed git
check_is_installed kubectl
check_is_installed yq
check_is_installed jq

# login to the image repository
perform_buildah_and_helm_login

# Assume the iam role used to create the EKS soak cluster
assume_soak_creds() {
  # unset previously assumed creds, and assume new creds using IRSA of postsubmit prowjob.
  unset AWS_ACCESS_KEY_ID && unset AWS_SECRET_ACCESS_KEY && unset AWS_SESSION_TOKEN
  local _ASSUME_COMMAND=$(aws sts assume-role --role-arn $ACK_ROLE_ARN --role-session-name 'ack-soak-test' --duration-seconds 3600 | jq -r '.Credentials | "export AWS_ACCESS_KEY_ID=\(.AccessKeyId)\nexport AWS_SECRET_ACCESS_KEY=\(.SecretAccessKey)\nexport AWS_SESSION_TOKEN=\(.SessionToken)\n"')
  eval $_ASSUME_COMMAND
  >&2 echo "soak-on-release.sh] [INFO] Assumed ACK_ROLE_ARN"
}

ASSUME_EXIT_VALUE=0
ACK_ROLE_ARN=$(aws ssm get-parameter --name /ack/prow/service_team_role/$AWS_SERVICE --query Parameter.Value --output text 2>/dev/null) || ASSUME_EXIT_VALUE=$?
if [ "$ASSUME_EXIT_VALUE" -ne 0 ]; then
  >&2 echo "soak-on-release.sh] [SETUP] Could not find service team role for $AWS_SERVICE"
  exit 1
fi
export ACK_ROLE_ARN
>&2 echo "soak-on-release.sh] [SETUP] exported ACK_ROLE_ARN"

ASSUME_EXIT_VALUE=0
IRSA_ARN=$(aws ssm get-parameter --name /ack/prow/soak/irsa/$AWS_SERVICE --query Parameter.Value --output text 2>/dev/null) || ASSUME_EXIT_VALUE=$?
if [ "$ASSUME_EXIT_VALUE" -ne 0 ]; then
  >&2 echo "soak-on-release.sh] [SETUP] Could not find irsa to run soak tests for $AWS_SERVICE"
  exit 1
fi
export IRSA_ARN
>&2 echo "soak-on-release.sh] [SETUP] exported IRSA_ARN"

ASSUME_COMMAND=$(aws sts assume-role --role-arn $ACK_ROLE_ARN --role-session-name 'ack-soak-test' --duration-seconds 3600 | jq -r '.Credentials | "export AWS_ACCESS_KEY_ID=\(.AccessKeyId)\nexport AWS_SECRET_ACCESS_KEY=\(.SecretAccessKey)\nexport AWS_SESSION_TOKEN=\(.SessionToken)\n"')
eval $ASSUME_COMMAND
>&2 echo "soak-on-release.sh] [SETUP] Assumed ACK_ROLE_ARN"

AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query "Account" --output text)
export AWS_ACCOUNT_ID
>&2 echo "soak-on-release.sh] [SETUP] Exported ACCOUNT_ID."

aws eks update-kubeconfig --name soak-test-cluster >/dev/null
>&2 echo "soak-on-release.sh] [INFO] Updated the kubeconfig to communicate with 'soak-test-cluster' eks cluster."

export HELM_EXPERIMENTAL_OCI=1
cd "$SERVICE_CONTROLLER_DIR"/helm

# Evaluation string for yq tool for updating helm chart's values.yaml file
export SERVICE_ACCOUNT_ANNOTATION_EVAL=".serviceAccount.annotations.\"eks.amazonaws.com/role-arn\" = \"$IRSA_ARN\""
export METRIC_SERVICE_CREATE_EVAL=".metrics.service.create = true"
export METRIC_SERVICE_TYPE_EVAL=".metrics.service.type = \"ClusterIP\""
export AWS_REGION_EVAL=".aws.region = \"us-west-2\""
export AWS_ACCOUNT_ID_EVAL=".aws.account_id = \"$AWS_ACCOUNT_ID\""

yq eval "$SERVICE_ACCOUNT_ANNOTATION_EVAL" -i values.yaml \
&& yq eval "$METRIC_SERVICE_CREATE_EVAL" -i values.yaml \
&& yq eval "$METRIC_SERVICE_TYPE_EVAL" -i values.yaml \
&& yq eval "$AWS_REGION_EVAL" -i values.yaml \
&& yq eval "$AWS_ACCOUNT_ID_EVAL" -i values.yaml

export CONTROLLER_CHART_RELEASE_NAME="soak-test"
chart_name=$(helm list -f '^soak-test$' -o json | jq -r '.[]|.name')
[[ -n $chart_name ]] && echo "Chart soak-test already exists. Uninstalling..." && helm uninstall $CONTROLLER_CHART_RELEASE_NAME
helm install $CONTROLLER_CHART_RELEASE_NAME . >/dev/null
>&2 echo "soak-on-release.sh] [INFO] Helm chart $CONTROLLER_CHART_RELEASE_NAME successfully installed."

# Build the soak test runner image
cd "$TEST_INFRA_DIR"/soak
SOAK_RUNNER_IMAGE="public.ecr.aws/aws-controllers-k8s/soak:$AWS_SERVICE"
buildah bud \
  -q \
  -t $SOAK_RUNNER_IMAGE \
  --build-arg AWS_SERVICE=$AWS_SERVICE \
  --build-arg E2E_GIT_REF=$VERSION \
  . >/dev/null

buildah push $SOAK_RUNNER_IMAGE >/dev/null
>&2 echo "soak-on-release.sh] [INFO] Successfully built and pushed soak runner image $SOAK_RUNNER_IMAGE"

# Check for already existing soak-test-runner helm chart
export SOAK_CHART_RELEASE_NAME="soak-test-runner"
chart_name=$(helm list -f '^soak-test-runner$' -o json | jq -r '.[]|.name')
[[ -n $chart_name ]] \
&& echo "soak-on-release.sh] [INFO] Chart soak-test-runner already exists. Uninstalling..." >&2 \
&& helm uninstall $SOAK_CHART_RELEASE_NAME >/dev/null

cd "$TEST_INFRA_DIR"/soak/helm/ack-soak-test
helm install $SOAK_CHART_RELEASE_NAME . \
    --set awsService=$AWS_SERVICE \
    --set soak.imageRepo="public.ecr.aws/aws-controllers-k8s/soak" \
    --set soak.imageTag=$AWS_SERVICE \
    --set soak.startTimeEpochSeconds=$(date +%s) \
    --set soak.durationMinutes=1440 >/dev/null

# Loop until the Job executing soak test does not complete. Check again with 30 minutes interval.
while kubectl get jobs/$AWS_SERVICE-soak-test -o=json | jq -r --exit-status '.status.completionTime'>/dev/null; [ $? -ne 0 ]
do
  >&2 echo "soak-on-release.sh] [INFO] Completion time is not present in the job status. Soak test is still running."
  >&2 echo "soak-on-release.sh] [INFO] Sleeping for 30 mins..."
  sleep 1800
  # refresh the aws credentials to communicate with eks soak cluster
  assume_soak_creds
done

echo "soak-on-release.sh] [INFO] Soak test is completed. Uninstalling controller and soak helm charts."
helm uninstall $SOAK_CHART_RELEASE_NAME >/dev/null
helm uninstall $CONTROLLER_CHART_RELEASE_NAME >/dev/null
echo "soak-on-release.sh] [INFO] Successfully finished soak prowjob for $AWS_SERVICE-controller release $VERSION"
