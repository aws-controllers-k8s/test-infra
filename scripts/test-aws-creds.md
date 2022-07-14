### Introduction
This documentation talks about how AWS credentials are managed for running ACK
service controller end-to-end tests locally and on *Prow*.

The actual bash scripts to understand this functionality can be confusing so
this document serves as reference for debugging and making future updates for
this functionality.

### Overview
While running end-to-end tests, AWS credentials are needed in the following
places:
- inside the service controller being tested 
- inside the test container executing the `pytest-local-runner.sh` script

### Service Controller
After the test scripts create a KIND cluster, they then install the ACK 
controller with credentials provided by assuming the testing IAM role. When run
from a Prow job, this IAM Role is discovered from *SSM Parameter Store*. When
run locally, this IAM Role is determined from the `test_config.yaml`file - under
`aws.assumed_role_arn`.

For both Prow and local testing, a *background thread* is started, which
refreshes AWS credentials every **50 minutes**. This background process updates
the AWS credentials environment variables in service controller Kubernetes
`deployment` and restarts the `deployment`. This background process avoids AWS
credential expiration for tests running longer than **60 minutes**.

One might follow up with the question **"Why didn't you increase the
`DurationSeconds` parameter when assuming IAM Role to maximum allowed 12 hours?
which should cover most cases"**

When an IAM Role assumes another IAM Role, the maximum session duration is
limited to 1 hour.

Since ACK Prow jobs get default credentials by assuming an IAM role (*IRSA*),
any credentials assumed within the job containers have a maximum expiration of 1
hour. Hence the background refresh process is necessary for running service
controllers in ACK Prow jobs which have tests spanning longer than 1 hour.

While this same limitation does not apply to testing on a local development
machine, the background refresh was kept just to have the same approach as ACK
Prow jobs (instead of increasing the maximum role assumption duration)

### test container
The e2e test container contains the Python runtime and requirements needed to
run the ACK Python tests. It is built inside the `pytest-image-runner.sh` file
and executes the `pytest-local-runner.sh` script (which runs the service's
Python e2e tests). One difference between the ACK service controller container
and the test container is that the test container cannot be restarted to refresh
credentials using a background thread.

To overcome the credential expiration inside test container, ACK uses the *AWS
profile* from `~/.aws/credentials` file. When using AWS profiles, the AWS SDK
automatically refreshes the credentials before their expiry.

To run the test container inside Prow, the `pytest-image-runner.sh` script
mounts the local `~/.aws/credentials` file inside test container with an
additionalS **"ack-test"** profile (which is used to run the e2e tests). The
source profile for this "ack-test" profile is the IRSA identity of Prow job pod.
See this [template](./templates/Prow-test-aws-creds-template.txt) for generated
`~/.aws/credentials` file.

When running test container locally, the local environment will not always have
consistent IRSA source identity as seen in Prow environment. To enable
credentials refresh when running locally, `~/.aws/credentials` file is mounted
inside test container with "ack-test" AWS profile similar to Prow environment
but the content of local `~/.aws/credentials` file is also copied inside this
mounted file. See this [template](./templates/local-test-aws-creds-template.txt)
for more details.

When running test container locally, the source profile for "ack-test" AWS
profile is provided using `aws.profile` in the `test_config.yaml` file which
defaults to "default".

NOTE:
> The source profile for "ack-test" profile is referred from local
> `~/.aws/credentials` file because its contents gets copied into
> `~/.aws/credentials` file of test container.

NOTE:
> The AWS profile approach used for test container is not used inside service
> controller because currently ACK service controllers do not support mounting AWS
> "credentials" file.

<!-- TODO(vijtrip2): Add diagrams for visualization of role assumption and credential
refresh -->
