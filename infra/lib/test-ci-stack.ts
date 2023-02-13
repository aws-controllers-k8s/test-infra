import { Construct } from "constructs";
import { CICluster, CIClusterCompileTimeProps } from "./ci-cluster";
import { LogBucket, LogBucketCompileProps } from "./log-bucket";
import { ProwServiceAccounts } from "./prow-service-accounts";
import { Stack, StackProps } from "aws-cdk-lib";

export const STACK_NAME = "TestCIStack";
export const PROW_NAMESPACE = "prow";
export const PROW_JOB_NAMESPACE = "test-pods";
export const FLUX_NAMESPACE = "flux-system";

export const CLUSTER_NAME = "TestInfraCluster";
export const CLUSTER_CONSTRUCT_NAME = "CIClusterConstruct";

export type TestCIStackProps = StackProps &
  LogBucketCompileProps & {
    clusterConfig: CIClusterCompileTimeProps;
  };

export class TestCIStack extends Stack {
  constructor(scope: Construct, id: string, props: TestCIStackProps) {
    super(scope, id, props);

    const logsBucket = new LogBucket(this, "LogBucketConstruct", {
      ...props,
      account: this.account,
    });

    const testCluster = new CICluster(this, CLUSTER_CONSTRUCT_NAME, {
      ...props.clusterConfig,
    });

    const prowServiceAccounts = new ProwServiceAccounts(
      this,
      "ProwServiceAccountsConstruct",
      {
        account: this.account,
        stackPartition: this.partition,
        region: this.region,

        prowCluster: testCluster.testCluster,
        namespaceManifests: testCluster.namespaceManifests,

        tideStatusBucket: logsBucket.bucket,
        presubmitsBucket: logsBucket.bucket,
        postsubmitsBucket: logsBucket.bucket,
        periodicsBucket: logsBucket.bucket,
      }
    );
    prowServiceAccounts.node.addDependency(testCluster);
  }
}
