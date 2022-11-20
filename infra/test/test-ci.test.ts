import { expect as expectCDK, matchTemplate, MatchStyle } from '@aws-cdk/assert';
import * as cdk from '@aws-cdk/core';
import * as TestCI from '../lib/test-ci-stack';

test('Empty Stack', () => {
    const app = new cdk.App();
    // WHEN
    const stack = new TestCI.TestCIStack(app, 'MyTestStack', {
      clusterConfig: {
        githubAppId: "12345",
        githubAppPrivateKey: "abc123",
        githubAppWebhookSecret: "def456"
      },
      logsBucketName: "my-log-bucket",
      pvreBucketName: undefined
    });
    // THEN
    expectCDK(stack).to(matchTemplate({
      "Resources": {}
    }, MatchStyle.EXACT))
});
