## Introduction

There is currently no automated process to trigger *aws-sdk-go* dependency
updates in ACK core libraries and controllers.

* **ACK runtime** depends on aws-sdk-go to find *AWS AccountID* and assuming IAM
role for **cross-account-resource-management** feature
* **ACK code-generator** depends on aws-sdk-go for generating K8s custom resources
by reading the AWS API models
* **ACK service controllers** depend on aws-sdk-go to perform CRUD operations
  using AWS API calls
  
## Background
Before proposing new automation, it is important to understand how aws-sdk-go
upgrades are handled in ACK repositories currently.

#### Current steps
* The ACK runtime first gets updated with the newer version of aws-sdk-go and
  a new patch release is made for ACK runtime.
* Then ACK code-generator is updated with new ACK runtime release. Because of
  transitive dependency the minimum required version of aws-sdk-go in 
  code-generator gets updated with version in latest ACK runtime
  release.
* Then a new code-generator release is made, which triggers ACK service 
  controller auto generation.
* The [`auto-generate-controllers.sh`](https://github.com/aws-controllers-k8s/test-infra/blob/main/cd/auto-generate/auto-generate-controllers.sh)
  updates the go.mod file in the service controller's source repository root
  directory with updated versions of the ACK runtime and minimum aws-sdk-go
  package version.
* ACK code-generator uses the aws-sdk-go present in
  the service controller go.mod file to generate new service controller.

While mostly automated, the current process does not cover automatically
updating the aws-sdk-go package in ACK runtime's go.mod file when
new versions of aws-sdk-go are released.

#### aws-sdk-go Releases
* aws-sdk-go **v1** has two kinds of releases. **Minor version updates** which
mainly reflect the changes in **SDK Features**(enabling FIPS endpoints) and
**Patch version updates** which mainly reflect **Service Client Updates**(updates
in specific AWS service API + documentation). See this
[v1.42.0 changelog](https://github.com/aws/aws-sdk-go/releases/tag/v1.42.0) as an example

## Scope
This documentation will focus on automating the updates for aws-sdk-go minor version
upgrades. Minor version upgrades will be done starting from ACK runtime to code-generator
and then to all service-controllers using existing automation.

Another [document](./aws-sdk-patch-version-upgrades.md) will focus on how to upgrade
individual service controllers for the patch releases because Patch releases will only
impact specific service controllers not the ACK runtime behavior (except IAM service
changes)

## High Level Overview

The automation will be divided in following sub-parts

### Trigger
Trigger is responsible for deciding when to upgrade ACK core libraries and service
controllers with the new aws-sdk-go version. The trigger can be event-driven(tracking
a new release of aws-sdk-go using webhooks or other methods) or periodic.

### Verification
Verification is responsible for providing the preview of changes caused by
aws-sdk-go upgrade. Example: Which controller(s) build successfully with the
new aws-sdk-go version? Does a new resource/ new fields gets added as part
of controller build? etc...

### Release
The actual process of upgrading the go.mod files with latest aws-sdk-go version
and raising PR.

## Implementation

### Trigger
For the trigger, the periodic job option should be preferred that will
run every day. 24 hours frequency is good enough to not delay verifying
the new aws-sdk-go release. Since immediate aws-sdk-go upgrades are not
the requirement, the Trigger does not need to handle complexity of being
event driven.

In the periodic job mentioned above, the ENTRYPOINT script will filter any
new aws-sdk-go release for minor-version-upgrades and run the following
"Verification" step. 

aws-sdk-go patch releases will be ignored because ACK runtime does not need
to be updated for every aws-sdk-go patch release(only *SDK Feature*
updates and IAM service changes impact ACK runtime functionality).

### Verification
This will be the major part of this new automation. This step will include
testing all the service controllers locally with new aws-sdk-go release.
The testing will include updating the dependency, generating new controller
source code, compiling and publishing a report of the upgrade. 
The report will include details like,
* Which controllers were successfully built and unit tested
* Which controllers failed to build and compile 
* The summary of files added/changed for each successful controller. This
will give an idea where new resources/fields were added in new aws-sdk-go
release

#### Setup
There will be a new periodic prowjob which will run once every 24 hours.
This job will checkout all the service controller repos, along with latest
ACK runtime release and corresponding code-generator release.

> NOTE: We will not checkout `main` branch of ACK core libraries because this
job should only validate the changes caused by aws-sdk-go upgrades for
the latest controller release

#### Controller Build
* For the new aws-sdk-go minor-version-upgrade release, update the ACK runtime
locally and validate that it compiles successfully
* Update the ACK code-generator `go.mod` file to replace ACK runtime
dependency with local ACK runtime repo and validate that it compiles
successfully. (The ACK runtime update will bring new aws-sdk-go dependency)
* Perform similar go.mod update for all service controllers and execute
`make build-controller` target for each service controller.
* Perform `make test` target for each controller to validate that newly
generated controller compiles successfully.

#### Reporting Results
* This step can be started as minimal reporting using `STDOUT` at the end of
prowjob. As the number of service controllers increase, the result can be
condensed into a separate summary file and individual results can be exported
into separate files per controller and stored in a S3 bucket.
* For every aws-sdk-go release upgrade, There will be auto created GitHub issue
to track the actual release and these artifacts will be linked in that issue.
* In the future, if needed, we can have a static webpage that can help service teams
and external contributors browse these artifacts before the upgrade.

### Release
With the automation in place for **auto-generating** service controllers, this step can
be started with some manual effort and completely automated with future enhancements.

Currently, after reviewing the artifacts of "Verification" step from the GitHub
issue, ACK core team member can just update the aws-sdk-go version manually in
the ACK runtime repo and follow the existing release process, which will auto-generate
new service controllers.

In future enhancements, the Verification step will also generate the Pull Request with
new aws-sdk-go update for ACK runtime. Creating new ACK runtime patch release, updating
code-generator and creating new code-generator release will also be automated when runtime
PR gets merged.


### Monitoring
* When this job fails ACK core team should be updated by some mechanism.
* Currently, ACK has no alerting mechanism for ACK prow job failures. Setting up
alerts for ACK prow job failures will be handled as separate issue and not as part of
this proposal.
* Monitoring for this job will get covered as part of aforementioned alerting task.
