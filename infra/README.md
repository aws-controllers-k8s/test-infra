# ACK Test Infrastructure CDK

## Useful commands

 * `npm run build`   compile typescript to js
 * `npm run watch`   watch for changes and compile
 * `npm run test`    perform the jest unit tests
 * `cdk deploy`      deploy this stack to your default AWS account/region
 * `cdk diff`        compare deployed stack with current state
 * `cdk synth`       emits the synthesized CloudFormation template

## Deploying the CDK
To deploy the CDK stacks, you must have the appropriate permissions to create
the CloudFormation stack and associated resources in a given AWS account. You 
will also need:
- A personal access token for the [Prow bot account](https://github.com/kubernetes/test-infra/blob/master/prow/getting_started_deploy.md#github-bot-account)
- An HMAC token used to validate Github webhooks
  - This can be generated using `openssl rand -hex 20`

Use the following command to deploy the stack with the included requirements:
```bash
export BOT_PAT='<bot personal access token>'
export WEBHOOK_HMAC='<webhook HMAC>'
export LOGS_BUCKET='<S3 bucket name for logs>' # Optional
export AWS_DEFAULT_REGION=us-west-2
cdk bootstrap
cdk deploy
```
