import * as cdk from '@aws-cdk/core';
import { CICluster, CIClusterCompileTimeProps } from './ci-cluster';
import { LogBucket, LogBucketCompileProps } from './log-bucket';
import { ProwServiceAccounts } from './prow-service-accounts';

export const PROW_NAMESPACE = "prow";
export const PROW_JOB_NAMESPACE = "test-pods";
export const EXTERNAL_DNS_NAMESPACE = "external-dns";

export type TestCIStackProps = cdk.StackProps & LogBucketCompileProps & {
  clusterConfig: CIClusterCompileTimeProps
};

export class TestCIStack extends cdk.Stack {
  constructor(scope: cdk.Construct, id: string, props: TestCIStackProps) {
    super(scope, id, props);

    const logsBucket = new LogBucket(this, 'LogBucketConstruct', {
      ...props,
      account: this.account
    })

    const testCluster = new CICluster(this, 'CIClusterConstruct', {
      ...props.clusterConfig
    });

    const prowServiceAccounts = new ProwServiceAccounts(this, 'ProwServiceAccountsConstruct', {
      account: this.account,
      stackPartition: this.partition,
      region: this.region,

      prowCluster: testCluster.testCluster,
      tideStatusBucket: logsBucket.bucket,
      presubmitsBucket: logsBucket.bucket,
      postsubmitsBucket: logsBucket.bucket,
    });
    prowServiceAccounts.node.addDependency(testCluster);
  }
}
