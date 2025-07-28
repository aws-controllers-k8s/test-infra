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
ack-build-tools build-prow-images \
  --images-config-path ./prow/jobs/images_config.yaml \
  --jobs-config-path ./prow/jobs/jobs_config.yaml \
  --jobs-templates-path ./prow/jobs/templates/ \
  --jobs-output-path ./prow/jobs/jobs.yaml \
  --prow-ecr-repository prow

# Build Prow Agent Workflows

# Build Prow Plugins
ack-build-tools build-prow-images \
  --images-config-path ./prow/plugins/images_config.yaml \
  --plugins-config-path ./prow/plugins/plugins_config.yaml \
  --plugins-output-path ./prow/plugins/plugins.yaml \
  --prow-ecr-repository prow
