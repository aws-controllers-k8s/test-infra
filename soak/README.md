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
* A tool for building OCI images (docker, buildah etc..)
* A Kubernetes cluster to run soak tests
* helm 
* kubectl
* curl
* yq

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
    
    # IAM Role Arn which will provide privileges to service account running controller pod
    # This role will also provide access to the soak-test-runner for validating e2e test results
    export IAM_ROLE_ARN_FOR_IRSA=<"IAM Role ARN for providing AWS access to ack controller's service account">

    export HELM_EXPERIMENTAL_OCI=1
    
    # Semver version of the controller under test. Ex: v0.0.2
    export CONTROLLER_VERSION=<semver>
    export CONTROLLER_CHART_URL=public.ecr.aws/aws-controllers-k8s/chart:$SERVICE_NAME-$CONTROLLER_VERSION
    
    # AWS Region for ACK service controller
    export CONTROLLER_AWS_REGION=us-west-2
  
    # AWS Account Id for ACK service controller
    export CONTROLLER_AWS_ACCOUNT_ID=<aws-account-id>
    
    # Release name of controller helm chart.
    export CONTROLLER_CHART_RELEASE_NAME=soak-test
        
    # Evaluation string for yq tool for updating helm chart's values.yaml file
    export SERVICE_ACCOUNT_ANNOTATION_EVAL=".serviceAccount.annotations.\"eks.amazonaws.com/role-arn\" = \"$IAM_ROLE_ARN_FOR_IRSA\""
    export METRIC_SERVICE_CREATE_EVAL=".metrics.service.create = true"
    export METRIC_SERVICE_TYPE_EVAL=".metrics.service.type = \"ClusterIP\""
    export AWS_REGION_EVAL=".aws.region = \"$CONTROLLER_AWS_REGION\""
    export AWS_ACCOUNT_ID_EVAL=".aws.account_id = \"$CONTROLLER_AWS_ACCOUNT_ID\""
    
    ### PROMETHEUS, GRAFANA CONFIGURATION ###
    
    # Release name of kube-prometheus helm chart.
    export PROM_CHART_RELEASE_NAME=kube-prom
    
    # Local port to access Prometheus dashbaord
    export LOCAL_PROMETHEUS_PORT=9090
    
    # Local port to access Prometheus dashbaord
    export LOCAL_GRAFANA_PORT=3000
    
    ### SOAK TEST RUNNER CONFIGURATION ###
    
    # Image repository where soak test runner image will be stored. (This can be your personal repository.) Ex: 'my-ack-repo/soak-test'
    # Make sure this is a public repository.
    export SOAK_IMAGE_REPO=<repository-to-store-soak-test-runner-image>
    
    # Image tag for soak-test-runner image.
    export SOAK_IMAGE_TAG=0.0.1
    
    # Release name of soak-test-runner helm chart.
    export SOAK_RUNNER_CHART_RELEASE_NAME=soak-test-runner
    
    # Total test duration is calculated as sum of TEST_DURATION_MINUTES, TEST_DURATION_HOURS and TEST_DURATION_DAYS after converting them in minutes.
    # Override following variables accordingly to set your soak test duration. Default value: 24 hrs.
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

You can use either (a) or (b) method below.

* Pull the helm chart either from (a) or (b)

    a) from public repo

    ```bash
    go-to-soak \
    && helm chart pull $CONTROLLER_CHART_URL \
    && mkdir controller-helm \
    && cd controller-helm \
    && helm chart export $CONTROLLER_CHART_URL
    && cd ack-$SERVICE_NAME-controller
    ```
    
    b) from source repository
    
    ```bash
    go-to-soak \
    && git clone https://github.com/aws-controllers-k8s/$SERVICE_NAME-controller.git -b main --depth 1 \
    && cd $SERVICE_NAME-controller/helm
    ```

* Insert $IAM_ROLE_ARN_FOR_IRSA into values.yaml
    ```bash
    yq e $SERVICE_ACCOUNT_ANNOTATION_EVAL -i values.yaml \
    && yq e $METRIC_SERVICE_CREATE_EVAL -i values.yaml \
    && yq e $METRIC_SERVICE_TYPE_EVAL -i values.yaml \
    && yq e $AWS_REGION_EVAL -i values.yaml \
    && yq e $AWS_ACCOUNT_ID_EVAL -i values.yaml
    ```

* Run
    ```bash
    helm install $CONTROLLER_CHART_RELEASE_NAME .
    ```

NOTE
* IRSA setup is must for executing soak tests on ACK service controllers. See [installation docs](https://aws-controllers-k8s.github.io/community/user-docs/install/)
 for how to use IRSA for ACK service controller pod.

Validation:
* After successful execution of above commands, ack-service-controller will be running in your cluster, exposing ACK 
metrics through a K8s service endpoint.
* RUN `kubectl get service/$CONTROLLER_CHART_RELEASE_NAME-ack-$SERVICE_NAME-controller-metrics` and result should be non-empty.


### Step 2 (Install "kube-prometheus" chart for monitoring the soak test behavior)

NOTE: You can skip this step if you have already installed 'Prometheus', 'Grafana' or other monitoring mechanism in your
K8s cluster where the soak tests are running.

The easiest way to install 'Prometheus', 'Grafana', 'Node Exporter' and 'Kube State Metrics' is through the official helm chart.
Follow the commands below to install this helm chart.

* Command
    ```bash
    go-to-soak \
    && helm repo add prometheus-community https://prometheus-community.github.io/helm-charts \
    && helm install $PROM_CHART_RELEASE_NAME prometheus-community/kube-prometheus-stack
    ```

 * Start background processes to access `Prometheus` and `Grafana` using `kubectl port-forward`
    ```bash
    kubectl port-forward service/$PROM_CHART_RELEASE_NAME-kube-prometheus-prometheus $LOCAL_PROMETHEUS_PORT:9090 >/dev/null &
    ```
   
    ```bash
    kubectl port-forward service/$PROM_CHART_RELEASE_NAME-grafana $LOCAL_GRAFANA_PORT:80 >/dev/null &
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

### Step 3 (Update Prometheus scrape config to include ACK metrics)

If you only care about the resource metrics(CPU, Memory) of the pod running the ack-service-controller, and not about 
ACK metrics emitted from service controller, you can skip this step.

ACK metrics which are emitted include `ack_outbound_api_requests_total` and `ack_outbound_api_requests_error_total`

To include these metrics into your prometheus scraping config, follow the commands below. Main guide can be found 
[here](https://github.com/prometheus-operator/prometheus-operator/blob/master/Documentation/additional-scrape-config.md)

* Commands
    ```bash
    go-to-soak \
    && echo "- job_name: ack-controller\n  static_configs:\n  - targets: [$CONTROLLER_CHART_RELEASE_NAME-ack-$SERVICE_NAME-controller-metrics:8080]" > prometheus-additional.yaml \
    && kubectl create secret generic additional-scrape-configs --from-file=prometheus-additional.yaml --dry-run=client -oyaml > additional-scrape-configs.yaml \
    && kubectl create -f additional-scrape-configs.yaml \
    && kubectl get prometheus/$PROM_CHART_RELEASE_NAME-kube-prometheus-prometheus -oyaml > prometheus.yaml \
    && yq e '.spec.additionalScrapeConfigs.name = "additional-scrape-configs"' -i prometheus.yaml \
    && yq e '.spec.additionalScrapeConfigs.key = "prometheus-additional.yaml"' -i prometheus.yaml \
    && kubectl apply -f prometheus.yaml
    ```
  
Validation
* Wait for ~1 minute for scraping configuration to propagate to Prometheus server. 
* Command `curl -s http://127.0.0.1:$LOCAL_PROMETHEUS_PORT/api/v1/status/config | grep "ack-controller"` should return a match.

### Step 4 (Import ACK Grafana dashboard )
* Run following command to checkout aws-controllers-k8s/test-infra source repository
    ```bash
    go-to-soak \
    && git clone https://github.com/aws-controllers-k8s/test-infra.git -b main --depth 1 \
    && cd test-infra/soak/monitoring/grafana
    ```

* Using the credentials mentioned in step 7, log into Grafana console
    * Go to `http://127.0.0.1:3000/dashboards`
    * Under 'Manage' tab, click Import
    * Import the content from `ack-dashboard-source.json` file inside current directory.
    * After the soak test run, this Dashboard will show all ACK related requests and error metrics.

### Step 5 (Build the soak test runner image)

In this step we will build a container image which will execute the soak tests.
Dockerfile for this image is present in `soak` directory of "aws-controllers-k8s/test-infra" github repository.

This Dockerfile requires two arguments for building the soak-test container.

a) `AWS_SERVICE` -> Name of the AWS service under test

b) `E2E_GIT_REF` -> branch/tag/commit on service controller repository whose e2e tests will
be run multiple times to perform the soak test.

* Command: 
    ```bash
    go-to-soak \
    && cd test-infra/soak \
    && docker build -t $SOAK_IMAGE_REPO:$SOAK_IMAGE_TAG --build-arg AWS_SERVICE=$SERVICE_NAME --build-arg E2E_GIT_REF=$CONTROLLER_VERSION .
    && docker push $SOAK_IMAGE_REPO:$SOAK_IMAGE_TAG
    ```

### Step 6 (Install the helm chart which will run the soak tests against service controller)

* Command
    ```bash
    go-to-soak \
    && cd test-infra/soak/helm/ack-soak-test \
    && helm install $SOAK_RUNNER_CHART_RELEASE_NAME . \
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

### Step 7 (Monitor the metrics using Grafana console)

* As mentioned in step 2, Prometheus and Grafana services are exposed through background port-forward processes.
* You can access the Grafana dashboard in your browser at `http://127.0.0.1:$LOCAL_GRAFANA_PORT` address. NOTE: Do not forget
to substitute "$LOCAL_GRAFANA_PORT" with actual value from your `~/.ack/soak/config` file
* When Grafana console is loaded, you can login with default username `admin` and default password `prom-operator`
NOTE: You can see the value of these credentials in secret named `$PROM_CHART_RELEASE_NAME-grafana`
* After the login, you can view your Grafana dashboard at `http://127.0.0.1:3000/dashboards`.
* `Kubernetes/Compute Resources/Pod` dashboard will show the resource consumption by the controller pod
* The dashboard imported in step 4 will show the ACK dashboard with request counts and error codes when making calls to AWS APIs.


### Step 8 (Cleanup Soak test chart)

* RUN `helm uninstall $SOAK_RUNNER_CHART_RELEASE_NAME`


### Step 9 (Cleanup the service controller deployment)

* RUN `helm uninstall $CONTROLLER_CHART_RELEASE_NAME`


### Step 10 (Optional: Cleanup the kube-prometheus-stack chart) 

* RUN `helm uninstall $PROM_CHART_RELEASE_NAME`
* If you executed step3, remove the secret which was created for additional prometheus configuration.
    * `kubectl delete -f additional-scrape-configs.yaml`

### Step 11 (Cleanup the background port-forward processes)

* 