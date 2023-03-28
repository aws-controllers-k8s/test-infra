import { Construct } from "constructs";
import { aws_ssm as ssm } from "aws-cdk-lib";

export type SSMInventoryCompileProps = {
  inventoryBucketName: string;
  inventoryBucketRegion: string
};

export type SSMInventoryRuntimeProps = {
  account: string
};

export type SSMInventoryProps = SSMInventoryCompileProps & SSMInventoryRuntimeProps;

export class SSMInventory extends Construct {
  constructor(scope: Construct, id: string, props: SSMInventoryProps) {
    super(scope, id);

    if (props.inventoryBucketName === undefined || props.inventoryBucketRegion === undefined) {
      throw new Error("Expected: Inventory bucket name and region to be defined")
    }

    new ssm.CfnAssociation(this, "GatherAssociation", {
      name: "AWS-GatherSoftwareInventory",
      scheduleExpression: "rate(30 minutes)",
      targets: [{
        // All instances within the account
        key: "InstanceIds",
        values: ["*"]
      }]
    })

    new ssm.CfnResourceDataSync(this, "SSMSync", {
      bucketName: props.inventoryBucketName,
      bucketRegion: props.inventoryBucketRegion,
      syncFormat: "JsonSerDe",
      syncName: `${props.account}-SSMReportingForAllInstances`
    })
  }
}
