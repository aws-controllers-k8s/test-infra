This directory contains scripts to help validate the changes in ACK core
libraries i.e. code-generator and runtime

* `generate-test-controller.sh` script regenerates a service controller, performs
unit tests, e2e tests and helm tests for the controller. This script runs as part
of presubmit prowjob for `code-generator` repository.