import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';

export interface FluxChartProps {
}

export class FluxChart extends cdk8s.Chart {
  readonly flux: cdk8s.Include;
  readonly appOfApps: cdk8s.Include;

  constructor(scope: constructs.Construct, id: string, props: FluxChartProps) {
    super(scope, id);

    this.flux = new cdk8s.Include(this, 'flux', {
      url: 'https://github.com/fluxcd/flux2/releases/download/v0.13.1/install.yaml'
    });
  }
}