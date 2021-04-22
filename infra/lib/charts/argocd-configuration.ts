import * as path from 'path';
import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';
import * as kplus from 'cdk8s-plus';
import { ARGOCD_NAMESPACE } from '../test-ci-stack';

export interface ArgoCDConfigurationChartProps {
  // readonly sourceRepository: string;
}

export class ArgoCDConfigurationChart extends cdk8s.Chart {
  readonly appOfApps: cdk8s.Include;

  constructor(scope: constructs.Construct, id: string, props: ArgoCDConfigurationChartProps) {
    super(scope, id, {namespace: ARGOCD_NAMESPACE});

    const appOfAppsManifest = path.join(__dirname, "app-of-apps.yaml");
    // Must import externally, as it is a CRD
    this.appOfApps = new cdk8s.Include(this, 'app-of-apps', {
      url: appOfAppsManifest
    });
  }
}