import * as cdk from '@aws-cdk/core';
import * as eks from '@aws-cdk/aws-eks';
import * as iam from '@aws-cdk/aws-iam';
import * as ssm from '@aws-cdk/aws-ssm';

export type ClusterSSMCompileProps = {
  pvreBucketName?: string;
}

export type ClusterSSMRuntimeProps = {
  account: string;
  region: string;
  cluster: eks.Cluster;
  nodes: eks.Nodegroup;
}

export type ClusterSSMProps = ClusterSSMCompileProps & ClusterSSMRuntimeProps;

export class ClusterSSM extends cdk.Construct {
  constructor(scope: cdk.Construct, id: string, props: ClusterSSMProps) {
    super(scope, id);

    // Only install if PVRE bucket is configured (optional)
    if (!props.pvreBucketName) {
      return;
    }

    props.nodes.role.addManagedPolicy(
      iam.ManagedPolicy.fromAwsManagedPolicyName(
        "AmazonSSMManagedInstanceCore"
      )
    );

    new ssm.CfnAssociation(this, 'InventoryCollection', {
      name: "AWS-GatherSoftwareInventory",
      associationName: `${props.account}-InventoryCollection`,
      scheduleExpression: "rate(12 hours)",
      targets: [
        {
          key: "tag:eks:cluster-name",
          values: [props.cluster.clusterName]
        }
      ]
    });

    new ssm.CfnResourceDataSync(this, 'PvreReporting', {
      bucketName: props.pvreBucketName,
      bucketRegion: props.region,
      syncFormat: "JsonSerDe",
      syncName: `${props.account}-PvreReporting`
    });
  }
}
