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

## Contributing

We welcome community contributions and pull requests.

See our [contribution guide](/CONTRIBUTING.md) for more information on how to
report issues, set up a development environment, and submit code.

We adhere to the [Amazon Open Source Code of Conduct][coc].

You can also learn more about our [Governance](/GOVERNANCE.md) structure.

[coc]: https://aws.github.io/code-of-conduct

## License

This project is [licensed](/LICENSE) under the Apache-2.0 License.
