#!/usr/bin/env node
import "source-map-support/register";
import { App } from "aws-cdk-lib";
import { TestCIStack } from "../lib/test-ci-stack";

const app = new App();
new TestCIStack(app, "TestCIStack", {
  /* If you don't specify 'env', this stack will be environment-agnostic.
   * Account/Region-dependent features and context lookups will not work,
   * but a single synthesized template can be deployed anywhere. */

  /* Uncomment the next line to specialize this stack for the AWS Account
   * and Region that are implied by the current CLI configuration. */
  env: {
    account: process.env.CDK_DEFAULT_ACCOUNT,
    region: process.env.CDK_DEFAULT_REGION,
  },

  /* Uncomment the next line if you know exactly what Account and Region you
   * want to deploy the stack to. */
  // env: { account: '123456789012', region: 'us-east-1' },
  terminationProtection: true,

  /* For more information, see https://docs.aws.amazon.com/cdk/latest/guide/environments.html */
  clusterConfig: {
    personalAccessToken:
      app.node.tryGetContext("pat") || process.env.GITHUB_PAT,
    appId: app.node.tryGetContext("app_id") || process.env.GITHUB_APP_ID,
    appClientId:
      app.node.tryGetContext("client_id") || process.env.GITHUB_APP_CLIENT_ID,
    appPrivateKey:
      app.node.tryGetContext("app_private_key") ||
      process.env.GITHUB_APP_PRIVATE_KEY,
    appWebhookSecret:
      app.node.tryGetContext("app_webhook_secret") ||
      process.env.GITHUB_APP_WEBHOOK_SECRET,
    hostedZoneId:
      app.node.tryGetContext("hosted_zone_id") || process.env.HOSTED_ZONE_ID,
  },
  logsBucketName:
    app.node.tryGetContext("logs_bucket") || process.env.LOGS_BUCKET,
  logsBucketImport:
    app.node.tryGetContext("logs_bucket_import") ||
    process.env.LOGS_BUCKET_IMPORT ||
    false,

    inventoryBucketName: app.node.tryGetContext("inventory_bucket") || process.env.INVENTORY_BUCKET,
    inventoryBucketRegion: app.node.tryGetContext("inventory_bucket_region") || process.env.INVENTORY_BUCKET_REGION,
});
