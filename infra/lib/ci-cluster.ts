import * as cdk from '@aws-cdk/core';
import * as eks from '@aws-cdk/aws-eks';
import * as ec2 from '@aws-cdk/aws-ec2';
import * as iam from '@aws-cdk/aws-iam';
import * as cdk8s from 'cdk8s';
import { policies as ALBPolicies } from './policies/aws-load-balancer-controller-policy';
import { FluxConfigurationChart } from './charts/flux-configuration';
import { ProwSecretsChart, ProwSecretsChartProps } from './charts/prow-secrets';
import { NamespaceChart } from './charts/namespace';
import { EXTERNAL_DNS_NAMESPACE, FLUX_NAMESPACE, PROW_JOB_NAMESPACE, PROW_NAMESPACE } from './test-ci-stack';

export type CIClusterCompileTimeProps = ProwSecretsChartProps;

export type CIClusterRuntimeProps = {
};

export type CIClusterProps = CIClusterCompileTimeProps & CIClusterRuntimeProps;

export class CICluster extends cdk.Construct {
  readonly testCluster: eks.Cluster;
  readonly testNodegroup: eks.Nodegroup;
  readonly cdk8sApp: cdk8s.App = new cdk8s.App();

  constructor(scope: cdk.Construct, id: string, props: CIClusterProps) {
    super(scope, id);

    this.testCluster = new eks.Cluster(scope, 'TestInfraCluster', {
      version: eks.KubernetesVersion.V1_21,
      defaultCapacity: 0
    })
    this.testNodegroup = this.testCluster.addNodegroupCapacity('TestInfraNodegroup', {
      instanceTypes: [ec2.InstanceType.of(ec2.InstanceClass.M5, ec2.InstanceSize.XLARGE8)],
      minSize: 2,
      diskSize: 150,
    })

    this.installProwRequirements(props);
    this.installFlux();
    this.installFluxConfiguration();
    this.installExternalDNS();
    this.installAWSLoadBalancer();
  }

  createNamespace = (name: string) => {
    return this.testCluster.addCdk8sChart(`${name}-namespace-chart`,
      new NamespaceChart(this.cdk8sApp, `${name}Namespace`, {
        name: name
      }));
  }

  installFlux = () => {
    const fluxChart = this.testCluster.addHelmChart('flux2', {
      chart: 'flux2',
      repository: 'https://fluxcd-community.github.io/helm-charts',
      namespace: FLUX_NAMESPACE,
      createNamespace: true,
      version: '0.19.2',
      values: {},
    })
  }

  installFluxConfiguration = () => {
    const fluxConfigChart = this.testCluster.addCdk8sChart('flux-configuration',
      new FluxConfigurationChart(
        this.cdk8sApp, 'FluxConfiguration', {}
      )
    );
  }

  installProwRequirements = (secretsProps: ProwSecretsChartProps) => {
    let requiredNamespaces: eks.KubernetesManifest[] =
      [PROW_NAMESPACE, PROW_JOB_NAMESPACE].map(this.createNamespace);

    const prowSecretsChart =
      this.testCluster.addCdk8sChart('prow-secrets',
        new ProwSecretsChart(
          this.cdk8sApp, 'ProwSecrets', secretsProps
        )
      );

    // Ensure namespaces are created before secrets
    prowSecretsChart.node.addDependency(...requiredNamespaces);
  }

  installExternalDNS = () => {
    const externalDNSNamespace = this.createNamespace(EXTERNAL_DNS_NAMESPACE);

    const externalDNSServiceAccount =
      this.testCluster.addServiceAccount('external-dns-service-account', {
        namespace: EXTERNAL_DNS_NAMESPACE,
      });
    externalDNSServiceAccount.node.addDependency(externalDNSNamespace);
    externalDNSServiceAccount.addToPrincipalPolicy(new iam.PolicyStatement({
      effect: iam.Effect.ALLOW,
      actions: ["route53:ChangeResourceRecordSets"],
      resources: ["arn:aws:route53:::hostedzone/*"]
    }))
    externalDNSServiceAccount.addToPrincipalPolicy(new iam.PolicyStatement({
      effect: iam.Effect.ALLOW,
      actions: [
        "route53:ListHostedZones",
        "route53:ListResourceRecordSets"
      ],
      resources: ["*"]
    }));

    const helmChart = this.testCluster.addHelmChart('external-dns', {
      chart: 'external-dns',
      repository: 'https://charts.bitnami.com/bitnami',
      namespace: EXTERNAL_DNS_NAMESPACE,
      version: '4.11.1',
      values: {
        namespace: PROW_NAMESPACE, // Limit only to DNS in Prow
        sources: ["ingress"],
        policy: "upsert-only",
        serviceAccount: {
          create: false,
          name: externalDNSServiceAccount.serviceAccountName
        },
        aws: {
          zoneType: "public"
        }
      }
    });
    helmChart.node.addDependency(externalDNSNamespace);
  }

  installAWSLoadBalancer = () => {
    const serviceAccount =
      this.testCluster.addServiceAccount('alb-service-account', {
        namespace: 'kube-system',
      });
    ALBPolicies.map(policy => serviceAccount.addToPrincipalPolicy(policy))

    this.testCluster.addHelmChart('aws-load-balancer-controller', {
      chart: 'aws-load-balancer-controller',
      repository: 'https://aws.github.io/eks-charts',
      namespace: 'kube-system',
      version: '1.1.6',
      values: {
        clusterName: this.testCluster.clusterName,
        serviceAccount: {
          create: false,
          name: serviceAccount.serviceAccountName
        }
      }
    });
  }
}
