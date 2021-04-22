import * as cdk from '@aws-cdk/core';
import * as eks from '@aws-cdk/aws-eks';
import * as cdk8s from 'cdk8s';
import {ArgoCDConfigurationChart, ArgoCDConfigurationChartProps} from './charts/argocd-configuration';
import {ProwSecretsChart, ProwSecretsChartProps} from './charts/prow-secrets';
import { ARGOCD_NAMESPACE } from './test-ci-stack';

export type CIClusterProps = ProwSecretsChartProps & ArgoCDConfigurationChartProps & {
  readonly argoCDAdminPassword: string;
};

export class CICluster {
  constructor(scope: cdk.Construct, id: string, props: CIClusterProps) {
    const cluster = new eks.Cluster(scope, id, {
      version: eks.KubernetesVersion.V1_19,
    })

    if (props.argoCDAdminPassword === undefined) {
      throw new Error(`Expected ArgoCD Admin password to be specified in context`)
    }

    const argoCDChart = 
      cluster.addHelmChart('argocd', {
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

    const prowSecretsConfigChart = new ProwSecretsChart(
      new cdk8s.App(), 'ProwSecrets', props
    );
    cluster.addCdk8sChart('prow-secrets', prowSecretsConfigChart);


    const argoCDConfigChart = 
      cluster.addCdk8sChart('argocd-configuration', new ArgoCDConfigurationChart(
        new cdk8s.App(), 'ArgoCDConfiguration', {}
      ));

    // Install in order, to ensure CRDs are in place
    argoCDConfigChart.node.addDependency(argoCDChart);
  }
}
