#!/usr/bin/env bash
ecr_publish_arn=""
if ! ecr_publish_arn="$(aws ssm get-parameter --name /ack/prow/cd/test-infra/publish-prow-images --query Parameter.Value --output text 2>/dev/null)"; then
    echo "Could not find the IAM role to publish images to the public ECR repository"
    exit 1
fi

# Assume role for permissions to push to ecr with kaniko
temp_creds=$(aws sts assume-role-with-web-identity \
    --role-arn $ecr_publish_arn \
    --role-session-name "WebIdentitySession" \
    --web-identity-token "$(cat $AWS_WEB_IDENTITY_TOKEN_FILE)" \
    --output json \
    --duration-seconds 3600 \
    | jq -r '.Credentials | "export AWS_ACCESS_KEY_ID=\(.AccessKeyId)\nexport AWS_SECRET_ACCESS_KEY=\(.SecretAccessKey)\nexport AWS_SESSION_TOKEN=\(.SessionToken)\n"')

eval "$temp_creds"


ack-build-tools build-prow-images \
    --images-config-path ./prow/jobs/images_config.yaml \
    --jobs-config-path ./prow/jobs/jobs_config.yaml \
    --jobs-templates-path ./prow/jobs/templates/ \
    --jobs-output-path ./prow/jobs/jobs.yaml \
    --prow-ecr-repository prow
