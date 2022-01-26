## Introduction

There is currently no automated process to trigger `aws-sdk-go` dependency
updates in ACK core libraries and controllers.

* ACK runtime depends on `aws-sdk-go` to find `AWS AccountID` and assuming iam
  role for cross-account resource management
* ACK code-generator depends on `aws-sdk-go` for generating K8s resource by
  reading the AWS API models
* ACK service controllers depend on `aws-sdk-go` to perform CRUD operations
  using AWS API calls
  
## Background
Before proposing new automation, it is important to understand how `aws-sdk-go`
upgrades are handled in ACK repositories currently.
#### Current steps
* At first ACK `runtime` gets updated with the newer version of `aws-sdk-go` and
  a new patch release is made for ACK `runtime`.
* Then ACK `code-generator` is updated with new ACK runtime release. Because of
  transitive dependency, the minimum required version of `aws-sdk-go` in 
  code-generator gets updated to `aws-sdk-go` version in latest ACK runtime
  release.
* Then a new code-generator release is made, which triggers ACK service 
  controller auto generation.
* Controller auto-generation script updates the ACK `runtime` version in each 
  controller to new ACK `runtime` version.
* The update to new ACK `runtime` in service controller repositories updates the
  minimum `aws-sdk-go` version in service controller `go.mod` file.
* During auto-generation, ACK `code-generator` uses the `aws-sdk-go` present in
  the service controller `go.mod` file to generate new service controller.

The current process is automated for most parts but it misses following important
parts:

1. No upgrade trigger based on new `aws-sdk-go` releases
2. No preview into the controller build. Currently ACK engineers have to manually
   build cherry-picked controllers locally to see if new `aws-sdk-go` version
   causes any regression. This is neither exhaustive nor automated.

## High Level Overview

The automation will be divided in following sub-parts

### Trigger
Trigger is responsible for when to upgrade ACK core libraries and service controllers
with the new `aws-sdk-go` version.

The trigger can be `event-driven` i.e. tracking a new release of `aws-sdk-go` using
webhooks or other methods. 

The trigger can also be `periodic`, where a periodic job constantly checks for the most
recent release of `aws-sdk-go`, compares it with `aws-sdk-go` version in the ACK `runtime`.
If most recent release is not same as ACK `runtime` dependency, then the automation
gets triggered.

### Verification
This is where the automation verifies the new release and provide a preview of what
the new changes will be for each repository. Example: Does a controller build successfully
with the new `aws-sdk-go` version? Does a new resource/ new fields gets added as part
of controller build? etc...

### Release
The actual process of upgrading the `go.mod` files with latest `aws-sdk-go` version
and raising PRs.

## Implementation Details

### Trigger
For the trigger, the periodic job option should be preferred that will
run every day. 24 hours frequency is good enough to not delay verifying
the new `aws-sdk-go` release. 

Since immidiate `aws-sdk-go` upgrades are not the requirement, the
Trigger does not need to handle complexity of being event driven.

### Verification
This will be the major part of this new automation. This step will include
testing all the service controllers locally with new `aws-sdk-go` release.
The testing will include updating the dependency, generating new controller
source code, compiling and publishing a report of the upgrade. 
The report will include details like,
* Which controllers were successfully built and unit tested
* Which controllers failed to build and compile 
* The summary of files added/changed for each successful controller. This
will give an idea where new resources/fields were added in new `aws-sdk-go`
release

###### Setup
There will be a new `periodic` prowjob, which will run once every 24 hours.
This job will checkout all the service controller repos, along with latest
ACK `runtime` release and corresponding `code-generator` release.

> NOTE: We will not checkout `main` branch of ACK core libraries because this
job should only validate the changes caused by `aws-sdk-go` upgrades for
the latest controller release

###### Controller Build
* In the above mentioned `periodic` job, the ENTRYPOINT script will first
validate whether a new `aws-sdk-go` release is made, since the last ACK
`runtime` release.
* If there is a new `aws-sdk-go` release present, update the ACK `runtime`
locally and validate that it compiles successfully
* Update the ACK `code-generator` `go.mod` file to replace ACK `runtime`
dependency with local ACK `runtime` repo and validate that it compiles
successfully. (The ACK `runtime` update will bring new `aws-sdk-go` dependency)
* Perform similar `go.mod` update for all service controllers and execute
`make build-controller` target for each service controller.
* Perform `make test` target for each controller to validate that newly
generated controller compiles successfully.

###### Reporting Results
* This step can be started as minimal reporting using `STDOUT` at the end of
prowjob. As the number of service controllers increase, the result can be
condensed into a separate summary file and individual results can be exported
into separate files per controller and stored in a S3 bucket.
* For every `aws-sdk-go` release upgrade, There will be auto created GitHub issue
to track the actual release and these artifacts will be linked in that issue.
* In the future, if needed, we can have a static webpage that can help service teams
and external contributors browse these artifacts for every upgrade.

### Release
With the automation is place for `auto-generating` controllers already, this step can
be started with some manual effort and completely automated with future enhancements.

Currently, after reviewing the artifacts of `Verification` step from the GitHub
issue, ACK core team member can just update the `aws-sdk-go` version manually in
the ACK `runtime` repo and follow the existing release process, which will `auto-generate`
new service controllers.

In further enhancements, the `Verification` step will also generate the `PR` with new
`aws-sdk-go` update. Creating new ACK `runtime` patch release, updating `code-generator`
and creating new `code-generator` release will also be automated when `runtime` PR with
`aws-sdk-go` upgrade gets merged.


### Monitoring
* Since this is one of the important jobs, when this job fails ACK core team should be
updated by some mechanism.
* Currently, ACK has no alerting mechanism for ACK prow job failures. Setting up
alerts for ACK prow job failures will be handled as separate issue and not as part of
this proposal.
* Monitoring for this job will get covered as part of aforementioned alerting task.
