## Introduction

The ACK core team provides a soak testing framework where service teams can
execute long running soak tests to test the performance and stability of the
service controllers. Running soak tests is one of the requirements for cutting
a new major version release for any service controller. Find more details on
the release process
[here](https://github.com/aws-controllers-k8s/community/blob/main/docs/content/docs/community/releases.md).

Under the hood, the soak test introduces load by repeatedly running the
end-to-end tests for the service controller. End to end tests are present in
service-controller Github repository. i.e.
"https://github.com/aws-controllers-k8s/\<service-name\>-controller".

Service teams are expected to deploy and maintain their own soak testing
infrastructure in their own accounts, based off the templates and configuration
provided in this repository. Soak tests will be started by Prow jobs before
cutting a new major version release, but can also be manually triggered. Use the
following guide to configure your soak testing infrastructure.

## Parallel Soak Testing

All resources created by this framework are scoped per AWS service, allowing
multiple controllers to be soak-tested in parallel without conflicts. Each
invocation of `bootstrap.sh` creates its own:

| Resource | Naming Convention |
|----------|-------------------|
| EKS Cluster | `ack-soak-<service>` |
| ECR Public Repo | `ack-<service>-soak` |
| Controller Helm Release | `soak-test-<service>` |
| Soak Runner Helm Release | `soak-runner-<service>` |
| Prometheus Namespace | `prometheus-<service>` |
| Prometheus Helm Release | `kube-prom-<service>` |
| Loki Helm Release | `loki-<service>` |

For example, running soak tests for both `s3` and `ecr` simultaneously:
```bash
./bootstrap.sh s3 v1.0.0
./bootstrap.sh ecr v0.0.12
```

This creates two independent EKS clusters (`ack-soak-s3` and `ack-soak-ecr`),
each with their own monitoring stack and test runner.

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

Create a [new AWS account](../docs/onboarding.md#prerequisites) to house the following resources. A new account ensures
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
1. Create an ECR public repository for the soak test runner (`ack-<service>-soak`)
1. Create an EKS cluster named `ack-soak-<service>` with the default soak test
   configuration (from `cluster-config.yaml`). This includes the EBS CSI driver
   addon (with an EKS Pod Identity association), which is required to provision
   the persistent volume that stores controller logs in Loki.
1. Install the controller Helm chart (`soak-test-<service>`)
1. Install Prometheus, Grafana, and Loki in namespace `prometheus-<service>`
1. Install the custom ACK soak test Grafana dashboard (from
   `./monitoring/grafana/ack-dashboard-source.json`)
1. Build and push the soak test runner image
1. Install the soak test runner Helm chart (`soak-runner-<service>`)

The script requires the name of the service and the semver tag of the controller
version up for testing. For example:
```bash
./bootstrap.sh s3 v0.0.12
```

### Step 2 (Log onto the Grafana dashboard)

After the script concludes, it will provide the exact command to port-forward
Grafana. The command includes the service-specific namespace:

```bash
kubectl port-forward -n prometheus-s3 service/kube-prom-s3-grafana 3000:80 --address='0.0.0.0' >/dev/null &
```

Navigate to `http://127.0.0.1:3000/` and log in. Retrieve the admin password:
```bash
kubectl get secret -n prometheus-<service> kube-prom-<service>-grafana \
    -o jsonpath='{.data.admin-password}' | base64 -d && echo
```

Using the menu bar on the left, navigate to `Dashboards` > `Browse`. Select
the `ACK Dashboard` at the top of the list. The `ACK Dashboard` will show the
ACK-specific request counts and error codes used by the controller when making
calls to AWS APIs. 

The `Kubernetes/Compute Resources/Pod` dashboard will show the resource
consumption by the controller pod.

### Step 3 (Monitor soak test progress)

Check the status of the soak test Job:
```bash
kubectl get jobs -n ack-system | grep <service>
kubectl logs -n ack-system -f job/<service>-soak-test
```

### Step 4 (View all dashboards)

To discover all running soak tests and open their Grafana dashboards:
```bash
./dashboards.sh
```

This script will:
1. Scan for all `ack-soak-*` EKS clusters in the region
2. Port-forward each cluster's Grafana on sequential ports (starting at 3000)
3. Print the URL and credentials for each dashboard

Example output:
```
========================================
 ACK Soak Test Dashboards
========================================

  emrserverless
    Cluster:   ack-soak-emrserverless
    Dashboard: http://localhost:3000/
    Creds:     admin / <password>
    Soak Job:  Running

  glue
    Cluster:   ack-soak-glue
    Dashboard: http://localhost:3001/
    Creds:     admin / <password>
    Soak Job:  Running

========================================
 All dashboards are port-forwarded.
 Stop with: pkill -f 'kubectl port-forward.*grafana'
========================================
```

To stop all port-forwards:
```bash
pkill -f 'kubectl port-forward.*grafana'
```

### Investigating test failures

Controller and test-runner logs are shipped by Promtail and stored in Loki on a
persistent volume (provisioned by the EBS CSI driver installed with the
cluster). Because the logs are persisted, you can investigate a failed test run
even after the controller pod restarts — for the lifetime of the cluster.

> **Note:** Logs live only as long as the cluster. The Loki volume uses
> `reclaimPolicy: Delete`, so investigate before tearing the cluster down.

**Option A — Grafana (Explore view):**

1. Open the service's Grafana (see Step 2 or run `./dashboards.sh`).
2. Go to `Explore` and select the `Loki` datasource.
3. Run a LogQL query. Useful examples:
   ```logql
   # All controller logs
   {namespace="ack-system", container="controller"}

   # Only controller errors
   {namespace="ack-system", container="controller"} |= "\"level\":\"error\""

   # Test-runner (pytest) output
   {namespace="ack-system", pod=~"<service>-soak-test.*"}
   ```
4. Use the time picker to narrow to the window of the failed iteration.

**Option B — Loki API (scriptable):**

Query Loki directly from inside the Loki pod (no extra port-forward needed):
```bash
NS=prometheus-<service>
LOKI_POD=loki-<service>-0

# Find the controller pod name
CTRL_POD=$(kubectl get pods -n ack-system --no-headers | grep chart | awk '{print $1}')

# Query the last 30 minutes of controller logs
END=$(date +%s)000000000
START=$(( $(date +%s) - 1800 ))000000000
kubectl exec -n "$NS" "$LOKI_POD" -- \
  wget -qO- "http://localhost:3100/loki/api/v1/query_range?query=%7Bpod%3D%22${CTRL_POD}%22%7D&start=${START}&end=${END}&limit=2000&direction=backward"
```

**Correlating a failed test with controller activity:**

1. From the test-runner stream, find the timestamp of the failing iteration
   (look for the pytest summary line, e.g. `1 failed, 2 passed ...`).
2. Query the controller stream (`{pod="<controller-pod>"}`) for the same time
   window to see exactly what the controller did — `created/updated/deleted
   resource`, `desired resource state has changed` deltas, and any
   `Reconciler error` / AWS API exceptions.

This is the fastest way to root-cause issues like reconciliation churn, stuck
resources, or AWS-side errors that surface as test failures.

### Step 5 (Tear down resources)

After reviewing results, clean up all resources for a specific service:
```bash
./teardown.sh <service>
```

Or tear down all soak clusters at once:
```bash
./teardown-all.sh
```

Or manually:
```bash
eksctl delete cluster --name ack-soak-<service> --region us-west-2
aws --region us-east-1 ecr-public delete-repository --repository-name ack-<service>-soak --force
```

## Batch Operations

### Running all soak tests at once

The `run-all.sh` script launches soak tests for multiple controllers in parallel.
Edit the `CONTROLLERS` array in the script to configure which services to test:

```bash
./run-all.sh
```

Environment variables:
- `TEST_DURATION_DAYS` — Duration per test (default: 7)
- `MAX_PARALLEL` — Max concurrent bootstraps (default: 5)

Logs are written to `/tmp/soak-logs/<service>.log`.

### Tearing down all soak clusters

```bash
./teardown-all.sh
```

This discovers and deletes all `ack-soak-*` clusters in parallel.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DEPLOY_REGION` | `us-west-2` | AWS region for cluster and resources |
| `CLUSTER_NAME` | `ack-soak-<service>` | Override the EKS cluster name |
| `SOAK_IMAGE_REPO_NAME` | `ack-<service>-soak` | ECR public repo name |
| `OCI_BUILDER` | `docker` | Container image builder binary |
| `TEST_DURATION_DAYS` | `1` | Days to run soak test |
| `TEST_DURATION_HOURS` | `0` | Additional hours |
| `TEST_DURATION_MINUTES` | `0` | Additional minutes |
| `CONTROLLER_IMAGE_REPO` | Auto-detected | Controller image repository URL |
