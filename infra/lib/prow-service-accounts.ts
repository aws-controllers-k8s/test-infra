import { Construct } from "constructs";
import {
  aws_s3 as s3,
  aws_eks as eks,
  aws_iam as iam,
  CfnOutput,
} from "aws-cdk-lib";
import { PROW_JOB_NAMESPACE, PROW_NAMESPACE } from "./test-ci-stack";

export type ProwServiceAccountsProps = {
  account: string;
  stackPartition: string;
  region: string;

  prowCluster: eks.Cluster;
  namespaceManifests: eks.KubernetesManifest[];

  tideStatusBucket: s3.IBucket;
  presubmitsBucket: s3.IBucket;
  postsubmitsBucket: s3.IBucket;
  periodicsBucket: s3.IBucket;
};

export class ProwServiceAccounts extends Construct {
  readonly deploymentServiceAccount: eks.ServiceAccount;
  readonly presubmitJobServiceAccount: eks.ServiceAccount;
  readonly postsubmitJobServiceAccount: eks.ServiceAccount;
  readonly periodicJobServiceAccount: eks.ServiceAccount;

  constructor(scope: Construct, id: string, props: ProwServiceAccountsProps) {
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

    // ** Pre-submit job policies

    const preBucketAccessPolicy = new iam.PolicyStatement({
      actions: ["s3:Get*", "s3:List*", "s3:Put*", "s3:DeleteObject"],
      resources: [
        `arn:${props.stackPartition}:s3:::${props.presubmitsBucket.bucketName}/*`,
        `arn:${props.stackPartition}:s3:::${props.presubmitsBucket.bucketName}`,
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

    const preAssumeRolePolicy = new iam.PolicyStatement({
      actions: ["sts:AssumeRole"],
      resources: ["*"],
    });

    // Used to validate recommended-policy-arn in service controllers repository
    const preGetPolicyPolicy = new iam.PolicyStatement({
      actions: ["iam:GetPolicy"],
      resources: ["*"],
    });

    const preParamStoreAccessPolicy = new iam.PolicyStatement({
      actions: ["ssm:Get*"],
      resources: [
        `arn:${props.stackPartition}:ssm:${props.region}:${props.account}:parameter/*`,
      ],
    });

    // ** Post-submit job policies

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

    // ** Periodic job policies

    const periodicBucketAccessPolicy = new iam.PolicyStatement({
      actions: ["s3:Get*", "s3:List*", "s3:Put*", "s3:DeleteObject"],
      resources: [
        `arn:${props.stackPartition}:s3:::${props.periodicsBucket.bucketName}/*`,
        `arn:${props.stackPartition}:s3:::${props.periodicsBucket.bucketName}`,
      ],
    });

    // ** Service accounts for each of the Prow deployments
    //    TODO(RedbackThomson): Split by service and assign individual permissions to each
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
    this.deploymentServiceAccount.addToPrincipalPolicy(
      periodicBucketAccessPolicy
    );

    new CfnOutput(scope, "DeploymentServiceAccountRoleOutput", {
      value: this.deploymentServiceAccount.role.roleName,
      exportName: "DeploymentServiceAccountRoleName",
      description: "Role ARN for the Prow deployments service account",
    });

    // ** Pre-submit job service account

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

    new CfnOutput(scope, "PreSubmitServiceAccountRoleOutput", {
      value: this.presubmitJobServiceAccount.role.roleName,
      exportName: "PreSubmitServiceAccountRoleName",
      description: "Role ARN for the Prow presubmit jobs' service account",
    });

    // ** Post-submit job service account

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
    new CfnOutput(scope, "PostSubmitServiceAccountRoleOutput", {
      value: this.postsubmitJobServiceAccount.role.roleName,
      exportName: "PostSubmitServiceAccountRoleName",
      description: "Role ARN for the Prow postsubmit jobs' service account",
    });

    // ** Periodic job service account

    this.periodicJobServiceAccount = props.prowCluster.addServiceAccount(
      "PeriodicJobServiceAccount",
      {
        namespace: PROW_JOB_NAMESPACE,
        name: "periodic-service-account",
      }
    );
    this.periodicJobServiceAccount.node.addDependency(
      ...props.namespaceManifests
    );
    this.periodicJobServiceAccount.addToPrincipalPolicy(
      periodicBucketAccessPolicy
    );
    new CfnOutput(scope, "PeriodicServiceAccountRoleOutput", {
      value: this.periodicJobServiceAccount.role.roleName,
      exportName: "PeriodicServiceAccountRoleName",
      description: "Role ARN for the Prow periodic jobs' service account",
    });
  }
}
