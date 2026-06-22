#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0") <AWS_SERVICE>

Tears down all soak test resources for the given AWS service controller.
This removes the EKS cluster, ECR repository, and all Helm releases
created by bootstrap.sh.

Example: $(basename "$0") ecr

Environment variables:
  DEPLOY_REGION:    The AWS region where resources were deployed.
                    Default: us-west-2
  CLUSTER_NAME:     The name of the EKS cluster to delete.
                    Default: ack-soak-<AWS_SERVICE>
"

if [ $# -ne 1 ]; then
    echo "Script requires one parameter: the AWS service name" 1>&2
    echo "${USAGE}"
    exit 1
fi

AWS_SERVICE=$(echo "$1" | tr '[:upper:]' '[:lower:]')
DEPLOY_REGION=${DEPLOY_REGION:-"us-west-2"}
CLUSTER_NAME=${CLUSTER_NAME:-"ack-soak-${AWS_SERVICE}"}
SOAK_IMAGE_REPO_NAME="ack-${AWS_SERVICE}-soak"

AWS_CLI="aws --region $DEPLOY_REGION"
AWS_ECR_PUBLIC_CLI="aws --region us-east-1 ecr-public"

echo "========================================"
echo " Soak Test Teardown: $AWS_SERVICE"
echo " Cluster:  $CLUSTER_NAME"
echo " Region:   $DEPLOY_REGION"
echo "========================================"
echo ""

# Delete the EKS cluster (this removes nodegroups, IAM roles, CloudFormation stacks, etc.)
if $AWS_CLI eks describe-cluster --name $CLUSTER_NAME > /dev/null 2>&1; then
    echo "Deleting EKS cluster ($CLUSTER_NAME) ... "
    eksctl delete cluster --name $CLUSTER_NAME --region $DEPLOY_REGION --wait
    echo "ok."
else
    echo "EKS cluster $CLUSTER_NAME does not exist. Skipping cluster deletion."
fi

# Delete the ECR public repository
echo -n "Deleting ECR public repository ($SOAK_IMAGE_REPO_NAME) ... "
$AWS_ECR_PUBLIC_CLI delete-repository --repository-name $SOAK_IMAGE_REPO_NAME --force > /dev/null 2>&1 && echo "ok." || echo "not found."

echo ""
echo "========================================"
echo " Teardown complete: $AWS_SERVICE"
echo "========================================"
