## Introduction

ACK team provides the soak testing framework where service teams can execute long running soak tests for testing the
performance and stability of the service controllers. Running soak tests is also a requirement for cutting the stable
release for the service controller. Find more details on the release process [here](https://github.com/aws-controllers-k8s/community/blob/main/docs/contents/releases.md).

Under the hood, the soak test introduces load by repeatedly running the end-to-end tests for the service controller. End
to end tests are present in service-controller github repository. i.e. "https://github.com/aws-controllers-k8s/<service-name>-controller".

If you wish to run soak tests for your service controller locally, see the "How To Run Soak Test Locally" section below.

## Running Soak Tests Using ACK Prow Jobs

TBD


## How To Run Soak Test Locally

### Prerequisites
* A tool for building OCI images ([docker](https://docs.docker.com/get-docker/), [buildah](https://github.com/containers/buildah/blob/master/install.md) etc..)
* A [Kubernetes cluster](https://docs.aws.amazon.com/eks/latest/userguide/getting-started.html) to run soak tests
  * We recommend using the `cluster-config.yaml` file with EKSCTL
  * Associate the cluster with OIDC and create an IAM role to authenticate the controller
* A [Public ECR repository](https://docs.aws.amazon.com/AmazonECR/latest/public/public-repository-create.html) to host the soak test image
  * Suggested name is `ack-<service>-soak` (e.g. `ack-s3-soak`)
* [helm](https://helm.sh/docs/intro/install/) 
* [kubectl](https://kubernetes.io/docs/tasks/tools/)
* [curl](https://curl.se/download.html)
* [yq](https://mikefarah.gitbook.io/yq/)
* [jq](https://stedolan.github.io/jq/)

### Before You Begin

If you are running soak tests locally for the first time, continue reading, otherwise skip to step 0.

Make sure that service controller release that you are testing has following three characteristics:
1. `aws-controllers-k8s/runtime` version is v0.2.2 or higher
2. helm chart release of the service controller has "metrics-service.yaml" template
3. `aws-controllers-k8s/test-infra` dependency inside `test/e2e/requirements.txt` is at
    `3d5e98f5960ac2ea8360c212141c4ec89cfcb668` or a later commit.

If this is not the case, please create a new release for your service controller and use it for soak testing.
Here is a sample [PR](https://github.com/aws-controllers-k8s/ecr-controller/pull/7) for ECR service controller.

### Step 0 (Declare the ACK soak test configuration)

* Setup soak test directory
    ```bash
    mkdir -p ~/.ack/soak && cd ~/.ack/soak
    ```

* Create a file name `config` and copy the following configuration into that file, substituting "< placeholders >" with
actual values.

    ```bash
    # NOTE: Substitute all the <placeholders> with actual values.
    
    # A handy alias to go to root of ACK soak test directory
    alias go-to-soak='cd ~/.ack/soak'
    
    ### SERVICE CONTROLLER CONFIGURATION ###
    
    # AWS Service name of the controller under test. Ex: apigatewayv2
    export SERVICE_NAME=<aws-service-name>
    
    # IAM Role Arn which will provide privileges to service account running
    # controller pod. This role will also provide access to the soak-test-runner
    # for validating e2e test results
    export IAM_ROLE_ARN_FOR_IRSA=<"IAM Role ARN for providing AWS access to ack controller's service account">

    export HELM_EXPERIMENTAL_OCI=1
    
    # Semver version of the controller under test. Ex: v0.0.2
    export CONTROLLER_VERSION=<semver>
    export CONTROLLER_CHART_URL=public.ecr.aws/aws-controllers-k8s/$SERVICE_NAME-chart
    
    # AWS Region for ACK service controller
    export CONTROLLER_AWS_REGION=us-west-2
  
    # AWS Account Id for ACK service controller
    export CONTROLLER_AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query "Account" --output text)
    
    # Release name of controller helm chart.
    export CONTROLLER_CHART_RELEASE_NAME=soak-test
        
    # Evaluation string for yq tool for updating helm chart's values.yaml file
    export SERVICE_ACCOUNT_ANNOTATION_EVAL=".serviceAccount.annotations.\"eks.amazonaws.com/role-arn\" = \"$IAM_ROLE_ARN_FOR_IRSA\""
    export METRIC_SERVICE_CREATE_EVAL=".metrics.service.create = true"
    export METRIC_SERVICE_TYPE_EVAL=".metrics.service.type = \"ClusterIP\""
    export AWS_REGION_EVAL=".aws.region = \"$CONTROLLER_AWS_REGION\""
    
    ### PROMETHEUS, GRAFANA CONFIGURATION ###
    
    # Release name of kube-prometheus helm chart.
    export PROM_CHART_RELEASE_NAME=kube-prom
    
    # Local port to access Prometheus dashbaord
    export LOCAL_PROMETHEUS_PORT=9090
    
    # Local port to access Prometheus dashbaord
    export LOCAL_GRAFANA_PORT=3000
    
    ### SOAK TEST RUNNER CONFIGURATION ###
    
    # The public ECR repository URI where your soak test runner image will be stored.
    export SOAK_IMAGE_REPO=<repository-to-store-soak-test-runner-image>
    
    # Image tag for soak-test-runner image.
    export SOAK_IMAGE_TAG=0.0.1
    
    # Release name of soak-test-runner helm chart.
    export SOAK_RUNNER_CHART_RELEASE_NAME=soak-test-runner
    
    # Total test duration is calculated as sum of TEST_DURATION_MINUTES,
    # TEST_DURATION_HOURS and TEST_DURATION_DAYS after converting them in
    # minutes. Override following variables accordingly to set your soak test
    # duration. Default value: 24 hrs.
    export TEST_DURATION_DAYS=1
    export TEST_DURATION_HOURS=0
    export TEST_DURATION_MINUTES=0
    export NET_SOAK_TEST_DURATION_MINUTES=$(($TEST_DURATION_MINUTES + $TEST_DURATION_HOURS*60 + $TEST_DURATION_DAYS*24*60))
    ``` 

* Initialize the shell with ACK soak test configuration

    ```
    source ~/.ack/soak/config
    ```

Validation:
* Running `echo $CONTROLLER_CHART_URL` should return non-empty result.

### Step 1 (Install the ACK service controller using helm)

In this step we install ACK service controller which will be soak tested using the soak-test runner created in step 4.

You can install an ACK controller following the documentation [here](https://aws-controllers-k8s.github.io/community/user-docs/install/)
but following commands are also TL;DR for the installation documentation.

* Pull the helm chart from either (a) or (b)

    a) from public repo

    ```bash
    go-to-soak \
    && mkdir controller-helm \
    && cd controller-helm \
    && helm pull --version $CONTROLLER_VERSION oci://$CONTROLLER_CHART_URL \
    && tar xvf $SERVICE_NAME-chart-$CONTROLLER_VERSION.tgz \
    && cd $SERVICE_NAME-chart
    ```
    
    b) from source repository
    
    ```bash
    go-to-soak \
    && git clone https://github.com/aws-controllers-k8s/$SERVICE_NAME-controller.git -b main --depth 1 \
    && cd $SERVICE_NAME-controller/helm
    ```

* Update values.yaml with overrides
    ```bash
    yq e $SERVICE_ACCOUNT_ANNOTATION_EVAL -i values.yaml \
    && yq e $METRIC_SERVICE_CREATE_EVAL -i values.yaml \
    && yq e $METRIC_SERVICE_TYPE_EVAL -i values.yaml \
    && yq e $AWS_REGION_EVAL -i values.yaml \
    && yq e $AWS_ACCOUNT_ID_EVAL -i values.yaml
    ```

* Run
    ```bash
    helm install --create-namespace -n ack-system $CONTROLLER_CHART_RELEASE_NAME .
    ```

NOTE
* IRSA setup is must for executing soak tests on ACK service controllers. See [installation docs](https://aws-controllers-k8s.github.io/community/user-docs/install/)
 for how to use IRSA for ACK service controller pod.

Validation:
* After successful execution of above commands, ack-service-controller will be running in your cluster, exposing ACK 
metrics through a K8s service endpoint.
* RUN `kubectl get -n ack-system service/$CONTROLLER_CHART_RELEASE_NAME-$SERVICE_NAME-chart-metrics` and result should be non-empty.


### Step 2 (Install "kube-prometheus" chart for monitoring the soak test behavior)

NOTE: You can skip this step if you have already installed 'Prometheus', 'Grafana' or other monitoring mechanism in your
K8s cluster where the soak tests are running.

The easiest way to install 'Prometheus', 'Grafana', 'Node Exporter' and 'Kube State Metrics' is through the official helm chart.
Follow the commands below to install this helm chart.

* Command
    ```bash
    go-to-soak \
    && jq -n --arg SERVICE_NAME $SERVICE_NAME '{prometheus: {prometheusSpec: { additionalScrapeConfigs: [{job_name: "ack_controller", static_configs:[{ targets: ["\($SERVICE_NAME)-controller-metrics.ack-system:8080"] }]}]}}}' | yq e -P > prometheus-values.yaml \
    && helm repo add prometheus-community https://prometheus-community.github.io/helm-charts \
    && helm install -f prometheus-values.yaml --create-namespace -n prometheus $PROM_CHART_RELEASE_NAME prometheus-community/kube-prometheus-stack
    ```

 * Start background processes to access `Prometheus` and `Grafana` using `kubectl port-forward`
    ```bash
    kubectl port-forward -n prometheus service/$PROM_CHART_RELEASE_NAME-kube-prometheus-prometheus $LOCAL_PROMETHEUS_PORT:9090 >/dev/null &
    ```
   
    ```bash
    kubectl port-forward -n prometheus service/$PROM_CHART_RELEASE_NAME-grafana $LOCAL_GRAFANA_PORT:3000 >/dev/null &
    ```

NOTE:
* Prometheus, Grafana services will be started as ClusterIP services and exposed on `localhost` using `kubectl port-forward`.
* The port-forward processes will run in the background and steps to clean them up are mentioned later in this guide.

Validation:
* Run following command:
    ```
    curl http://127.0.0.1:$LOCAL_PROMETHEUS_PORT >/dev/null 2>&1; [[ $? -eq 0 ]] && echo "Prometheus Successully started" || echo "Failed to start Prometheus." \
    && curl http://127.0.0.1:$LOCAL_GRAFANA_PORT >/dev/null 2>&1; [[ $? -eq 0 ]] && echo "Grafana Successully started" || echo "Failed to start Grafana."
    ```

NOTE: You can also choose to update Prometheus and Grafana services to NodePort or LoadBalancer Type service, if you wish
to access them through a public-facing ELB.

### Step 3 (Import ACK Grafana dashboard)
* Run following command to install the default ACK soak test dashboard
    ```bash
    kubectl apply -n prometheus -k github.com/aws-controllers-k8s/test-infra/soak/monitoring/grafana\?ref\=main
    ```

### Step 4 (Build the soak test runner image)

In this step we will build a container image which will execute the soak tests.
Dockerfile for this image is present in `soak` directory of "aws-controllers-k8s/test-infra" github repository.

This Dockerfile requires two arguments for building the soak-test container.

a) `AWS_SERVICE` -> Name of the AWS service under test

b) `E2E_GIT_REF` -> branch/tag/commit on service controller repository whose e2e tests will
be run multiple times to perform the soak test.

* Command: 
    ```bash
    go-to-soak \
    && git clone https://github.com/aws-controllers-k8s/test-infra.git -b main --depth 1 \
    && cd test-infra/soak \
    && docker build -t $SOAK_IMAGE_REPO:$SOAK_IMAGE_TAG --build-arg AWS_SERVICE=$SERVICE_NAME --build-arg E2E_GIT_REF=$CONTROLLER_VERSION . \
    && docker push $SOAK_IMAGE_REPO:$SOAK_IMAGE_TAG
    ```

### Step 5 (Install the helm chart which will run the soak tests against service controller)

* Command
    ```bash
    go-to-soak \
    && cd test-infra/soak/helm/ack-soak-test \
    && helm -n ack-system install $SOAK_RUNNER_CHART_RELEASE_NAME . \
    --set awsService=$SERVICE_NAME \
    --set soak.imageRepo=$SOAK_IMAGE_REPO \
    --set soak.imageTag=$SOAK_IMAGE_TAG \
    --set soak.startTimeEpochSeconds=$(date +%s) \
    --set soak.durationMinutes=$NET_SOAK_TEST_DURATION_MINUTES
    ```
            
* Above command will install the helm chart which will run your soak tests. You can now view live metrics on Grafana console
or check the test results later when soak tests are finished after "$NET_SOAK_TEST_DURATION_MINUTES" minutes.

NOTE:
* Currently soak-test job uses the same service account for running soak tests that is running the service controller.
* E2E tests that run as part of soak tests require aws credentials for test setup and validation steps of those tests.

Validation:
* After executing above command, a Kubernetes Job will start which will execute the soak tests until complete. 
* Run `kubectl get job/$SERVICE_NAME-soak-test`  and validate that the resource exists.

### Step 6 (Monitor the metrics using Grafana console)

* As mentioned in step 2, Prometheus and Grafana services are exposed through background port-forward processes.
* You can access the Grafana dashboard in your browser at `http://127.0.0.1:$LOCAL_GRAFANA_PORT` address. NOTE: Do not forget
to substitute "$LOCAL_GRAFANA_PORT" with actual value from your `~/.ack/soak/config` file
* When Grafana console is loaded, you can login with default username `admin` and default password `prom-operator`
NOTE: You can see the value of these credentials in secret named `$PROM_CHART_RELEASE_NAME-grafana`
* After the login, you can view your Grafana dashboard at `http://127.0.0.1:3000/dashboards`.
* The `ACK Dashboard` will show the ACK-specific request counts and error codes when making calls to AWS APIs.
* `Kubernetes/Compute Resources/Pod` dashboard will show the resource consumption by the controller pod


### Step 7 (Cleanup Soak test chart)

* RUN `helm -n ack-system uninstall $SOAK_RUNNER_CHART_RELEASE_NAME`

### Step 8 (Cleanup the service controller deployment)

* RUN `helm -n ack-system uninstall $CONTROLLER_CHART_RELEASE_NAME && helm delete namespace ack-system`

### Step 9 (Optional: Cleanup the kube-prometheus-stack chart) 

* RUN `helm -n prometheus uninstall $PROM_CHART_RELEASE_NAME && helm delete namespace prometheus`

### Step 10 (Cleanup the background port-forward processes)

* RUN 
    ```bash
    for pid in $(ps -a | grep "kubectl port-forward" | grep "$PROM_CHART_RELEASE_NAME" | cut -d" " -f1); do
        kill -9 $pid ;
    done
    ```