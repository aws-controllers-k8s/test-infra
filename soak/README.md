## Introduction

The ACK core team provides a soak testing framework and template cluster where
service teams can execute long running soak tests to test the performance and
stability of the service controllers. Running soak tests is one of the
requirements for cutting the stable release for any service controller. Find
more details on the release process
[here](https://github.com/aws-controllers-k8s/community/blob/main/docs/contents/releases.md).

Under the hood, the soak test introduces load by repeatedly running the
end-to-end tests for the service controller. End to end tests are present in
service-controller Github repository. i.e.
"https://github.com/aws-controllers-k8s/\<service-name\>-controller".

Service teams are expected to deploy and maintain their own soak testing
infrastructure in their own accounts, based off the templates and configuration
provided in this repository. Soak tests will be started by Prow jobs before
cutting a new `stable` release, but can also be manually triggered. Use the
following guide to configure your soak testing infrastructure.

## Running soak tests with Prow jobs

After confirming that the soak tests work manually, follow the [Prow soak test
document](./prow/README.md) for onboarding your soak tests onto the Prow
automation.


## Bootstrapping your soak test cluster

### Prerequisites
* A tool for building OCI images ([docker](https://docs.docker.com/get-docker/),
  [buildah](https://github.com/containers/buildah/blob/master/install.md) etc..)
* [helm](https://helm.sh/docs/intro/install/) 
* [kubectl](https://kubernetes.io/docs/tasks/tools/)
* [yq](https://mikefarah.gitbook.io/yq/)
* [jq](https://stedolan.github.io/jq/)

### Before You Begin

Create a new AWS account to house the following resources. A new account ensures
that any leaked resources do not affect the e2e tests running on your pull
requests, and that you can give the ACK core team permissions to view this
account.

Export the credentials for an AWS user with administrator access to your
terminal before following the rest of the documents.

### Step 1 (Run the bootstrapping script)

Run the `bootstrap.sh` script in the current directory. This script will do the
following: 
1. Create an ECR public repository for the soak test runner
1. Create an EKS cluster with the default soak test configuration (from
  `cluster-config.yaml`)
1. Install the controller Helm chart
1. Install Prometheus and Grafana
1. Install the custom ACK soak test Grafana dashboard (from
   `./monitoring/grafana/ack-dashboard-source.json`)
1. Build and push the soak test runner image
1. Install the soak test runner Helm chart

The script requires the name of the service and the semver tag of the controller
version up for testing. For example:
```bash
./bootstrap.sh s3 v0.0.12
```

### Step 2 (Log onto the Prometheus dashboard)

After the script concludes, it should provide an example of the command to
port-forward Prometheus. By default, the command should look like the following:

```bash
kubectl port-forward -n prometheus service/kube-prom-grafana 3000:80 --address='0.0.0.0' >/dev/null &
```

Navigate to `http://127.0.0.1:3000/` and log in. You should be able to log into
the dashboard with the following credentials:
- Username: `admin`
- Password: `prom-operator`

Using the menu bar on the left, side navigate to `Dashboards` > `Browse`. Select
the `ACK Dashboard` at the top of the list. The `ACK Dashboard` will show the
ACK-specific request counts and error codes used by the controller when making
calls to AWS APIs. 

The `Kubernetes/Compute Resources/Pod` dashboard will show the resource
consumption by the controller pod.

### Step 3 (Notify the ACK core team)

The ACK core team needs to be made aware that service controller is ready for
soak test execution using Prow, which is a manual process. Therefore, once you
have completed the above steps copy the IRSA ARN from the following command, and
send it to a member of the ACK core contributor team:
```bash
kubectl get sa -n ack-system ack-core-account -o json | jq -r ".metadata.annotations.\"eks.amazonaws.com/role-arn\""
```

> **Note for Core Contributors:** Upon receiving a new IRSA ARN, access the
ACK infrastructure account and add a new SSM string parameter with the path
`/ack/prow/soak/irsa/<service>` and a value of the ARN.
```bash
# For ACK core contributors
aws ssm put-parameter --name "/ack/prow/soak/irsa/$SERVICE" --type String
--value <provided-value> 
```

### Step 4 (Customise your soak tests)

By default, the soak-runner uses the [default
configuration](https://github.com/aws-controllers-k8s/test-infra/blob/main/soak/default_soak_config.yaml)
i.e. run e2e tests continuously for 24 hours. To provide custom behavior for
your soak tests, create a file in the service-controller repository at
`test/e2e/soak_config.yaml`. Take a look at [default
configuration](https://github.com/aws-controllers-k8s/test-infra/blob/main/soak/default_soak_config.yaml)
for sample configuration.