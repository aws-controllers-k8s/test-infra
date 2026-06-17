# AWS Controllers for Kubernetes Test Infrastructure

This repository contains a framework for functional integration (e2e) testing
of AWS Controllers for Kubernetes (ACK) service controllers.

Please [log issues][ack-issues] and feedback on the main AWS Controllers for
Kubernetes Github project.

[ack-issues]: https://github.com/aws-controllers-k8s/community/issues

Get started by [setting up your local development environment][setup] for ACK
`test-infra`.

[setup]: /docs/setup.md

## Getting Started

To provide re-usable common functionality to each service's integration test 
suite, we provide the `acktest` Python module. This module contains 
methods and classes for accessing aws and k8s resources, and for bootstrapping 
common test prerequisites.

The common test module can be installed using `pip` through using the following
command:
```bash
pip install git+https://github.com/aws-controllers-k8s/test-infra.git@main
```

Once installed, methods and classes are accessed by referencing the
`acktest`:
```python
import acktest
```

## Upgrading EKS Addons

The test clusters use EKS managed addons (secrets-store-csi-driver, external-dns, coredns) defined in `flux/ack/cluster/addons/addons.yaml`. To upgrade the `aws-secrets-store-csi-driver-provider` addon to the latest version:

```bash
# Auto-detect and apply the latest version
./scripts/upgrade-secrets-csi-driver.sh

# Preview changes without modifying files
./scripts/upgrade-secrets-csi-driver.sh --dry-run

# Pin to a specific version
./scripts/upgrade-secrets-csi-driver.sh v1.0.0-eksbuild.1

# Constrain to a specific Kubernetes version
./scripts/upgrade-secrets-csi-driver.sh --kubernetes-version=1.31
```

The script uses `aws eks describe-addon-versions` to discover the latest compatible version and updates the `addonVersion` field in the Addon manifest. It also updates the `test-infra-upgrade` sibling repo if present.

**Prerequisites:** AWS CLI (configured with EKS access), [yq](https://github.com/mikefarah/yq)

## Contributing

We welcome community contributions and pull requests.

See our [contribution guide](/CONTRIBUTING.md) for more information on how to
report issues, set up a development environment, and submit code.

We adhere to the [Amazon Open Source Code of Conduct][coc].

You can also learn more about our [Governance](/GOVERNANCE.md) structure.

[coc]: https://aws.github.io/code-of-conduct

## License

This project is [licensed](/LICENSE) under the Apache-2.0 License.
