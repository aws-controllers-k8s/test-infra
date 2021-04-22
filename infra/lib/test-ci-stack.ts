import * as cdk from '@aws-cdk/core';
import * as s3 from '@aws-cdk/aws-s3';
import { CICluster, CIClusterProps } from './ci-cluster';

export const ARGOCD_NAMESPACE = "argocd";
export const PROW_NAMESPACE = "prow";

export type TestCIStackProps = cdk.StackProps & {
  clusterConfig: CIClusterProps
  logsBucket?: string
};

export class TestCIStack extends cdk.Stack {
  constructor(scope: cdk.Construct, id: string, props: TestCIStackProps) {
    super(scope, id, props);

    new CICluster(this, 'ArgoCDCICluster', props.clusterConfig);

    new s3.Bucket(this, 'LogsBucket', {
      bucketName: props.logsBucket || "ack-prow-logs-" + this.account
    });
  }
}
