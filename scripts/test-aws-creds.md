### Introduction
This documentation talks about how AWS credentials are managed for running
ACK service controller end-to-end tests locally and on *Prow*.

The actual bash scripts to understand this functionality can be confusing so
this document serves as reference for debugging and making future updates for
this functionality.

### Overview
While running end-to-end tests, AWS credentials are needed inside the service
controller being tested as well as the test container executing the `run-tests.sh`
script.

### Service Controller
The `kind-build-test.sh` script assumes an IAM Role before creating the KinD cluster
and installing the ACK service controller being tested. When run from a Prow job,
the IAM Role is discovered from *SSM Parameter Store*. When run locally, the IAM Role
is determined from the `ACK_ROLE_ARN` environment variable.

For both Prow and local testing, a *background thread* is started, which
refreshes AWS credentials every **50 minutes**. This background process updates the
AWS credentials environment variables in service controller Kubernetes `deployment`
and restarts the `deployment`. This background process avoids AWS credential expiration
for tests running longer than **60 minutes**.

A good followup question for above background refresh will be **"Why didn't
you increase the `DurationSeconds` parameter when assuming IAM Role to maximum
allowed 12 hours? which should cover most cases"**

When an IAM Role assumes another IAM Role, the maximum session duration is
limited to 1 hour.

Since ACK prow jobs get default credentials by assuming an IAM role (*IRSA*),
credentials acquired by further assuming the test IAM role provided by service
teams expire within 1 hour. Hence the background refresh is a necessity
for running service controllers in ACK prow jobs.

For local testing, the background refresh was kept just to have the same
approach as ACK prow jobs instead of increasing maximum assume role duration
for `ACK_ROLE_ARN`

### Test Container
Test Container executes the `run-tests.sh` script which runs python e2e tests.
One difference between Service Controller and Test Container is that Test
Container cannot be restarted to refresh credentials using a background thread.

To overcome the credential expiry inside Test Container, ACK uses the
*AWS profile* from `~/.aws/credentials` file . When using AWS profiles
AWS SDK automatically refreshes the credentials before their expiry.

To run Test Container inside Prow, `build-run-test-dockerfile.sh` script mounts
`~/.aws/credentials` file inside Test Container with an **"ack-test"** profile which
is used to run the e2e tests. The source profile for this "ack-test" profile is the
IRSA identity of Prow job pod. See this [template](./templates/prow-test-aws-creds-template.txt)
for generated `~/.aws/credentials` file.

When running Test Container locally, the local environment will not always have
consistent IRSA source identity as seen in Prow environment. To enable credentials
refresh when running locally, `~/.aws/credentials` file is mounted inside
Test Container with "ack-test" AWS profile similar to Prow environment but
the content of local `~/.aws/credentials` file is also copied inside this mounted
file. See this [template](./templates/local-test-aws-creds-template.txt) for more
details.

When running Test Container locally, the source profile for "ack-test" AWS profile
is provided using `ACK_TEST_SOURCE_AWS_PROFILE` environment variable which defaults
to "default". 

NOTE
```
The source profile for "ack-test" profile is referred from local
"~/.aws/credentials" file because its contents gets copied into "~/.aws/credentials"
file of Test Container.
```

NOTE
```
The AWS profile approach used for Test Container is not used inside Service Controller
because currently ACK service controllers do not support mounting AWS "credentials" file.
```

TODO(vijtrip2): Add diagrams for visualization of role assumption and credential refresh
