## Introduction

The ACK core team provides a soak testing framework and template cluster where
service teams can execute long running soak tests to test the performance and
stability of the service controllers. Running soak tests is one of the
requirements for cutting the stable release for any service controller. Find
more details on the release process
[here](https://github.com/aws-controllers-k8s/community/blob/main/docs/content/docs/community/releases.md).

Under the hood, the soak test introduces load by repeatedly running the
end-to-end tests for the service controller. End to end tests are present in
service-controller Github repository. i.e.
"https://github.com/aws-controllers-k8s/\<service-name\>-controller".

Service teams are expected to deploy and maintain their own soak testing
infrastructure in their own accounts, based off the templates and configuration
provided in this repository. Soak tests will be started by Prow jobs before
cutting a new `stable` release, but can also be manually triggered. Use the
following guide to configure your soak testing infrastructure.

## Bootstrapping your soak test cluster

### Prerequisites
* A tool for building OCI images ([docker](https://docs.docker.com/get-docker/),
  [buildah](https://github.com/containers/buildah/blob/master/install.md) etc..)
* [helm](https://helm.sh/docs/intro/install/) 
* [kubectl](https://kubernetes.io/docs/tasks/tools/)
* [eksctl](https://docs.aws.amazon.com/eks/latest/userguide/eksctl.html)
* [yq](https://mikefarah.gitbook.io/yq/)
* [jq](https://stedolan.github.io/jq/)

### Before You Begin

Create a new AWS account to house the following resources. A new account ensures
that any leaked resources do not affect the e2e tests running on your pull
requests, and that you can give the ACK core team permissions to view this
account.

Export the credentials for an AWS user with administrator access to your
terminal before following the rest of the documents.

> **Note:** Bootstrapping the soak test cluster will create an IAM role in the
> account used to run the e2e tests for the soak load. By default this role has 
> the `PowerUserAccess` policy. Update `attachPolicyARNs` under the
> `ack-soak-controller` in the `cluster-config.yaml` file with the least
> privileges required to run your service's e2e tests.


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