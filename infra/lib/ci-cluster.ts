import * as cdk from '@aws-cdk/core';
import * as eks from '@aws-cdk/aws-eks';
import * as cdk8s from 'cdk8s';
import {FluxChart} from './charts/flux';
import {FluxConfigurationChart} from './charts/flux-configuration';
import {ProwSecretsChart, ProwSecretsChartProps} from './charts/prow-secrets';
import {CommonNamespacesChart} from './charts/common-namespaces';

export type CIClusterCompileTimeProps = ProwSecretsChartProps;

export type CIClusterRuntimeProps = {
};

export type CIClusterProps = CIClusterCompileTimeProps & CIClusterRuntimeProps;

export class CICluster extends cdk.Construct {
  readonly testCluster: eks.Cluster;

  constructor(scope: cdk.Construct, id: string, props: CIClusterProps) {
    super(scope, id);

    this.testCluster = new eks.Cluster(scope, 'TestCluster', {
      version: eks.KubernetesVersion.V1_19,
    })

    const commonNamespacesChart = this.testCluster.addCdk8sChart('common-namespaces',
      new CommonNamespacesChart(new cdk8s.App(), 'CommonNamespaces', {}))

    const prowSecretsChart =
      this.testCluster.addCdk8sChart('prow-secrets',
        new ProwSecretsChart(
          new cdk8s.App(), 'ProwSecrets', props
        )
      );
    // Ensure namespaces are created before secrets
    prowSecretsChart.node.addDependency(commonNamespacesChart);

    const fluxChart = this.testCluster.addCdk8sChart('flux',
      new FluxChart(
        new cdk8s.App(), 'Flux', {}
      )
    );
    const fluxConfigChart = this.testCluster.addCdk8sChart('flux-configuration',
      new FluxConfigurationChart(
        new cdk8s.App(), 'FluxConfiguration', {}
      )
    );
    fluxConfigChart.node.addDependency(fluxChart);
  }
}
