## Introduction
This document serves as the onboarding guide for AWS service teams to automate the soak testing for service controller using
[ACK prow cluster](https://prow.ack.aws.dev/) .

### Infrastructure Components
1. Prow cluster owned by ACK team, where the postsubmit job will execute.
2. An EKS cluster owned by service team. 
3. Monitoring resources in the EKS cluster to monitor soak test runs. Ex: Prometheus and Grafana. 
4. The IAM role which is used to create the EKS cluster in #2. This role will have trust relationship for ACK service so
   that ACK prow cluster can communicate the EKS cluster executing soak tests. 
4. The IAM role for service accounts to provide access to AWS for service controller and soak runner.

### Steps Overview
1. Whenever service teams cut a new semver release(Ex: v0.0.2) for the service controller, a postsubmit prow job is
triggered.
2. The postsubmit prowjob assumes the IAM role used by service teams to create the soak test EKS cluster.
3. The postsubmit prowjob uses EKS `update-kubeconfig` command to gain access to the soak test cluster.
4. Based on the artifacts in the latest release, prowjob starts the service controller in the soak test cluster.
5. Prowjob will build a container image which will execute the soak tests
   against the controller installed in #4 .
6. Prowjob creates a Kubernetes Job resource in Soak test cluster and waits for the job to complete.
7. Once the job completes, Prowjob cleans up the Job resource from #6 and the service controller resources from #4 .
8. Based on the monitoring and alarming setup by service teams in the soak test cluster, they can review the results of
   soak tests.

## Onboarding Guide

### Prerequisite
* An AWS account to host the cluster. This will be reused from service teams onboarding for e2e test executions.
* [yq](https://mikefarah.gitbook.io/yq/#install)
* [eksctl](https://docs.aws.amazon.com/eks/latest/userguide/eksctl.html)
* [helm](https://helm.sh/docs/intro/install/)

### Steps
1. Update the current shell with AWS Credentials of ack-test role. Replace "<my-service-name>" with actual service name
   in the command below.
   ```bash
   export SERVICE=<my-service-name> \
   && export ACK_TEST_ROLE_NAME=$SERVICE-ack-test-role-DO-NOT-DELETE \
   && export ACK_TEST_ROLE_ARN=$(aws iam get-role --role-name $ACK_TEST_ROLE_NAME --output text --query "Role.Arn") \
   && export AWS_CREDS_JSON=$(aws sts assume-role --role-arn $ACK_TEST_ROLE_ARN --role-session-name eks-cluster-create) \
   && export AWS_ACCESS_KEY_ID=$(echo $AWS_CREDS_JSON | yq eval '.Credentials.AccessKeyId' -) \
   && export AWS_SECRET_ACCESS_KEY=$(echo $AWS_CREDS_JSON | yq eval '.Credentials.SecretAccessKey' -) \
   && export AWS_SESSION_TOKEN=$(echo $AWS_CREDS_JSON | yq eval '.Credentials.SessionToken' -) \
   && export AWS_DEFAULT_REGION=us-west-2
   ```

2. Use `eksctl` to create an EKS cluster with assumed credentials
   * Create a sample config file
   ```bash
      eksctl create cluster --name=soak-test-cluster --nodes=2 --version=1.20 --with-oidc --dry-run --enable-ssm > eksctl-config.yaml
   ```
   * Update the config as per your need
   * Create the cluster. This may take ~10-20 minutes.
   ```bash
      eksctl create cluster --config-file=eksctl-config.yaml
   ```

3. Setup IRSA for soak test execution.
   * Export AWS account id and OIDC provider for the EKS cluster
   ```bash
   export ACCOUNT_ID=$(aws sts get-caller-identity --query "Account" --output text) \
   && export OIDC_PROVIDER=$(aws eks describe-cluster --name soak-test-cluster --query "cluster.identity.oidc.issuer" --output text | sed -e "s/^https:\/\///")
   ```
   * Create the trust relationship file for IRSA
   ```bash
   read -r -d '' TRUST_RELATIONSHIP <<EOF
   {
      "Version": "2012-10-17",
      "Statement": [
         {
            "Effect": "Allow",
            "Principal": {
               "Federated": "arn:aws:iam::${ACCOUNT_ID}:oidc-provider/${OIDC_PROVIDER}"
            },
            "Action": "sts:AssumeRoleWithWebIdentity",
            "Condition": {
               "StringLike": {
                  "${OIDC_PROVIDER}:sub": "system:serviceaccount:default:ack-*"
               }
            }
         }
      ]
   }
   EOF
   ```
   ```bash
   echo "${TRUST_RELATIONSHIP}" > trust.json
   ```
   * Create the IRSA
   ```bash
   export IRSA_NAME="$SERVICE-ack-irsa-DO-NOT-DELETE" \
   && aws iam create-role --role-name $IRSA_NAME --assume-role-policy-document file://trust.json --description "IRSA for ACK $SERVICE-controller"
   ```
   * Attach permission policy to IRSA. Note: You can provide any policy arn here. Default is Administrator here.
   ```bash
   aws iam attach-role-policy --role-name $IRSA_NAME --policy-arn=arn:aws:iam::aws:policy/AdministratorAccess
   ```

4. Setup monitoring for the soak tests
   * See steps from #2 to #4 in [soak/README.md](https://github.com/aws-controllers-k8s/test-infra/blob/main/soak/README.md) for
   setting up Prometheus and Grafana monitoring in your cluster.
   * ACK team will automate the soak test execution, but AWS service teams will still own monitoring, alerting.

5. Notify the ACK core team 
   * The test infrastructure needs to be made aware that service controller is ready for soak test execution using prow.
     This is a manual process for the ACK core contributor team. Therefore, once you have completed the above steps copy
     the IRSA ARN and send it to a member of the ACK core contributor team.

   * > **Note for Core Contributors:** Upon receiving a new IRSA ARN,
     access the ACK infrastructure account and add a new SSM string parameter with
     the path `/ack/prow/soak/irsa/<service>` and a value of the ARN.
   * ```bash
      # For ACK core contributors
      aws ssm put-parameter --name "/ack/prow/soak/irsa/$SERVICE" --type String --value <provided-value> 
      ```

6. By default, the soak-runner uses the [default configuration](https://github.com/aws-controllers-k8s/test-infra/blob/main/soak/default_soak_config.yaml)
   i.e. run e2e tests continuously for 24 hours. To provide custom behavior for soak tests, create a file in service-controller
   repository at "test/e2e/soak_config.yaml". Take a look at [default configuration](https://github.com/aws-controllers-k8s/test-infra/blob/main/soak/default_soak_config.yaml)
   for sample configuration.