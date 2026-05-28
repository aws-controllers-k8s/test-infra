#!/usr/bin/env bash

buildah_login() {
  __pw=$(aws ecr-public get-login-password --region us-east-1)
  echo "$__pw" | buildah login -u AWS --password-stdin public.ecr.aws
}

buildah_login

# Derive the ECR repository name from PROW_IMAGES_REPO_URI
# e.g. public.ecr.aws/x1y2z3/ack-test-infra-dev-prow-images -> ack-test-infra-dev-prow-images
PROW_ECR_REPO_NAME="${PROW_IMAGES_REPO_URI##*/}"
if [ -z "$PROW_ECR_REPO_NAME" ]; then
  echo "build-prow-images.sh] [ERROR] Could not derive repo name from PROW_IMAGES_REPO_URI=$PROW_IMAGES_REPO_URI"
  exit 1
fi
echo "build-prow-images.sh] [SETUP] Using ECR repo: $PROW_ECR_REPO_NAME"

# Resolve env vars in config files
envsubst < ./prow/jobs/images_config.yaml > /tmp/images_config.yaml
envsubst < ./prow/plugins/images_config.yaml > /tmp/plugins_images_config.yaml
envsubst < ./prow/agent-workflows/images_config.yaml > /tmp/agent_workflows_images_config.yaml

# Build Prow Jobs
BUILT_JOB_TAGS=$(ack-build-tools build-prow-images \
  --images-config-path /tmp/images_config.yaml \
  --jobs-config-path ./prow/jobs/jobs_config.yaml \
  --jobs-templates-path ./prow/jobs/templates/ \
  --jobs-output-path ./prow/jobs/jobs.yaml \
  --prow-ecr-repository "$PROW_ECR_REPO_NAME")

if [ $? -ne 0 ]; then
  echo "Error building prow jobs"
  exit 1
fi

# Build Prow Agent Workflows
BUILT_AGENT_WORKFLOW_TAGS=$(ack-build-tools build-prow-agent-workflow-images \
  --images-config-path /tmp/agent_workflows_images_config.yaml \
  --prow-ecr-repository "$PROW_ECR_REPO_NAME" \
  --agent-workflows-templates-path ./prow/agent-workflows/templates \
  --agent-workflows-output-path ./prow/agent-workflows/agent-workflows.yaml)

if [ $? -ne 0 ]; then
  echo "Error building prow agent workflows"
  exit 1
fi

# Build Prow Plugins
BUILT_PLUGIN_TAGS=$(ack-build-tools build-prow-plugin-images \
  --images-config-path /tmp/plugins_images_config.yaml \
  --prow-ecr-repository "$PROW_ECR_REPO_NAME")

if [ $? -ne 0 ]; then
  echo "Error building prow plugins"
  exit 1
fi

if [ -n "$BUILT_PLUGIN_TAGS" ]; then
  PLUGIN_SOURCE_FILES=$(ack-build-tools generate-prow-plugins \
  --images-config-path ./prow/plugins/images_config.yaml \
  --plugins-templates-path prow/plugins/templates \
  --plugins-output-path prow/plugins/deployments)
fi


# If we patched any images publish a PR with the changes.
if [ -n "$BUILT_JOB_TAGS" ] || [ -n "$BUILT_AGENT_WORKFLOW_TAGS" ] || [ -n "$BUILT_PLUGIN_TAGS" ]; then
  if [ -n "$BUILT_JOB_TAGS" ]; then
    PR_DESCRIPTION+="Built and pushed prow job images:"$'\n'"$BUILT_JOB_TAGS"$'\n\n'
    SOURCE_FILES+="prow/jobs/jobs.yaml:prow/jobs/jobs.yaml"
  fi

  if [ -n "$BUILT_AGENT_WORKFLOW_TAGS" ]; then
    PR_DESCRIPTION+="Built and pushed prow agent workflow images:"$'\n'"$BUILT_AGENT_WORKFLOW_TAGS"$'\n\n'
    SOURCE_FILES+="prow/agent-workflows/agent-workflows.yaml:prow/agent-workflows/agent-workflows.yaml,"
  fi

  if [ -n "$BUILT_PLUGIN_TAGS" ]; then
    PR_DESCRIPTION+="Built and pushed prow plugin images:"$'\n'"$BUILT_PLUGIN_TAGS"$'\n\n'
    SOURCE_FILES+=$PLUGIN_SOURCE_FILES
  fi

  ack-build-tools publish-pr \
  --subject "Patch Prow Image Versions" \
  --description "$PR_DESCRIPTION" \
  --commit-branch "ack-bot/built-and-pushed-images-$(date +%N)" \
  --base-branch "$TEST_INFRA_BRANCH" \
  --source-owner "$TEST_INFRA_ORG" \
  --source-repo "$REPO_NAME" \
  --source-files $SOURCE_FILES
fi