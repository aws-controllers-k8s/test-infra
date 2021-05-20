import * as cdk from '@aws-cdk/core';
import * as iam from '@aws-cdk/aws-iam';
import * as ssm from '@aws-cdk/aws-ssm';
import { ArnPrincipal, ManagedPolicy } from '@aws-cdk/aws-iam';

export const TEST_ROLE_PARAM_NAME = 'ACK_ROLE_ARN'

export type TestRoleProps = {
  trustedEntities: iam.IRole[]
};

export class TestRole extends cdk.Construct {
  readonly role: iam.Role;
  readonly parameter: ssm.StringParameter;

  constructor(scope: cdk.Construct, id: string, props: TestRoleProps) {
    super(scope, id);

    this.role = new iam.Role(this, 'TestRole', {
      assumedBy: new iam.CompositePrincipal(...props.trustedEntities.map(role => new ArnPrincipal(role.roleArn))),
      // TODO (RedbackThomson): Downgrade to a secure policy
      managedPolicies: [ManagedPolicy.fromAwsManagedPolicyName("AdministratorAccess")]
    });

    this.parameter = new ssm.StringParameter(this, 'TestRoleParam', {
      parameterName: TEST_ROLE_PARAM_NAME,
      stringValue: this.role.roleArn,
    });
  }
}
