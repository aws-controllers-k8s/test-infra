### Introduction

This directory contains `auto-generate-controller.sh` script along with template
files that provide the content for creating github issues and pull requests using
ack-bot.


### How to add new AWS service for controller auto generation
To enable new services for auto generating controllers on new ACK code-generator
release,

1. Create a GitHub branch named `ack-bot-codegen` on `aws-controllers-k8s/$SERVICE-controller`
 repository.    
2. Create a custom label named `ack-bot-codegen` on `aws-controllers-k8s/$SERVICE-controller`
 repository.
3. Make sure `ack-bot` is collaborator on `aws-controllers-k8s/$SERVICE-controller`
 repository.
4. Add the service name in `aws-controllers-k8s/test-infra/prow/jobs/jobs_config.yaml`
5. Execute `make build-prow-jobs` on `aws-controllers-k8s/test-infra` repository.
6. Create a PR for `test-infra` repository , get it merged.
7. Next time an ACK code-generator release happens, the service controller will 
be auto generated.

### Gotchas
* `gh_issue_body_template.txt` & `gh_pr_body_template.txt` provide the body
content for GitHub issue and PR creation from `auto-generate-controller.sh`
script. Mark down is supported from these files but be careful about the variable
expansion since these files are evaluated in bash shell.
  > NOTE: Add backslash(\\) before back-tick(`) and '$' symbol to preserve them
  > inside GitHub issue/PR body.
