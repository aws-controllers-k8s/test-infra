import * as cdk from '@aws-cdk/core';
import * as eks from '@aws-cdk/aws-eks';
import * as cdk8s from 'cdk8s';
import * as s3 from '@aws-cdk/aws-s3';
import {ArgoCDConfigurationChart, ArgoCDConfigurationChartProps} from './charts/argocd-configuration';
import {ProwSecretsChart, ProwSecretsChartProps} from './charts/prow-secrets';
import {CommonNamespacesChart} from './charts/common-namespaces';
import { ARGOCD_NAMESPACE } from './test-ci-stack';

export type CIClusterCompileTimeProps = ProwSecretsChartProps & ArgoCDConfigurationChartProps &{
  readonly argoCDAdminPassword: string;
};

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

    if (props.argoCDAdminPassword === undefined) {
      throw new Error(`Expected ArgoCD Admin password to be specified in context`)
    }

    const commonNamespacesChart = this.testCluster.addCdk8sChart('common-namespaces',
      new CommonNamespacesChart(new cdk8s.App(), 'CommonNamespaces', {}))

    const argoCDChart = 
      this.testCluster.addHelmChart('argocd', {
        chart: 'argo-cd',
        repository: 'https://argoproj.github.io/argo-helm',
        version: '3.1.1',
        namespace: ARGOCD_NAMESPACE,
        values: {
          configs: {
            secret: {
              argocdServerAdminPassword: props.argoCDAdminPassword
            }
          },
          server: {
            service: {
              type: "LoadBalancer"
            }
          },
        }
      });

    const prowSecretsChart =
      this.testCluster.addCdk8sChart('prow-secrets', new ProwSecretsChart(
        new cdk8s.App(), 'ProwSecrets', props
      ));
    // Ensure namespaces are created before secrets
    prowSecretsChart.node.addDependency(commonNamespacesChart);

    const argoCDConfigChart = 
      this.testCluster.addCdk8sChart('argocd-configuration', new ArgoCDConfigurationChart(
        new cdk8s.App(), 'ArgoCDConfiguration', {}
      ));

    // Install in order, to ensure CRDs are in place
    argoCDConfigChart.node.addDependency(argoCDChart);
  }
}
