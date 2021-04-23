import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';
import * as kplus from 'cdk8s-plus';
import { PROW_JOB_NAMESPACE, PROW_NAMESPACE } from '../test-ci-stack';

export interface CommonNamespacesChartProps {
}

export class CommonNamespacesChart extends cdk8s.Chart {
  readonly botPATSecret: kplus.Secret;
  readonly webhookHMACSecret: kplus.Secret;

  constructor(scope: constructs.Construct, id: string, props: CommonNamespacesChartProps) {
    super(scope, id);

    new cdk8s.ApiObject(this, 'prow-namespace', {
      apiVersion: "v1",
      kind: "Namespace",
      metadata: {
        name: PROW_NAMESPACE
      }
    });

    new cdk8s.ApiObject(this, 'test-pods-namespace', {
      apiVersion: "v1",
      kind: "Namespace",
      metadata: {
        name: PROW_JOB_NAMESPACE
      }
    });
  }
}