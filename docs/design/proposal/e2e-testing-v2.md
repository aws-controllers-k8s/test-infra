# ACK e2e testing v2

## Goal

Increasing confidence in the release quality of core ACK libraries ([code-generator](https://github.com/aws-controllers-k8s/code-generator), [runtime](https://github.com/aws-controllers-k8s/runtime)) and controllers.

## Proposed requirements

### High-priority maintenance work

* [dockershim goes away in k8s v1.24](https://kubernetes.io/blog/2022/01/07/kubernetes-is-moving-on-from-dockershim) — our tests currently rely heavily on Docker and *we need our testing framework to be supported to k8s v1.24 and beyond*

### Individual controller testing (expanding on the existing e2e testing framework)

1. e2e tests that can be written as declarative YAML test files
  * A list of steps in a YAML file - each step gets some configuration
  * The only Python code that implementers need to write is glue for how to connect to their service
  * Standardizes the way tests are written — potentially improves the quality of e2e tests and reduces amount of  time required for implementing and reviewing test code

### Multi-controller testing framework (**new**)

1. The ability to test the following common run-time features with n >= 1 controllers:
  * [AdoptedResource](https://aws-controllers-k8s.github.io/community/reference/common/v1alpha1/adoptedresource/)
  * [FieldExport](https://aws-controllers-k8s.github.io/community/reference/common/v1alpha1/fieldexport/)
  * [DeletionPolicy](https://aws-controllers-k8s.github.io/community/docs/user-docs/deletion-policy/)
  * [Cross-account resource management](https://aws-controllers-k8s.github.io/community/docs/user-docs/cross-account-resource-management/)
  * [Drift remediation](https://github.com/aws-controllers-k8s/community/issues/1367)
  * [Multi-version support](https://github.com/aws-controllers-k8s/community/issues/835) ([related documentation task](https://github.com/aws-controllers-k8s/community/issues/1432))
  * All CLI flag options
  * Possible stretch goals:
    * Cross-Region resource management
    * [metrics](https://github.com/aws-controllers-k8s/runtime/tree/main/pkg/metrics)
    * [tags](https://github.com/aws-controllers-k8s/runtime/tree/main/pkg/tags)
    * [conditions management](https://github.com/aws-controllers-k8s/runtime/blob/main/pkg/runtime/reconciler.go#L310)

2. The interaction of multiple controllers in a cluster
  * Cross-controller references - these are only applicable in certain concrete situations (e.g., EC2 referencing other services, IAM referencing EC2, MemoryDB referencing IAM, etc.)
  * Soak tests with multiple controllers in the same cluster