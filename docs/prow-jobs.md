# ACK Prow Jobs

## Summary

ACK uses [Prow jobs][prow-jobs] to define the CI and CD system for each of the
service controllers and common repositories. All of the job specifications are
located in the `prow/jobs` directory of the `test-infra` repository. See the 
`README` within that directory for building the list of jobs.

We break down jobs into "presubmit", "postsubmit" and "periodic" types. 
"Presubmit" jobs must pass before a PR can be merged and are triggered by an 
`/ok-to-test` command in the PR comments. "Postsubmit" jobs are triggered by the
merging of a pull request and are typically used for publishing artifacts.
"Periodic" jobs run on a set interval and are currently not used by ACK.

The container images we use for Prow jobs are located in the `prow/jobs/images`
directory. For information about how to build and release new versions of these
images, refer to that `README` file.

To add any new repositories to the CI/CD system, see the `README` file in
`prow/jobs`.

## Presubmit Jobs

There are currently two sets of presubmit jobs:

### Unit tests

Unit tests runs each of the [Golang testing][golang-testing] files and reports
back on any errors.

[golang-testing]: https://golang.org/pkg/cmd/go/internal/test/

### Integration tests

Prow runs integration tests in the same manner as defined in the
[community testing documentation][testing-docs]. That is, each of the tests will
create a new KIND cluster, generate temporary credentials from a test IAM role 
and run the Python e2e tests against this cluster. However, because Prow jobs
run inside a Kubernetes cluster, we have to configure KIND and IAM especially to 
work inside a container environment.

[testing-docs]: https://aws-controllers-k8s.github.io/community/dev-docs/testing/

The Dockerfile we use for integration tests is
`prow/jobs/images/Dockerfile.test`. It includes all the command line tools 
required to run our bootstrapping and testing scripts. The image also includes a
full version of the Docker engine, which serves as the container runtime for
KIND and our Python e2e test container. The entrypoint for the image is a shell
script that optionally enables the Docker engine and assumes the appropriate
test role within the pod. For more information on the IAM pathway, see the
[iam-structure](iam-structure.md) document.

The Prow job configured for each service includes labels for enabling 
Docker-in-Docker support and to enable mounting the appropriate cgroup 
directories from the host instance (to support KIND clusters). It also ensures
that each job pod uses the `pre-submit-service-account` k8s service account, 
which has permissions to assume the test IAM role.

## Postsubmit Jobs

Currently, the only postsubmit job we manage with Prow is to publish any new
versions of the service controllers into the [ECR public repository][ecr-repo]
and into the [Helm chart repository][helm-repo].

The Dockerfile we use for continuous deployment is
`prow/jobs/images/Dockerfile.deploy`. It is based off `buildah`, which we use to
construct OCI-compliant images from inside the Prow container.

[prow-jobs]: https://github.com/kubernetes/test-infra/blob/master/config/jobs/README.md
[ecr-repo]: https://gallery.ecr.aws/aws-controllers-k8s/controller
[helm-repo]: https://gallery.ecr.aws/aws-controllers-k8s/chart