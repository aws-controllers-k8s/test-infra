#!/usr/bin/env bash

buildah_login() {
  __pw=$(aws ecr-public get-login-password --region us-east-1)
  echo "$__pw" | buildah login -u AWS --password-stdin public.ecr.aws
}

ECR_PUBLISH_ARN=$(aws ssm get-parameter --name /ack/prow/cd/public_ecr/publish_role --query Parameter.Value --output text 2>/dev/null) || ASSUME_EXIT_VALUE=$?
if [ "$ASSUME_EXIT_VALUE" -ne 0 ]; then
  echo "build-prow-images.sh] [SETUP] Could not find the iam role to publish images to public ecr repository"
  exit 1
fi
export ECR_PUBLISH_ARN
echo "build-prow-images.sh] [SETUP] exported ECR_PUBLISH_ARN"

ASSUME_COMMAND=$(aws sts assume-role --role-arn $ECR_PUBLISH_ARN --role-session-name 'publish-images' --duration-seconds 3600 | jq -r '.Credentials | "export AWS_ACCESS_KEY_ID=\(.AccessKeyId)\nexport AWS_SECRET_ACCESS_KEY=\(.SecretAccessKey)\nexport AWS_SESSION_TOKEN=\(.SessionToken)\n"')
eval $ASSUME_COMMAND
echo "build-prow-images.sh] [SETUP] Assumed ECR_PUBLISH_ARN"

buildah_login

ack-build-tools build-prow-images \
  --images-config-path ./prow/jobs/images_config.yaml \
  --jobs-config-path ./prow/jobs/jobs_config.yaml \
  --jobs-templates-path ./prow/jobs/templates/ \
  --jobs-output-path ./prow/jobs/jobs.yaml \
  --prow-ecr-repository prow