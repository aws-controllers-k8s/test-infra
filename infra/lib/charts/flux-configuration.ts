import * as path from 'path';
import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';

export interface FluxConfigurationChartProps {
}

export class FluxConfigurationChart extends cdk8s.Chart {
  readonly flux: cdk8s.Include;
  readonly appOfApps: cdk8s.Include;

  constructor(scope: constructs.Construct, id: string, props: FluxConfigurationChartProps) {
    super(scope, id);

    const appOfAppsManifest = path.join(__dirname, "app-of-apps.yaml");
    // Must import externally, as it is a CRD
    this.appOfApps = new cdk8s.Include(this, 'app-of-apps', {
      url: appOfAppsManifest
    });
  }
}