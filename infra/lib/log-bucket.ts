import { Construct } from "constructs";
import {
  aws_s3 as s3,
  aws_eks as eks,
  CfnOutput,
  RemovalPolicy,
} from "aws-cdk-lib";

export type LogBucketCompileProps = {
  logsBucketName: string;
  logsBucketImport: boolean;
};

export type LogBucketRuntimeProps = {
  account: string;
};

export type LogBucketProps = LogBucketCompileProps & LogBucketRuntimeProps;

export class LogBucket extends Construct {
  readonly bucket: s3.IBucket;
  readonly deploymentServiceAccountRole: eks.ServiceAccount;

  constructor(scope: Construct, id: string, props: LogBucketProps) {
    super(scope, id);

    let bucketName = props.logsBucketName || "ack-prow-logs-" + props.account;
    if (props.logsBucketImport) {
      this.bucket = s3.Bucket.fromBucketName(this, "LogsBucket", bucketName);
    } else {
      this.bucket = new s3.Bucket(this, "LogsBucket", {
        bucketName: bucketName,
        encryption: s3.BucketEncryption.S3_MANAGED,
        versioned: true,
      });
    }

    // Destroy bucket if name not specifically specified
    if (props.logsBucketName === undefined) {
      this.bucket.applyRemovalPolicy(RemovalPolicy.DESTROY);
    }

    new CfnOutput(this, "LogsBucketCfnOutput", {
      value: this.bucket.bucketName,
      exportName: "ProwLogsBucketName",
      description: "S3 bucket name for the Prow logs",
    });
  }
}
