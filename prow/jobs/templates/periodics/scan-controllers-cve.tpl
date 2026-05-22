- name: scan-controllers-cve
  decorate: true
  interval: 720h
  annotations:
    description: Scans ack supported AWS service controllers for CVE's. If they exist, creates a github issue in commmunity repository
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
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "scan-controllers-cve") }}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["ack-build-tools", "scan-controllers-cve",
            "--jobs-config-path", "./prow/jobs/jobs_config.yaml",
            "--github-issues-owner", "${TEST_INFRA_ORG}",
            "--github-issues-repo", "community"]
