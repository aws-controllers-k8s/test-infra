- name: sync-prs-to-project
  decorate: true
  interval: 1h
  annotations:
    description: Adds new community PRs from across the org to the ACK Planning project.
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
      # Ships gh and jq.
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "controller-release-tag") }}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["./scripts/sync-prs-to-project.sh",
            "--org", "${TEST_INFRA_ORG}",
            "--project-number", "17",
            "--window-hours", "72",
            "--exclude-owners-aliases", "OWNERS_ALIASES",
            "--exclude-author", "ack-agent",
            "--exclude-author", "ack-bot",
            "--exclude-author", "backlog-burndown-bot",
            "--exclude-author", "backlog-burndown-agent",
            "--exclude-author", "dependabot[bot]"]
