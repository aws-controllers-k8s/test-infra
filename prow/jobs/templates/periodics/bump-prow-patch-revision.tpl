- name: bump-prow-patch-revision
  decorate: true
  # Every other day at a fixed time rather than an interval: the merge this job makes rolls
  # every Prow component, so it should land when maintainers are around.
  cron: "0 16 */2 * *"
  annotations:
    description: Rebuilds the pinned Prow images against current OS packages by bumping prowPatchRevision, then merges its own PR once scripts/verify-prow-patch-revision.sh confirms nothing else changed. Never moves prowVersion, toolsVersion or the ProwJob CRD.
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: ${TEST_INFRA_REPO}
    base_ref: ${TEST_INFRA_BRANCH}
    workdir: true
    path_alias: github.com/aws-controllers-k8s/test-infra
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      # The only job image with bash, git, yq, jq, curl, the AWS CLI and ack-build-tools.
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "build-prow-images") }}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["./scripts/bump-prow-patch-revision.sh",
            "--source-owner", "${TEST_INFRA_ORG}",
            "--source-repo", "${TEST_INFRA_REPO}",
            "--base-branch", "${TEST_INFRA_BRANCH}"]
