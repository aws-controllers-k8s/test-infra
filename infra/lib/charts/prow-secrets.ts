import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';
import * as kplus from 'cdk8s-plus';
import { PROW_NAMESPACE } from '../test-ci-stack';

export interface ProwSecretsChartProps {
  readonly botPersonalAccessToken: string;
  readonly webhookHMACToken: string;
}

export class ProwSecretsChart extends cdk8s.Chart {
  readonly botPATSecret: kplus.Secret;
  readonly webhookHMACSecret: kplus.Secret;

  constructor(scope: constructs.Construct, id: string, props: ProwSecretsChartProps) {
    super(scope, id);

    if (props.botPersonalAccessToken === undefined || props.webhookHMACToken === undefined) {
      throw new Error(`Expected bot personal access token and webhook HMAC token to be specified`);
    }

    this.botPATSecret = new kplus.Secret(this, 'github-token', {
      stringData: {
        'token': props.botPersonalAccessToken
      },
      metadata: {
        name: 'github-token',
        namespace: PROW_NAMESPACE
      }
    });

    this.webhookHMACSecret = new kplus.Secret(this, 'hmac-token', {
      stringData: {
        'hmac': props.webhookHMACToken
      },
      metadata: {
        name: 'hmac-token',
        namespace: PROW_NAMESPACE
      }
    });
  }
}