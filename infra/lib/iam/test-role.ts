import * as cdk from '@aws-cdk/core';
import * as iam from '@aws-cdk/aws-iam';
import * as ssm from '@aws-cdk/aws-ssm';
import { ArnPrincipal, ManagedPolicy } from '@aws-cdk/aws-iam';

export const DEFAULT_TEST_ROLE_PARAM_PATH = `/ack/prow/service_team_role/default`

export type DefaultTestRoleProps = {
  trustedEntities: iam.IRole[]
};

export class DefaultTestRole extends cdk.Construct {
  readonly role: iam.Role;
  readonly parameter: ssm.StringParameter;

  constructor(scope: cdk.Construct, id: string, props: DefaultTestRoleProps) {
    super(scope, id);

    this.role = new iam.Role(this, 'DefaultTestRole', {
      assumedBy: new iam.CompositePrincipal(...props.trustedEntities.map(role => new ArnPrincipal(role.roleArn))),
      // TODO (RedbackThomson): Downgrade to a secure policy
      managedPolicies: [ManagedPolicy.fromAwsManagedPolicyName("AdministratorAccess")]
    });

    this.parameter = new ssm.StringParameter(this, 'TestRoleParam', {
      parameterName: DEFAULT_TEST_ROLE_PARAM_PATH,
      stringValue: this.role.roleArn,
    });
  }
}
