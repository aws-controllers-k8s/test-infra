import { expect as expectCDK, matchTemplate, MatchStyle } from '@aws-cdk/assert';
import * as cdk from '@aws-cdk/core';
import * as TestCI from '../lib/test-ci-stack';

test('Empty Stack', () => {
    const app = new cdk.App();
    // WHEN
    const stack = new TestCI.TestCIStack(app, 'MyTestStack', {
      clusterConfig: {
        appId: "12345",
        appPrivateKey: "abc123",
        appWebhookSecret: "def456",
        appClientId: "1234567890",
        personalAccessToken: "987654321"
      },
      logsBucketName: "my-log-bucket",
      logsBucketImport: false,
      pvreBucketName: undefined
    });
    // THEN
    expectCDK(stack).to(matchTemplate({
      "Resources": {}
    }, MatchStyle.EXACT))
});
