# Service Team Onboarding

This document outlines the steps required to onboard a new service and service
team into the testing infrastructure. 

## Prerequisites

Before starting the onboarding process, the service team should have already
created their service controller repository and have created a **_new_** AWS
account solely for the purposes of hosting test resources.

## 1. Create an ACK test role

When running e2e or soak tests, the test infrastructure will assume a role and
create AWS resources in your account. In this section we will create that role 
and provide access for the ACK infrastructure system. See the
[IAM doc][iam-doc] for more details on how the system uses credentials.

[iam-doc]: iam-structure.md

### a. Create the base role

In the AWS console, navigate to IAM and create a new Role. For now, choose
`EC2` as the trusted entity. Initially select the `AdministratorAccess` 
policy, although your team can adjust policies for stricter requirements in
the future. Set the role name to `<service>-ack-test-role-DO-NOT-DELETE`.

> **Note:** This role is also used for creating the EKS cluster, which is needed
for soak test execution. Make sure this Role has at least EKS admin privileges to
create the EKS cluster for soak tests.

The testing infrastructure will need to assume this role for the duration of the
integration tests. The default maximum session duration needs to be extended out
to ensure the credentials do not expire during this period. In the settings of 
the IAM role, under `Maximum session duration`, extend this option to the 
maximum of 12 hours and save changes.

```bash
export SERVICE=<my-service-name>
export ACK_TEST_ROLE_NAME=$SERVICE-ack-test-role-DO-NOT-DELETE
aws iam create-role --role-name $ACK_TEST_ROLE_NAME \
  --max-session-duration 43200 \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":{"Effect"'\
':"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":'\
'"sts:AssumeRole"}}'
aws iam attach-role-policy --role-name $ACK_TEST_ROLE_NAME \
  --policy-arn arn:aws:iam::aws:policy/AdministratorAccess
```

### b. Update the trust relationship

We cannot make the trust relationship public, for security reasons. Before 
continuing, message a member of the ACK core contributor team to get the 
details of the updated trust relationship. Once you have that, follow the step 
below to modify the role.

In the details for the newly created role, navigate to the `Trust
relationships` tab and click on `Edit trust relationship`. Add the new trust
relationship as provided by the ACK core contributor team.

### c. Notify the ACK core team

The test infrastructure needs to be made aware of the new service team role
to use in place of the default fallback. This is a manual process for the ACK
core contributor team. Therefore, once you have completed the above steps copy
the role ARN and send it to a member of the ACK core contributor team.

> **Note for Core Contributors:** Upon receiving a new service team role ARN, 
access the ACK infrastructure account and add a new SSM string parameter with
the path `/ack/prow/service_team_role/<service>` and a value of the ARN.
```bash
# For ACK core contributors
aws ssm put-parameter --name "/ack/prow/service_team_role/$SERVICE" \
  --type String --value <provided-value> 
```

## 2. Update `OWNERS_ALIASES` file

Prow does not use Github teams as the source of truth for who has ownership 
over any given repository. Instead it relies on the Kubernetes
[`OWNERS`][owners] file structure. Therefore, whenever any member of your team
joins or leaves, you must update the Github usernames of the team in your 
repository accordingly.

In your repository, edit the `OWNERS_ALIASES` file and edit the `service-team`
alias to include each of the Github usernames of your team members. Do 
**NOT** modify any members in the `core-ack-team` alias. Below is an
example:

```yaml
  service-team:
  - memberA
  - memberB
  - memberC
```

Once modified, create a pull request and notify a member of the ACK core 
contributor team who will subsequently approve and merge the request.

[owners]: https://www.kubernetes.dev/docs/guide/owners/

> **Note for Core Contributors:** `ack-bot` may complain about the accounts not
being public members of the `aws-controllers-k8s` organisation. This should not
affect their ability to approve or merge pull requests and can be ignored.

## 3. Setup GitHub Action Workflow For Creating GitHub Release

ACK Prowjob automatically updates the ACK runtime dependency in the service controller
repository whenever a new ACK runtime version is available and then creates a pull
request.
When this pull request gets merged, ACK Prowjob also automatically cuts a new patch
release for the service controller.

To create this release and update the changelog, ACK currently relies on a GitHub
Action workflow. Follow the steps below to do this one time setup for service
controller repository.

* Click on 'Actions' tab on service-controller GitHub page
* Click 'Set up this workflow' button on "Create Github Release Workflow" panel
* Click 'Start commit' button and then 'Commit new file' directly into main branch

This is it. With 4 clicks, the automatic patch releases for your controller is
setup now.

## 4. Setup Soak Test Infrastructure

Follow the guide [here](https://github.com/aws-controllers-k8s/test-infra/blob/main/soak/prow/README.md) to setup soak
test infrastructure

## Done!

Your Github repository should now be configured to run any pre- and 
post-submit tests. In your next pull request, you should expect the following
behaviour:

* The bot should post a comment when the PR is first submitted
* Members of the team should be able to run `/ok-to-test`
  * Only do so after verifying the PR does not have any malicious code
* Members of the team should be able to `/approve` and `/lgtm`
* The bot should appropriately place labels and respect the `OWNERS` file
* The bot should merge the pull request after receiving an `/lgtm`

If any of these behaviours is not working as expected, reach out on the ACK
Slack channel for guidance.
