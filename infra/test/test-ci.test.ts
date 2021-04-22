import { expect as expectCDK, matchTemplate, MatchStyle } from '@aws-cdk/assert';
import * as cdk from '@aws-cdk/core';
import * as TestCI from '../lib/test-ci-stack';

test('Empty Stack', () => {
    const app = new cdk.App();
    // WHEN
    const stack = new TestCI.TestCIStack(app, 'MyTestStack', {
      clusterConfig: {
        botPersonalAccessToken: "abc123",
        webhookHMACToken: "def456",
        argoCDAdminPassword: "mypassword"
      }
    });
    // THEN
    expectCDK(stack).to(matchTemplate({
      "Resources": {}
    }, MatchStyle.EXACT))
});
