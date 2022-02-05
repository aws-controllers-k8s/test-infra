#!/usr/bin/env bash

set -eo pipefail

USAGE="
Usage:
  $(basename "$0") <AWS_SERVICE> <TEST_SEMVER>

Creates the prerequisite AWS resources then creates an EKS cluster and applies
the testing infrastructure for long-term soak testing.

Example: $(basename "$0") ecr v0.0.1

<AWS_SERVICE> should be an AWS Service name (ecr, sns, sqs, petstore, bookstore)
<TEST_SEMVER> should be the semver tag of the service controller that should be
  tested.

Environment variables:
  DEPLOY_REGION:        The AWS region where the cluster and resources will be 
                        deployed.
                        Default: us-west-2
  CLUSTER_NAME:         The name of the EKS cluster.
                        Default: The value in the cluster-config.yaml file
  SOAK_IMAGE_REPO_NAME: The name of the soak test ECR public repository.
                        Default: ack-\$AWS_SERVICE-soak
  OCI_BUILDER:          The binary used to build the OCI images.
                        Default: docker
"

if [ $# -ne 2 ]; then
    echo "Script requires two parameters" 1>&2
    echo "${USAGE}"
    exit 1
fi

# Bootstrapping configuration
AWS_SERVICE=$(echo "$1" | tr '[:upper:]' '[:lower:]')
CONTROLLER_TAG="v$(echo $2 | tr -d "v")"
DEPLOY_REGION=${DEPLOY_REGION:-"us-west-2"}

SOAK_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
ROOT_DIR="$SOAK_DIR/.."
TEST_INFRA_DIR="$ROOT_DIR/../test-infra"
CONTROLLER_DIR="$ROOT_DIR/../$AWS_SERVICE-controller"

## CLUSTER CONFIGURATION
# Path to the eksctl cluster config
CLUSTER_CONFIG_PATH="$SOAK_DIR/cluster-config.yaml"
# EKS cluster name if not specified in cluster config
DEFAULT_CLUSTER_NAME="ack-soak-test"
# Semver version of the controller under test. Ex: v0.0.2
CONTROLLER_CHART_URL="public.ecr.aws/aws-controllers-k8s/$AWS_SERVICE-chart"
# AWS Region for ACK service controller
CONTROLLER_AWS_REGION="us-west-2"
# AWS Account Id for ACK service controller
CONTROLLER_AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query "Account" --output text)
# Release name of controller helm chart
CONTROLLER_CHART_RELEASE_NAME="soak-test"
# Namespace for the controller and its resources
CONTROLLER_NAMESPACE="ack-system"
# K8s Service name for <aws-service-name>-controller
# K8s servcie name has following format inside helm chart
# "{{ .Chart.Name | trimSuffix "-chart" | trunc 44 }}-controller-metrics"
K8S_SERVICE_NAME_PREFIX=$(echo "$AWS_SERVICE" | cut -c -44)
K8S_SERVICE_NAME="$K8S_SERVICE_NAME_PREFIX-controller-metrics"

### PROMETHEUS, GRAFANA CONFIGURATION ###
# Local helm repository name for the prometheus repository
PROM_REPO_NAME="prometheus-community"
# Helm repository URL for the prometheus community charts
PROM_REPO_URL="https://prometheus-community.github.io/helm-charts"
# Release name of kube-prometheus helm chart
PROM_CHART_RELEASE_NAME="kube-prom"
# Namespace for the prometheus helm chart
PROM_NAMESPACE="prometheus"
# Local port to access Prometheus dashbaord
LOCAL_PROMETHEUS_PORT=9090
# Local port to access Prometheus dashbaord
LOCAL_GRAFANA_PORT=3000

### SOAK TEST RUNNER CONFIGURATION ###
# The binary used to build and push the container images
OCI_BUILDER=${OCI_BUILDER:-"docker"}
# The public ECR repository URI where your soak test runner image will be stored.
DEFAULT_SOAK_IMAGE_REPO_NAME="ack-$AWS_SERVICE-soak"
SOAK_IMAGE_REPO_NAME=${SOAK_IMAGE_REPO_NAME:-$DEFAULT_SOAK_IMAGE_REPO_NAME}
# Image tag for soak-test-runner image.
SOAK_IMAGE_TAG="v0.0.1"
# Platform for soak-test-runner image.
SOAK_IMAGE_PLATFORM="linux/amd64"
# Release name of soak-test-runner helm chart.
SOAK_RUNNER_CHART_RELEASE_NAME="soak-test-runner"

# Total test duration is calculated as sum of TEST_DURATION_MINUTES,
# TEST_DURATION_HOURS and TEST_DURATION_DAYS after converting them in
# minutes. Override following variables accordingly to set your soak test
# duration. Default value: 24 hrs.
TEST_DURATION_DAYS=1
TEST_DURATION_HOURS=0
TEST_DURATION_MINUTES=0
NET_SOAK_TEST_DURATION_MINUTES=$(($TEST_DURATION_MINUTES + $TEST_DURATION_HOURS*60 + $TEST_DURATION_DAYS*24*60))

export HELM_EXPERIMENTAL_OCI=1

AWS_CLI="aws --region $DEPLOY_REGION"
AWS_ECR_PUBLIC_CLI="aws --region us-east-1 ecr-public"

# Check and create the public ECR repository
ret=0
$AWS_ECR_PUBLIC_CLI describe-repositories --repository-name $SOAK_IMAGE_REPO_NAME > /dev/null 2>&1 || ret=$?
if [ $ret -eq 0 ]; then
    echo "ECR public repository already exists"
else
    echo -n "Creating ECR public repository ... "
    $AWS_ECR_PUBLIC_CLI create-repository --repository-name $SOAK_IMAGE_REPO_NAME
    echo "ok."
fi

# Check and create the EKS cluster
CLUSTER_NAME=$(yq eval -e ".metadata.name" $CLUSTER_CONFIG_PATH 2> /dev/null) || CLUSTER_NAME=$DEFAULT_CLUSTER_NAME

ret=0
$AWS_CLI eks describe-cluster --name $CLUSTER_NAME > /dev/null 2>&1 || ret=$?
if [ $ret -eq 0 ]; then
    echo "EKS cluster already exists"
else
    echo "Creating EKS cluster ... "

    eksctl create cluster -f $CLUSTER_CONFIG_PATH

    echo "ok."
fi

# Install the controller into the cluster
CONTROLLER_SERVICE_ACCOUNT_NAME=$(yq eval -e ".iam.serviceAccounts[0].metadata.name" $CLUSTER_CONFIG_PATH 2> /dev/null) || \
    { >&2 echo "IRSA is not included in this cluster config. IRSA is required for soak testing"; exit 1; }

ret=0
helm list -n $CONTROLLER_NAMESPACE 2> /dev/null | grep -q $CONTROLLER_CHART_RELEASE_NAME || ret=$?
if [ $ret -eq 0 ]; then
    echo "Controller Helm release ($CONTROLLER_CHART_RELEASE_NAME) already installed in cluster"
else
    echo -n "Installing controller chart ... "
    helm install --create-namespace -n $CONTROLLER_NAMESPACE \
        --set metrics.service.create="true" --set metrics.service.type="ClusterIP" \
        --set aws.region=$CONTROLLER_AWS_REGION --set serviceAccount.create="false" \
        --set serviceAccount.name="$CONTROLLER_SERVICE_ACCOUNT_NAME" \
        $CONTROLLER_CHART_RELEASE_NAME "$CONTROLLER_DIR/helm" 1> /dev/null
    echo "ok."
fi

# Install the prometheus helm repo
ret=0
helm repo list 2> /dev/null | grep -q $PROM_REPO_NAME || ret=$?
if [ $ret -ne 0 ]; then
    echo -n "Adding prometheus chart repository ... "
    helm repo add $PROM_REPO_NAME $PROM_REPO_URL 1> /dev/null
    echo "ok."
fi

# Install the prometheus chart
ret=0
helm list -n $PROM_NAMESPACE 2> /dev/null | grep -q $PROM_CHART_RELEASE_NAME || ret=$?
if [ $ret -eq 0 ]; then
    echo "Prometheus Helm release ($PROM_CHART_RELEASE_NAME) already installed in cluster"
else
    echo -n "Installing Prometheus chart ... "
    helm install --create-namespace -n $PROM_NAMESPACE \
        --set prometheus.prometheusSpec.additionalScrapeConfigs[0].job_name="ack-controller" \
        --set prometheus.prometheusSpec.additionalScrapeConfigs[0].static_configs[0].targets[0]="$AWS_SERVICE-controller-metrics.ack-system:8080" \
        $PROM_CHART_RELEASE_NAME prometheus-community/kube-prometheus-stack 1> /dev/null
    echo "ok."
fi

# Apply the grafana dashboard
kubectl apply -n $PROM_NAMESPACE -k github.com/aws-controllers-k8s/test-infra/soak/monitoring/grafana?ref=main

# Build and publish the soak test runner image
SOAK_IMAGE_REPO_URI="$($AWS_ECR_PUBLIC_CLI describe-repositories --repository-name $SOAK_IMAGE_REPO_NAME --output text --query "repositories[0].repositoryUri")" || \
    { >&2 echo "Could not get the soak test image repository URI"; exit 1; }

echo "Building soak test image ... "
$OCI_BUILDER build --platform $SOAK_IMAGE_PLATFORM -t $SOAK_IMAGE_REPO_URI:$SOAK_IMAGE_TAG \
    --build-arg AWS_SERVICE=$AWS_SERVICE --build-arg E2E_GIT_REF=$CONTROLLER_TAG "$TEST_INFRA_DIR/soak"
echo "ok."

echo -n "Pushing soak test image to ECR public ... "
$AWS_ECR_PUBLIC_CLI get-login-password | $OCI_BUILDER login --username AWS --password-stdin public.ecr.aws 1> /dev/null 2>&1
$OCI_BUILDER push $SOAK_IMAGE_REPO_URI:$SOAK_IMAGE_TAG 1> /dev/null
echo "ok."

# Install the soak test runner
ret=0
helm list -n $CONTROLLER_NAMESPACE 2> /dev/null | grep -q $SOAK_RUNNER_CHART_RELEASE_NAME || ret=$?
if [ $ret -eq 0 ]; then
    echo -n "Controller Helm release ($SOAK_RUNNER_CHART_RELEASE_NAME) already installed in cluster"
else
    echo -n "Installing soak test chart ... "
    helm upgrade --install --create-namespace -n $CONTROLLER_NAMESPACE \
        --set awsService=$AWS_SERVICE --set soak.imageRepo=$SOAK_IMAGE_REPO_URI \
        --set soak.imageTag=$SOAK_IMAGE_TAG --set soak.startTimeEpochSeconds=$(date +%s) \
        --set soak.durationMinutes=$NET_SOAK_TEST_DURATION_MINUTES \
        $SOAK_RUNNER_CHART_RELEASE_NAME "$SOAK_DIR/helm/ack-soak-test" 1> /dev/null
    echo "ok."
fi

# Final messages and links
echo "Your soak test cluster should now be operational and actively running tests"
echo "To port-forward your Grafana dashboard use the following command:"
echo "    kubectl port-forward -n $PROM_NAMESPACE service/$PROM_CHART_RELEASE_NAME-grafana $LOCAL_GRAFANA_PORT:80 --address='0.0.0.0' >/dev/null &"
echo "Then navigate to http://localhost:$LOCAL_GRAFANA_PORT/"
echo ""
echo "The default username is \"admin\" and the default password is \"prom-operator\""