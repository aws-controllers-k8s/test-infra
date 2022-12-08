import * as cdk from "@aws-cdk/core";
import * as eks from "@aws-cdk/aws-eks";
import * as s3 from "@aws-cdk/aws-s3";
import * as iam from "@aws-cdk/aws-iam";
import { PROW_JOB_NAMESPACE, PROW_NAMESPACE } from "./test-ci-stack";

export type ProwServiceAccountsProps = {
  account: string;
  stackPartition: string;
  region: string;

  prowCluster: eks.Cluster;
  namespaceManifests: eks.KubernetesManifest[];

  tideStatusBucket: s3.Bucket;
  presubmitsBucket: s3.Bucket;
  postsubmitsBucket: s3.Bucket;
};

export class ProwServiceAccounts extends cdk.Construct {
  readonly deploymentServiceAccount: eks.ServiceAccount;
  readonly presubmitJobServiceAccount: eks.ServiceAccount;
  readonly postsubmitJobServiceAccount: eks.ServiceAccount;

  constructor(
    scope: cdk.Construct,
    id: string,
    props: ProwServiceAccountsProps
  ) {
    super(scope, id);

    // Necessary only when splitting control and data plane
    // const dataplaneAccessPolicy = new iam.PolicyStatement({
    //   actions: ["eks:DescribeCluster"],
    //   resources: [props.prowCluster.clusterArn],
    // });

    const tideStatusReconcilerAccessPolicy = new iam.PolicyStatement({
      actions: ["s3:Get*", "s3:List*", "s3:Put*", "s3:DeleteObject"],
      resources: [
        `arn:aws:s3:::${props.tideStatusBucket.bucketName}/*`,
        `arn:aws:s3:::${props.tideStatusBucket.bucketName}`,
      ],
    });

    const preAssumeRolePolicy = new iam.PolicyStatement({
      actions: ["sts:AssumeRole"],
      resources: ["*"],
    });

    // Used to validate recommended-policy-arn in service controllers repository
    const preGetPolicyPolicy = new iam.PolicyStatement({
      actions: ["iam:GetPolicy"],
      resources: ["*"],
    });

    const preBucketAccessPolicy = new iam.PolicyStatement({
      actions: ["s3:Get*", "s3:List*", "s3:Put*", "s3:DeleteObject"],
      resources: [
        `arn:${props.stackPartition}:s3:::${props.presubmitsBucket.bucketName}/*`,
        `arn:${props.stackPartition}:s3:::${props.presubmitsBucket.bucketName}`,
      ],
    });

    const preParamStoreAccessPolicy = new iam.PolicyStatement({
      actions: ["ssm:Get*"],
      resources: [
        `arn:${props.stackPartition}:ssm:${props.region}:${props.account}:parameter/*`,
      ],
    });

    const preECRPublicReadOnlyPolicy = new iam.PolicyStatement({
      actions: [
        "ecr-public:GetAuthorizationToken",
        "sts:GetServiceBearerToken",
        "ecr-public:BatchCheckLayerAvailability",
        "ecr-public:GetRepositoryPolicy",
        "ecr-public:DescribeRepositories",
        "ecr-public:DescribeRegistries",
        "ecr-public:DescribeImages",
        "ecr-public:DescribeImageTags",
        "ecr-public:GetRepositoryCatalogData",
        "ecr-public:GetRegistryCatalogData",
      ],
      resources: ["*"],
    });

    const postBucketAccessPolicy = new iam.PolicyStatement({
      actions: ["s3:Get*", "s3:List*", "s3:Put*", "s3:DeleteObject"],
      resources: [
        `arn:${props.stackPartition}:s3:::${props.postsubmitsBucket.bucketName}/*`,
        `arn:${props.stackPartition}:s3:::${props.postsubmitsBucket.bucketName}`,
      ],
    });

    const postEcrPublicPolicy = new iam.PolicyStatement({
      actions: [
        // Read access
        "ecr-public:Describe*",
        "ecr-public:Get*",
        // Limited write access
        "ecr-public:CreateRepository",
        "ecr-public:PutRepositoryCatalogData",
        "ecr-public:UploadLayerPart",
        "ecr-public:CompleteLayerUpload",
        "ecr-public:InitiateLayerUpload",
        "ecr-public:PutImage",
        "ecr-public:ListTagsForResource",
        "ecr-public:PutRegistryCatalogData",
        "ecr-public:BatchCheckLayerAvailability",
      ],
      resources: [
        `arn:${props.stackPartition}:ecr-public::${props.account}:registry/*`,
        `arn:${props.stackPartition}:ecr-public::${props.account}:repository/*`,
      ],
    });

    const postEcrPublicAllResourcePolicy = new iam.PolicyStatement({
      actions: ["ecr-public:GetAuthorizationToken"],
      resources: ["*"],
    });

    const postStsPolicy = new iam.PolicyStatement({
      actions: ["sts:GetServiceBearerToken"],
      resources: ["*"],
    });

    // Assumes the Role in service team's account to access soak EKS cluster
    const postAssumeRolePolicy = new iam.PolicyStatement({
      actions: ["sts:AssumeRole"],
      resources: ["*"],
    });

    const postParamStoreAccessPolicy = new iam.PolicyStatement({
      actions: ["ssm:Get*"],
      resources: [
        `arn:${props.stackPartition}:ssm:${props.region}:${props.account}:parameter/*`,
      ],
    });

    // Service account for each of the Prow deployments
    // TODO(RedbackThomson): Split by service and assign individual permissions to each
    this.deploymentServiceAccount = props.prowCluster.addServiceAccount(
      "ProwDeploymentServiceAccount",
      {
        namespace: PROW_NAMESPACE,
        name: "prow-deployment-service-account",
      }
    );
    this.deploymentServiceAccount.node.addDependency(
      ...props.namespaceManifests
    );

    // this.deploymentServiceAccount.addToPrincipalPolicy(dataplaneAccessPolicy);
    this.deploymentServiceAccount.addToPrincipalPolicy(
      tideStatusReconcilerAccessPolicy
    );
    this.deploymentServiceAccount.addToPrincipalPolicy(preBucketAccessPolicy);
    this.deploymentServiceAccount.addToPrincipalPolicy(postBucketAccessPolicy);

    new cdk.CfnOutput(scope, "DeploymentServiceAccountRoleOutput", {
      value: this.deploymentServiceAccount.role.roleName,
      exportName: "DeploymentServiceAccountRoleName",
      description: "Role ARN for the Prow deployments service account",
    });

    // Service account for presubmit jobs
    this.presubmitJobServiceAccount = props.prowCluster.addServiceAccount(
      "PreSubmitJobServiceAccount",
      {
        namespace: PROW_JOB_NAMESPACE,
        name: "pre-submit-service-account",
      }
    );
    this.presubmitJobServiceAccount.node.addDependency(
      ...props.namespaceManifests
    );

    this.presubmitJobServiceAccount.addToPrincipalPolicy(preAssumeRolePolicy);
    this.presubmitJobServiceAccount.addToPrincipalPolicy(preGetPolicyPolicy);
    this.presubmitJobServiceAccount.addToPrincipalPolicy(preBucketAccessPolicy);
    this.presubmitJobServiceAccount.addToPrincipalPolicy(
      preParamStoreAccessPolicy
    );
    this.presubmitJobServiceAccount.addToPrincipalPolicy(
      preECRPublicReadOnlyPolicy
    );

    new cdk.CfnOutput(scope, "PreSubmitServiceAccountRoleOutput", {
      value: this.presubmitJobServiceAccount.role.roleName,
      exportName: "PreSubmitServiceAccountRoleName",
      description: "Role ARN for the Prow presubmit jobs' service account",
    });

    // Service account for postsubmit jobs
    this.postsubmitJobServiceAccount = props.prowCluster.addServiceAccount(
      "PostSubmitJobServiceAccount",
      {
        namespace: PROW_JOB_NAMESPACE,
        name: "post-submit-service-account",
      }
    );
    this.postsubmitJobServiceAccount.node.addDependency(
      ...props.namespaceManifests
    );
    this.postsubmitJobServiceAccount.addToPrincipalPolicy(
      postBucketAccessPolicy
    );
    this.postsubmitJobServiceAccount.addToPrincipalPolicy(postEcrPublicPolicy);
    this.postsubmitJobServiceAccount.addToPrincipalPolicy(
      postEcrPublicAllResourcePolicy
    );
    this.postsubmitJobServiceAccount.addToPrincipalPolicy(postStsPolicy);
    this.postsubmitJobServiceAccount.addToPrincipalPolicy(postAssumeRolePolicy);
    this.postsubmitJobServiceAccount.addToPrincipalPolicy(
      postParamStoreAccessPolicy
    );
    new cdk.CfnOutput(scope, "PostSubmitServiceAccountRoleOutput", {
      value: this.postsubmitJobServiceAccount.role.roleName,
      exportName: "PostSubmitServiceAccountRoleName",
      description: "Role ARN for the Prow postsubmit jobs' service account",
    });
  }
}
