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
the CloudFormation stack and associated resources in a given AWS account.

You will also need to **manually** create and configure a GitHub app for Prow as [documented here](https://github.com/kubernetes-sigs/prow/blob/main/site/content/en/docs/getting-started-deploy.md#github-app).

Once the GitHub app is configured, you will need three data elements from the app's settings page to pass to the CDK deployment:

- The app ID
- The app's private RSA key (in PEM format)
- The app's configured webhook secret
  - **_NOTE_**: you can generate a valid value for this using `openssl rand -hex 20`

Use the following command to deploy the stack with the included requirements:
```bash
export GITHUB_APP_ID='<GitHub app ID>'
export GITHUB_APP_PRIVATE_KEY='<GitHub app private RSA key in PEM format>'
export GITHUB_APP_WEBHOOK_SECRET='<GitHub app webhook secret>'
export LOGS_BUCKET='<S3 bucket name for logs>' # Optional
export AWS_DEFAULT_REGION=us-west-2

cd <test-infra root>/infra
cdk bootstrap
cdk deploy
```

or, via command line arguments:
```bash
export AWS_DEFAULT_REGION=us-west-2

cd <test-infra root>/infra
cdk bootstrap
cdk deploy -c app_id="<GitHub app ID>" \
           -c app_private_key="<GitHub app private RSA key in PEM format>" \
           -c app_webhook_secret="<GitHub app webhook secret>" \
           -c logs_bucket="<S3 bucket name for logs>"
```

An example:
```bash
export AWS_DEFAULT_REGION=us-west-2
cd $GOPATH/src/github.com/aws-controllers-k8s/test-infra/infra
cdk bootstrap
cdk deploy -c app_id="123456" \
    -c app_private_key="$(cat ./github_app_cert.pem)" \
    -c app_webhook_secret="081d23f783c016e91950c92a4fe4f87bfe61ca8b" \
    -c logs_bucket="ack-prow-logs-1234567890"
```