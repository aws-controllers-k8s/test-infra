import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';
import * as kplus from 'cdk8s-plus-20';
import { PROW_NAMESPACE, PROW_JOB_NAMESPACE } from '../test-ci-stack';

export interface ProwGitHubSecretsChartProps {
  readonly personalAccessToken: string;
  readonly appId: string;
  readonly appClientId: string;
  readonly appPrivateKey: string;
  readonly appWebhookSecret: string;
}

export class ProwGitHubSecretsChart extends cdk8s.Chart {
  readonly pat: kplus.Secret;
  readonly prowjobPAT: kplus.Secret;

  readonly token: kplus.Secret;
  // github client secret to be used by prowjobs in PROW_JOB_NAMESPACE
  readonly prowjobToken: kplus.Secret;
  readonly hmacToken: kplus.Secret;

  constructor(scope: constructs.Construct, id: string, props: ProwGitHubSecretsChartProps) {
    super(scope, id);

    if (
        props.personalAccessToken === undefined ||
        props.appPrivateKey === undefined ||
        props.appClientId === undefined ||
        props.appWebhookSecret === undefined ||
        props.appId === undefined) {
      throw new Error(`Expected: GitHub bot PAT, bot Webhook HMAC, app ID, client ID, app private key, & app webhook HMAC token`);
    }
    if (props.appPrivateKey.length < 1500) {
      console.error("Found invalid app private key:  ", props.appPrivateKey);
      throw new Error(`Expected GitHub app private key to be in valid PEM format (and >= 1500 bytes)`);
    }

    // a GitHub PAT for use by various scripts for deploying code to repos
    this.pat = new kplus.Secret(this, 'github-pat-token', {
      stringData: {
        'token': props.personalAccessToken
      },
      metadata: {
        name: 'github-pat-token',
        namespace: PROW_NAMESPACE
      }
    });

    // a GitHub PAT for use by various Prow jobs
    this.prowjobPAT = new kplus.Secret(this, 'prowjob-github-pat-token', {
      stringData: {
        'token': props.personalAccessToken
      },
      metadata: {
        name: 'prowjob-github-pat-token',
        namespace: PROW_JOB_NAMESPACE
      }
    });

    // three pieces of important data from the GitHub app:  the private key, the app ID, and the client ID
    this.token = new kplus.Secret(this, 'github-token', {
      stringData: {
        'cert': props.appPrivateKey,
        'appid': props.appId,
        'clientid': props.appClientId
      },
      metadata: {
        name: 'github-token',
        namespace: PROW_NAMESPACE
      }
    });

    this.prowjobToken = new kplus.Secret(this, 'prowjob-github-token', {
      stringData: {
        'cert': props.appPrivateKey,
        'appid': props.appId,
        'clientid': props.appClientId
      },
      metadata: {
        name: 'prowjob-github-token',
        namespace: PROW_JOB_NAMESPACE
      }
    });

    this.hmacToken = new kplus.Secret(this, 'hmac-token', {
      stringData: {
        'hmac': props.appWebhookSecret
      },
      metadata: {
        name: 'hmac-token',
        namespace: PROW_NAMESPACE
      }
    });
  }
}