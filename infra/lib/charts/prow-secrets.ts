import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';
import * as kplus from 'cdk8s-plus-20';
import { PROW_NAMESPACE, PROW_JOB_NAMESPACE } from '../test-ci-stack';

export interface ProwSecretsChartProps {
  readonly githubAppId: string;
  readonly githubAppClientId: string;
  readonly githubAppPrivateKey: string;
  readonly githubAppWebhookSecret: string;
}

export class ProwSecretsChart extends cdk8s.Chart {
  readonly githubToken: kplus.Secret;
  // github client secret to be used by prowjobs in PROW_JOB_NAMESPACE
  readonly prowjobGithubToken: kplus.Secret;
  readonly hmacToken: kplus.Secret;

  constructor(scope: constructs.Construct, id: string, props: ProwSecretsChartProps) {
    super(scope, id);

    if (props.githubAppPrivateKey === undefined || props.githubAppClientId === undefined || props.githubAppWebhookSecret === undefined || props.githubAppId === undefined) {
      console.trace()
      throw new Error(`Expected GitHub app ID & client ID & app private key & app webhook HMAC token to be specified`);
    }

    this.githubToken = new kplus.Secret(this, 'github-token', {
      stringData: {
        'cert': props.githubAppPrivateKey,
        'appid': props.githubAppId,
        'clientid': props.githubAppClientId
      },
      metadata: {
        name: 'github-token',
        namespace: PROW_NAMESPACE
      }
    });

    this.prowjobGithubToken = new kplus.Secret(this, 'prowjob-github-token', {
      stringData: {
        'cert': props.githubAppPrivateKey,
        'appid': props.githubAppId,
        'clientid': props.githubAppClientId
      },
      metadata: {
        name: 'prowjob-github-token',
        namespace: PROW_JOB_NAMESPACE
      }
    });

    this.hmacToken = new kplus.Secret(this, 'hmac-token', {
      stringData: {
        'hmac': props.githubAppWebhookSecret
      },
      metadata: {
        name: 'hmac-token',
        namespace: PROW_NAMESPACE
      }
    });
  }
}