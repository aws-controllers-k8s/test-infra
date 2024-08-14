# default: global configuration to be applied to all repos
# repos: list of repos with specific configuration to be applied in addition to default
#   labels: list of labels - keys for each item: color, description, name, target, deleteAfter, previously
#     deleteAfter: 2006-01-02T15:04:05Z (rfc3339)
#     previously: list of previous labels (color name deleteAfter, previously)
#     target: one of issues, prs, or both (also TBD)
#     addedBy: human? prow plugin? other?
---
default:
  labels:
    # Area labels
    - color: 0052cc
      description: Issues or PRs related to ACK Adopted Resources
      name: area/adopted-resource
      target: both
      addedBy: label
      previously:
        - name: AdoptedResource
    - color: 0052cc
      description: Issues or PRs related to AWS APIs
      name: area/api/aws
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to kubernetes APIs
      name: area/api/k8s
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs as related to controllers or docs code generation
      name: area/code-generation
      target: both
      previously:
        - name: code generator
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to community meetings and notes
      name: area/community-meetings
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to crossplane
      name: area/crossplane
      previously:
        - name: Crossplane
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to CARM (Cross Account Resource Management)
      name: area/carm
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to ACK metrics
      name: area/metrics
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to scaling ACK.
      name: area/scaling
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to deletion policy.
      name: area/deletion-policy
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to dependency changes
      name: area/dependency
      target: both
      previously:
        - name: dependencies
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to drift remediation.
      name: area/drift-remediation
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to documentation and examples
      name: area/documentation
      target: both
      addedBy: label
      previously:
        - name: documentation
    - color: 0052cc
      description: Issues or PRs related to Field Export
      name: area/field-export
      target: both
      previously:
        - name: FieldExport
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to Helm charts
      name: area/helm
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs as related to our testing infrastructure, prow/flux configuration etc...
      name: area/infra
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to our installation tools, configurations, helm charts, etc...
      name: area/installation
      target: both
      previously:
        - name: install-setup
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to Prow
      name: area/prow
      target: both
      addedBy: label
      previously:
        - name: prow
    - color: 0052cc
      description: Issues or PRs related to multi version support
      name: area/multi-version
      target: both
      previously:
        - name: multi-versioning
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to resource references
      name: area/resource-references
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs as related to controller runtime, common reconciliation logic, etc
      name: area/runtime
      target: both
      previously:
        - name: runtime
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to security topics
      name: area/security
      target: both
      addedBy: label
    - color: 0052cc
      description: Issues or PRs related to testing
      name: area/testing
      target: both
      addedBy: label
    
    # Prow related labels
    - color: 2C3248
      description: PRs related to prow auto generation automation
      name: prow/auto-gen
      target: both
      addedBy: label
    - color: 2C3248
      description: Issues related to prow olm automation
      name: prow/olm
      target: both
      addedBy: label

    # Triage labels
    - color: 8fc951
      description: Indicates an issue or PR is ready to be actively worked on.
      name: triage/accepted
      target: both
      prowPlugin: label
      addedBy: org members
    - color: d455d0
      description: Indicates an issue is a duplicate of other open issue.
      name: triage/duplicate
      target: both
      addedBy: humans
    - color: d455d0
      description: Indicates an issue needs more information in order to work on it.
      name: triage/needs-information
      target: both
      addedBy: humans
    - color: d455d0
      description: Indicates an issue can not be reproduced as described.
      name: triage/not-reproducible
      target: both
      addedBy: humans
    - color: d455d0
      description: Indicates an issue that can not or will not be resolved.
      name: triage/unresolved
      target: both
      addedBy: humans

    # Do not merge labels
    - color: e11d21
      description: DEPRECATED. Indicates that a PR should not merge. Label can only be manually applied/removed.
      name: do-not-merge
      target: prs
      addedBy: humans
    - color: e11d21
      description: Indicates that a PR should not merge because someone has issued a /hold command.
      name: do-not-merge/hold
      target: prs
      prowPlugin: hold
      addedBy: anyone
    - color: e11d21
      description: Indicates that a PR should not merge because it has an invalid commit message.
      name: do-not-merge/invalid-commit-message
      target: prs
      prowPlugin: invalidcommitmsg #TODO(a-hilaly): Add invalidcommitmsg plugin
      addedBy: prow
    - color: e11d21
      description: Indicates that a PR should not merge because it has an invalid OWNERS file in it.
      name: do-not-merge/invalid-owners-file
      target: prs
      prowPlugin: verify-owners
      addedBy: prow
    - color: e11d21
      description: Indicates that a PR should not merge because it is a work in progress.
      name: do-not-merge/work-in-progress
      target: prs
      prowPlugin: wip
      addedBy: prow

      # Contribution labels
    - color: 7057ff
      description: Denotes an issue ready for a new contributor, according to the "help wanted" guidelines.
      name: 'good first issue'
      target: issues
      prowPlugin: help
      addedBy: anyone
    - color: 006b75
      description: Denotes an issue that needs help from a contributor. Must meet "help wanted" guidelines.
      name: 'help wanted'
      previously:
        - name: help-wanted
      target: issues
      prowPlugin: help
      addedBy: anyone

    # Kind labels
    - color: e11d21
      description: Categorizes issue or PR as related to adding, removing, or otherwise changing an API
      name: kind/api-change
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: e11d21
      description: Categorizes issue or PR as related to a bug.
      name: kind/bug
      previously:
        - name: bug
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: c7def8
      description: Categorizes issue or PR as related to cleaning up code, process, or technical debt.
      name: kind/cleanup
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: e11d21
      description: Categorizes issue or PR as related to CVE.
      name: kind/cve
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: e11d21
      description: Categorizes issue or PR as related to a feature/enhancement marked for deprecation.
      name: kind/deprecation
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: c7def8
      description: Categorizes issue or PR as related to a technical design.
      name: kind/design
      target: both
      prowPlugin: label
      previously:
        - name: design
      addedBy: anyone
    - color: c7def8
      description: Categorizes issue or PR as related to documentation.
      name: kind/documentation
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: c7def8
      description: Categorizes issue or PR as related to existing feature enhancements.
      name: kind/enhancement
      target: both
      prowPlugin: label
      previously:
        - name: enhancement
      addedBy: anyone
    - color: c7def8
      description: Categorizes issue or PR as related to a new feature.
      name: kind/feature
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: c2e0c6
      description: Categorizes issue or PR as related to a new resource.
      name: kind/new-resource
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: 59bbaa
      description: Categorizes issue or PR as related to a new service.
      name: kind/new-service
      target: both
      prowPlugin: label
      previously:
        - name: Service Controller
      addedBy: anyone
    - color: e11d21
      description: Categorizes issue or PR as related to a regression from a prior release.
      name: kind/regression
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: d455d0
      description: Categorizes issue or PR as a support question.
      name: kind/support
      target: both
      addedBy: humans
    - color: f7c6c7
      description: Categorizes issue or PR as related to a consistently or frequently failing test.
      name: kind/tests/failing
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: f7c6c7
      description: Categorizes issue or PR as related to a flaky test.
      name: kind/tests/flaky
      target: both
      prowPlugin: label
      addedBy: anyone
  
      # Lifecycle labels
    - color: d3e2f0
      description: Indicates that an issue or PR should not be auto-closed due to staleness.
      name: lifecycle/frozen
      target: both
      prowPlugin: lifecycle
      addedBy: anyone
    - color: 8fc951
      description: Indicates that an issue or PR is actively being worked on by a contributor.
      name: lifecycle/active
      target: both
      prowPlugin: lifecycle
      addedBy: anyone
    - color: "604460"
      description: Denotes an issue or PR that has aged beyond stale and will be auto-closed.
      name: lifecycle/rotten
      target: both
      prowPlugin: lifecycle
      addedBy: anyone
    - color: "795548"
      description: Denotes an issue or PR has remained open with no activity and has become stale.
      name: lifecycle/stale
      target: both
      prowPlugin: lifecycle
      addedBy: anyone

    # Need-X labels
    - color: ededed
      description: Indicates a PR lacks a `kind/foo` label and requires one.
      name: needs-kind
      target: prs
      prowPlugin: require-matching-label
      addedBy: prow
    - color: e11d21
      description: Indicates an issue needs some investigation.
      name: needs-investigation
      target: issues
      addedBy: anyone
    - color: b60205
      # This is to prevent spam/abuse of our CI system, and can be circumvented by becoming an 
      # org member. Org members can remove this label with the `/ok-to-test` command.
      description: Indicates a PR that requires an org member to verify it is safe to test.
      name: needs-ok-to-test
      target: prs
      prowPlugin: trigger
      addedBy: prow
    - color: e11d21
      description: Indicates a PR cannot be merged because it has merge conflicts with HEAD.
      name: needs-rebase
      target: prs
      prowPlugin: needs-rebase
      isExternalPlugin: true
      addedBy: prow
    - color: ededed
      description: Indicates an issue or PR lacks a `triage/foo` label and requires one.
      name: needs-triage
      target: both
      prowPlugin: require-matching-label
      addedBy: prow

    # Priority labels
    - color: fef2c0
      # These are mostly place-holders for potentially good ideas, so that they don't get completely
      # forgotten, and can be referenced /deduped every time they come up.
      description: Lowest priority. Possibly useful, but not yet enough support to actually get it done.
      name: priority/awaiting-more-evidence
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: fbca04
      # There appears to be general agreement that this would be good to have, but we may not have anyone
      # available to work on it right now or in the immediate future. Community contributions would be most
      # welcome in the mean time (although it might take a while to get them reviewed if reviewers are fully
      # occupied with higher priority issues, for example immediately before a release).
      description: Higher priority than priority/awaiting-more-evidence.
      name: priority/backlog
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: e11d21
      # Stuff is burning. If it's not being actively worked on, someone is expected to drop what they're
      # doing immediately to work on it. Team leaders are responsible for making sure that all the issues, 
      # labeled with this priority, in their area are being actively worked on. Examples include user-visible
      # bugs in core features, broken builds or tests and critical security issues.
      description: Highest priority. Must be actively worked on as someone's top priority right now.
      name: priority/critical-urgent
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: eb6420
      description: Important over the long term, but may not be staffed and/or may need multiple releases to complete.
      name: priority/important-longterm
      target: both
      prowPlugin: label
      addedBy: anyone
    - color: eb6420
      description: Must be staffed and worked on either currently, or very soon, ideally in time for the next release.
      name: priority/important-soon
      target: both
      prowPlugin: label
      addedBy: anyone

    # Size PR
    # TODO(hilalymh): Needs size plugin
    - color: ee9900
      description: Denotes a PR that changes 100-499 lines, ignoring generated files.
      name: size/L
      target: prs
      prowPlugin: size
      addedBy: prow
    - color: eebb00
      description: Denotes a PR that changes 30-99 lines, ignoring generated files.
      name: size/M
      target: prs
      prowPlugin: size
      addedBy: prow
    - color: 77bb00
      description: Denotes a PR that changes 10-29 lines, ignoring generated files.
      name: size/S
      target: prs
      prowPlugin: size
      addedBy: prow
    - color: ee5500
      description: Denotes a PR that changes 500-999 lines, ignoring generated files.
      name: size/XL
      target: prs
      prowPlugin: size
      addedBy: prow
    - color: "009900"
      description: Denotes a PR that changes 0-9 lines, ignoring generated files.
      name: size/XS
      target: prs
      prowPlugin: size
      addedBy: prow
    - color: ee0000
      description: Denotes a PR that changes 1000+ lines, ignoring generated files.
      name: size/XXL
      target: prs
      prowPlugin: size
      addedBy: prow

    # Tide labels
    - color: ffaa00
      description: Denotes a PR that should be squashed by tide when it merges.
      name: tide/merge-method-squash
      target: prs
      addedBy: humans
    - color: ffaa00
      description: Denotes a PR that should be rebased by tide when it merges.
      name: tide/merge-method-rebase
      target: prs
      addedBy: humans
    - color: ffaa00
      description: Denotes a PR that should use a standard merge by tide when it merges.
      name: tide/merge-method-merge
      target: prs
      addedBy: humans
    - color: e11d21
      description: Denotes an issue that blocks the tide merge queue for a branch while it is open.
      name: tide/merge-blocker
      target: issues
      addedBy: humans

    # ¯\\\\_(ツ)_/¯
    - color: f9d0c4
      description: ¯\\\_(ツ)_/¯
      name: "¯\\_(ツ)_/¯"
      target: both
      prowPlugin: shrug
      addedBy: humans

    # /lgtm
    - color: 15dd18
      description: Indicates that a PR is ready to be merged.
      name: lgtm
      target: prs
      prowPlugin: lgtm
      addedBy: reviewers or members

    # /ok-to-test
    - color: 15dd18
      # This is the opposite of needs-ok-to-test and should be mutually exclusive.
      description: Indicates a non-member PR verified by an org member that is safe to test.
      name: ok-to-test
      target: prs
      prowPlugin: trigger
      addedBy: prow

repos:
  aws-controllers-k8s/community:
    labels:
    - color: ffddbf
      description: Indicates issues or PRs related to all the service controllers.
      name: service/all
      target: issues
      addedBy: anyone

    - color: ffddbf
      description: Indicates issues or PRs that are not related to any of the service controllers.
      name: service/none
      target: issues
      addedBy: anyone

    # AWS services labels
    {{range $_, $service := .AWSServices }}- color: f59745
      description: Indicates issues or PRs that are related to {{ $service }}-controller.
      name: service/{{ $service }}
      target: issues
      addedBy: anyone
    {{ end }}
  # TODO(a-hilaly): Maybe these repository needs specific labels to them
  aws-controllers-k8s/runtime:
  aws-controllers-k8s/code-generator:
  aws-controllers-k8s/test-infra:
  aws-controllers-k8s/pkg:
  aws-controllers-k8s/dev-tools:
  aws-controllers-k8s/examples: