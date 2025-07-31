#!/usr/bin/env bash

buildah_login() {
  __pw=$(aws ecr-public get-login-password --region us-east-1)
  echo "$__pw" | buildah login -u AWS --password-stdin public.ecr.aws
}

PROW_ECR_PUBLISH_ARN=""
if ! PROW_ECR_PUBLISH_ARN="$(aws ssm get-parameter --name /ack/prow/cd/test-infra/publish-prow-images --query Parameter.Value --output text 2>/dev/null)"; then
    echo "Could not find the IAM role to publish images to the public ECR repository"
    exit 1
fi
export PROW_ECR_PUBLISH_ARN
echo "build-prow-images.sh] [SETUP] exported PROW_ECR_PUBLISH_ARN"

ASSUME_COMMAND=$(aws sts assume-role-with-web-identity \
    --role-arn $PROW_ECR_PUBLISH_ARN \
    --role-session-name "WebIdentitySession" \
    --web-identity-token "$(cat $AWS_WEB_IDENTITY_TOKEN_FILE)" \
    --output json \
    --duration-seconds 3600 \
    | jq -r '.Credentials | "export AWS_ACCESS_KEY_ID=\(.AccessKeyId)\nexport AWS_SECRET_ACCESS_KEY=\(.SecretAccessKey)\nexport AWS_SESSION_TOKEN=\(.SessionToken)\n"')
eval $ASSUME_COMMAND
echo "build-prow-images.sh] [SETUP] Assumed PROW_ECR_PUBLISH_ARN"

buildah_login

# Build Prow Jobs
BUILT_JOB_TAGS=$(ack-build-tools build-prow-images \
  --images-config-path ./prow/jobs/images_config.yaml \
  --jobs-config-path ./prow/jobs/jobs_config.yaml \
  --jobs-templates-path ./prow/jobs/templates/ \
  --jobs-output-path ./prow/jobs/jobs.yaml \
  --prow-ecr-repository prow)

if [ $? -ne 0 ]; then
  echo "Error building prow jobs"
  exit 1
fi

# Build Prow Agent Workflows
BUILT_AGENT_WORKFLOW_TAGS=$(ack-build-tools build-prow-agent-workflow-images \
  --images-config-path ./prow/agent-workflows/images_config.yaml \
  --prow-ecr-repository prow \
  --agent-workflows-templates-path ./prow/agent-workflows/templates \
  --agent-workflows-output-path ./prow/agent-workflows/agent-workflows.yaml)

if [ $? -ne 0 ]; then
  echo "Error building prow agent workflows"
  exit 1
fi

# Build Prow Plugins
BUILT_PLUGIN_TAGS=$(ack-build-tools build-prow-plugin-images \
  --images-config-path ./prow/plugins/images_config.yaml \
  --prow-ecr-repository prow)

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
  --source-files $SOURCE_FILES
fi