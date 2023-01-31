#!/usr/bin/env node
import 'source-map-support/register';
import * as cdk from '@aws-cdk/core';
import { TestCIStack } from '../lib/test-ci-stack';

const app = new cdk.App();
new TestCIStack(app, 'TestCIStack', {
  /* If you don't specify 'env', this stack will be environment-agnostic.
   * Account/Region-dependent features and context lookups will not work,
   * but a single synthesized template can be deployed anywhere. */

  /* Uncomment the next line to specialize this stack for the AWS Account
   * and Region that are implied by the current CLI configuration. */
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION },

  /* Uncomment the next line if you know exactly what Account and Region you
   * want to deploy the stack to. */
  // env: { account: '123456789012', region: 'us-east-1' },
  terminationProtection: true,

  /* For more information, see https://docs.aws.amazon.com/cdk/latest/guide/environments.html */
  clusterConfig: {
    personalAccessToken: app.node.tryGetContext('pat') || process.env.GITHUB_PAT,
    appId: app.node.tryGetContext('app_id') || process.env.GITHUB_APP_ID,
    appClientId: app.node.tryGetContext('client_id') || process.env.GITHUB_APP_CLIENT_ID,
    appPrivateKey: app.node.tryGetContext('app_private_key') || process.env.GITHUB_APP_PRIVATE_KEY,
    appWebhookSecret: app.node.tryGetContext('app_webhook_secret') || process.env.GITHUB_APP_WEBHOOK_SECRET
  },
  logsBucketName: app.node.tryGetContext('logs_bucket') || process.env.LOGS_BUCKET,
  logsBucketImport: app.node.tryGetContext('logs_bucket_import') || process.env.LOGS_BUCKET_IMPORT || false,
  pvreBucketName: app.node.tryGetContext('pvre_bucket') || process.env.PVRE_BUCKET,
});
