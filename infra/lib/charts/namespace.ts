import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';
import * as kplus from 'cdk8s-plus';

export interface NamespaceChartProps {
  readonly name: string;
}

export class NamespaceChart extends cdk8s.Chart {
  readonly botPATSecret: kplus.Secret;
  readonly webhookHMACSecret: kplus.Secret;

  constructor(scope: constructs.Construct, id: string, props: NamespaceChartProps) {
    super(scope, id);

    new cdk8s.ApiObject(this, `${props.name}-namespace`, {
      apiVersion: "v1",
      kind: "Namespace",
      metadata: {
        name: props.name
      }
    });
  }
}