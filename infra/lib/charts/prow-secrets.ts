import * as cdk8s from 'cdk8s';
import * as constructs from 'constructs';
import * as kplus from 'cdk8s-plus-20';
import { PROW_NAMESPACE, PROW_JOB_NAMESPACE } from '../test-ci-stack';

export interface ProwSecretsChartProps {
  readonly githubPersonalAccessToken: string;
  readonly githubAppId: string;
  readonly githubAppClientId: string;
  readonly githubAppPrivateKey: string;
  readonly githubAppWebhookSecret: string;
}

export class ProwSecretsChart extends cdk8s.Chart {
  readonly githubPAT: kplus.Secret;
  readonly prowjobGithubPAT: kplus.Secret;

  readonly githubToken: kplus.Secret;
  // github client secret to be used by prowjobs in PROW_JOB_NAMESPACE
  readonly prowjobGithubToken: kplus.Secret;
  readonly hmacToken: kplus.Secret;

  constructor(scope: constructs.Construct, id: string, props: ProwSecretsChartProps) {
    super(scope, id);

    if (
        props.githubPersonalAccessToken === undefined ||
        props.githubAppPrivateKey === undefined ||
        props.githubAppClientId === undefined ||
        props.githubAppWebhookSecret === undefined ||
        props.githubAppId === undefined) {
      throw new Error(`Expected: GitHub bot PAT, GitHub bot Webhook HMAC, GitHub app ID, client ID, app private key, & app webhook HMAC token`);
    }
    if (props.githubAppPrivateKey.length < 1500) {
      console.error("Found invalid app private key:  ", props.githubAppPrivateKey);
      throw new Error(`Expected GitHub app private key to be in valid PEM format (and >= 1500 bytes)`);
    }

    // a GitHub PAT for use by various scripts for deploying code to repos
    this.githubPAT = new kplus.Secret(this, 'github-pat-token', {
      stringData: {
        'token': props.githubPersonalAccessToken
      },
      metadata: {
        name: 'github-pat-token',
        namespace: PROW_NAMESPACE
      }
    });

    // a GitHub PAT for use by various Prow jobs
    this.prowjobGithubPAT = new kplus.Secret(this, 'prowjob-github-pat-token', {
      stringData: {
        'token': props.githubPersonalAccessToken
      },
      metadata: {
        name: 'prowjob-github-pat-token',
        namespace: PROW_JOB_NAMESPACE
      }
    });

    // three pieces of important data from the GitHub app:  the private key, the app ID, and the client ID
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