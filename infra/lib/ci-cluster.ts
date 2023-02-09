import { Construct } from "constructs";
import { aws_eks as eks, aws_ec2 as ec2, Stack } from "aws-cdk-lib";
import * as blueprints from "@aws-quickstart/eks-blueprints";
import * as cdk8s from "cdk8s";
import {
  ProwGitHubSecretsChart,
  ProwGitHubSecretsChartProps,
} from "./charts/prow-secrets";
import {
  FLUX_NAMESPACE,
  PROW_JOB_NAMESPACE,
  PROW_NAMESPACE,
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

    const mngProps: blueprints.MngClusterProviderProps = {
      minSize: 2,
      maxSize: 8,
      desiredSize: 2,
      diskSize: 150,
      version: clusterVersion,
      instanceTypes: [
        ec2.InstanceType.of(ec2.InstanceClass.M5, ec2.InstanceSize.XLARGE8),
      ],
      amiType: eks.NodegroupAmiType.AL2_X86_64,
      nodeGroupCapacityType: eks.CapacityType.ON_DEMAND,
    };

    const blueprintStack = blueprints.EksBlueprint.builder()
      .account(Stack.of(this).account)
      .region(Stack.of(this).region)
      .version(clusterVersion)
      .clusterProvider(new blueprints.MngClusterProvider(mngProps))
      .resourceProvider(
        GlobalResources.HostedZone,
        new ImportHostedZoneProvider(props.hostedZoneId)
      )
      .addOns(
        new blueprints.addons.VpcCniAddOn(),
        new blueprints.addons.KarpenterAddOn(),
        new blueprints.addons.AwsLoadBalancerControllerAddOn(),
        new blueprints.addons.EbsCsiDriverAddOn(),
        new blueprints.addons.ExternalDnsAddOn({
          hostedZoneResources: [GlobalResources.HostedZone],
        })
      )
      .build(this, "TestInfraCluster");

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
