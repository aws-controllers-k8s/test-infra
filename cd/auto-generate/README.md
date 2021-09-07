### Introduction

This directory contains `auto-generate-controllers.sh` script along with template
files that provide the content for creating github issues and pull requests using
ack-bot.


### How to add new AWS service for controller auto generation
To enable new services for auto generating controllers on new ACK code-generator
release,

1. Create a custom label named `ack-bot-autogen` on `aws-controllers-k8s/$SERVICE-controller`
 repository.
2. Make sure `ack-bot` is collaborator on `aws-controllers-k8s/$SERVICE-controller`
 repository. See ["Configure ack-bot access"](https://github.com/aws-controllers-k8s/test-infra/blob/main/docs/onboarding.md#1-configure-ack-bot-access)
3. Add the service name in `aws-controllers-k8s/test-infra/prow/jobs/jobs_config.yaml`
4. Execute `make build-prow-jobs` on `aws-controllers-k8s/test-infra` repository.
5. Create a PR for `test-infra` repository , get it merged.
6. Next time an ACK code-generator release happens, the service controller will 
be auto generated.

### Gotchas
* `gh_issue_body_template.txt` & `gh_pr_body_template.txt` provide the body
content for GitHub issue and PR creation from `auto-generate-controllers.sh`
script. Mark down is supported from these files but be careful about the variable
expansion since these files are evaluated in bash shell.
  > NOTE: Add backslash(\\) before back-tick(`) and '$' symbol to preserve them
  > inside GitHub issue/PR body.
