# ACK `prow` test infrastructure

## Overview

The ACK project uses an EKS cluster running [`Prow`](https://github.com/kubernetes/test-infra/tree/master/prow) to manage each of the service controller’s CI and CD workflows, as well as to run unit and integration tests on common code (such as runtime and code-generator). This document outlines the systems configured to deploy and maintain this cluster and the deployments running within it. Access to the cluster directly is prohibited to anyone outside the core ACK contributor team, although service teams may work with this team to add additional services in the future.

In general, the system is managed by a CDK template which is used to initialize the EKS cluster, installs Flux2 and creates a number of secrets. After the CDK is deployed, Flux2 is in charge of managing any additional deployments into the cluster. The CDK template is assumed to be run manually by a member of the ACK team with access to the infrastructure AWS account and that has all values to be passed into the corresponding secrets. 

The EKS cluster that is deployed is the default as provided by CDK, which should use the latest EKS release version and 2 managed nodegroups.

## Flux2

The test infrastructure uses Flux2 to manage the deployment of Helm charts and Kustomization manifests into the testing cluster. After the initial installation of Flux2, CDK will also install a master manifest that points back into the `test-infra` repository and will add any charts from the `flux/` directory.
To add any additional deployments into the cluster, they should be added to a new application under `flux/` and should be automatically consumed by the cluster when merged.

Flux currently syncs the following charts:

* `prow-config`: A custom Helm chart containing Prow and its associated configuration files (found in `prow/config/`)
* `prow-jobs`: A Kustomize template containing the ACK-specific Prow jobs configured to run for each of our repositories (found in `prow/jobs/`)
* `prow-data-plane`: A custom Helm chart containing the roles and configuration to be installed into the pod namespace for the prow job pods (found in `prow/data-plane`)

## Prow

The ACK team has to manage our own Helm chart for Prow because a) Prow does not have an official Helm repository and b) the [manifest that Prow provides for bootstrapping](https://github.com/kubernetes/test-infra/blob/2ae6e67a50abc7f4ef757b1f0271d31d53108ca7/config/prow/cluster/starter-s3.yaml) is (by-design) invalid if applied directly. Therefore, we have split the provided starter manifest into a series of Helm templates that are modified specifically for our testing cluster needs. This custom Helm chart is located in `test-infra/prow/config`. 

Some of the changes we have made to the manifests:

* All of the secrets have been removed from the manifest. They are now installed when we create the cluster in CDK.
* Added the `--job-config-path` container argument and associated configmap volume mounts to each of the deployments. This allows us to load the jobs from a separate configmap than the rest of the configuration.

## Prow Jobs

The [Prow jobs](https://github.com/kubernetes/test-infra/blob/master/config/jobs/README.md) are managed in a separate directory from the Prow configuration so that they may be controlled and versioned without the risk of interfering with the service directly. Prow jobs are configured through a config map which is compiled using Kustomize’s `configMapGenerator` method. This Kustomization specification is located in `test-infra/prow/jobs`. Currently all jobs are specified in a single `jobs.yaml` file, but in the future it would be preferable to spread them into individual files grouped either by test type or by repository.

For more information regarding the building and specification of Prow jobs, see [prow-jobs.md](prow-jobs.md)