import { Construct } from "constructs";
import { aws_eks as eks, aws_ec2 as ec2, Stack } from "aws-cdk-lib";
import * as blueprints from "@aws-quickstart/eks-blueprints";
import * as cdk8s from "cdk8s";
import {
  ProwGitHubSecretsChart,
  ProwGitHubSecretsChartProps,
} from "./charts/prow-secrets";
import {
  STACK_NAME,
  FLUX_NAMESPACE,
  PROW_JOB_NAMESPACE,
  PROW_NAMESPACE,
  CLUSTER_NAME,
  CLUSTER_CONSTRUCT_NAME,
} from "./test-ci-stack";
import {
  GlobalResources,
  ImportHostedZoneProvider,
} from "@aws-quickstart/eks-blueprints";

export type CIClusterCompileTimeProps = ProwGitHubSecretsChartProps & {
  hostedZoneId: string;
};

export type CIClusterRuntimeProps = {};

export type CIClusterProps = CIClusterCompileTimeProps & CIClusterRuntimeProps;

export class CICluster extends Construct {
  readonly testCluster: eks.Cluster;

  readonly namespaceManifests: eks.KubernetesManifest[];

  constructor(scope: Construct, id: string, props: CIClusterProps) {
    super(scope, id);

    const clusterVersion = eks.KubernetesVersion.V1_23;

    const subnetTagPattern = `${STACK_NAME}/${CLUSTER_CONSTRUCT_NAME}/${CLUSTER_NAME}/${CLUSTER_NAME}-vpc/PrivateSubnet*`;
    const securityGroupTagPattern = "kubernetes.io/cluster/" + CLUSTER_NAME;
    const securityGroupTags = {
      [securityGroupTagPattern] : "owned",
    };

    const karpenterAddonProps = {
      version: "v0.24.0",
      requirements: [
          {
            key: 'node.kubernetes.io/instance-type',
            op: "In" as const,
            vals: ['m5.xlarge','m5.2xlarge','m5.4xlarge','m5.8xlarge'],
          },
          {
            key: 'kubernetes.io/arch',
            op: "In" as const,
            vals: ['amd64']
          },
          {
            key: 'karpenter.sh/capacity-type',
            op: "In" as const,
            vals: ['on-demand']
          },
      ],
      subnetTags: { "Name": subnetTagPattern },
      securityGroupTags: securityGroupTags,
      amiFamily: "AL2" as const,
      consolidation: { enabled: true },
      ttlSecondsUntilExpired: 1 * 60 * 60, // 1 hour in seconds
      interruptionHandling: true,
    }
    const karpenterAddOn = new blueprints.addons.KarpenterAddOn(karpenterAddonProps);

    const blueprintStack = blueprints.EksBlueprint.builder()
      .account(Stack.of(this).account)
      .region(Stack.of(this).region)
      .version(clusterVersion)
      .resourceProvider(
        GlobalResources.HostedZone,
        new ImportHostedZoneProvider(props.hostedZoneId)
      )
      .addOns(
        new blueprints.addons.CertManagerAddOn,
        new blueprints.addons.AwsLoadBalancerControllerAddOn,
        new blueprints.addons.VpcCniAddOn,
        karpenterAddOn,
        new blueprints.addons.EbsCsiDriverAddOn,
        new blueprints.addons.ExternalDnsAddOn({ hostedZoneResources: [GlobalResources.HostedZone] })
      )
      .build(this, CLUSTER_NAME);

    this.testCluster = blueprintStack.getClusterInfo().cluster;

    this.namespaceManifests = [PROW_JOB_NAMESPACE, PROW_NAMESPACE].map(
      this.createNamespace
    );

    this.installProwRequirements(props);
    this.installFlux();
  }

  createNamespace = (name: string) => {
    return new eks.KubernetesManifest(
      this.testCluster.stack,
      `${name}-namespace-struct`,
      {
        cluster: this.testCluster,
        manifest: [
          {
            apiVersion: "v1",
            kind: "Namespace",
            metadata: {
              name: name,
            },
          },
        ],
      }
    );
  };

  installFlux = () => {
    const fluxChart = this.testCluster.addHelmChart("flux2", {
      release: "flux2",
      chart: "flux2",
      repository: "https://fluxcd-community.github.io/helm-charts",
      namespace: FLUX_NAMESPACE,
      createNamespace: true,
      version: "0.19.2",
      values: {},
    });

    const fluxBootstrap = this.testCluster.addManifest(
      "FluxBootstrap",
      ...[
        {
          apiVersion: "source.toolkit.fluxcd.io/v1beta2",
          kind: "GitRepository",
          metadata: {
            name: "test-infra",
            namespace: "flux-system",
          },
          spec: {
            interval: "30s",
            ref: {
              branch: "main",
            },
            url: "https://github.com/aws-controllers-k8s/test-infra",
          },
        },
        {
          apiVersion: "kustomize.toolkit.fluxcd.io/v1beta2",
          kind: "Kustomization",
          metadata: {
            name: "all-apps",
            namespace: "flux-system",
          },
          spec: {
            interval: "5m",
            sourceRef: {
              kind: "GitRepository",
              name: "test-infra",
            },
            path: "./flux",
            prune: true,
            targetNamespace: "flux-system",
            validation: "client",
          },
        },
      ]
    );
    fluxBootstrap.node.addDependency(fluxChart);
  };

  installProwRequirements = (secretsProps: ProwGitHubSecretsChartProps) => {
    const prowSecretsApp = new cdk8s.App();
    const prowSecretsChart = this.testCluster.addCdk8sChart(
      "prow-secrets",
      new ProwGitHubSecretsChart(prowSecretsApp, "ProwSecrets", secretsProps)
    );

    // Ensure namespaces are created before secrets
    prowSecretsChart.node.addDependency(...this.namespaceManifests);
    prowSecretsApp.charts.forEach((chart) =>
      chart.addDependency(...this.namespaceManifests)
    );
  };
}
