- name: detect-api-changes
  decorate: true
  interval: 24h
  annotations:
    description: Compares the latest AWS API models against ACK controllers and opens a github issue in the community repository when new resources, operations, or fields are found
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: ${TEST_INFRA_REPO}
    base_ref: ${TEST_INFRA_BRANCH}
    workdir: true
    path_alias: github.com/aws-controllers-k8s/test-infra
  {{- range $_, $service := .Config.APINotificationServices }}
  - org: ${TEST_INFRA_ORG}
    repo: {{ $service }}-controller
    base_ref: main
    workdir: false
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
  {{- end }}
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "detect-api-changes") }}
        resources:
          limits:
            cpu: 1
            memory: "1Gi"
          requests:
            cpu: 1
            memory: "1Gi"
        command: ["ack-build-tools", "detect-api-changes",
            "--jobs-config-path", "./prow/jobs/jobs_config.yaml",
            "--controllers-root", "..",
            "--github-issues-owner", "${TEST_INFRA_ORG}",
            "--github-issues-repo", "community",
            "--max-open-issues", "{{ $.Config.APINotificationMaxOpenIssues }}"]
