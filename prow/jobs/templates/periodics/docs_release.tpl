- name: docs-release
  decorate: true
  interval: 24h
  annotations:
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  labels:
    preset-github-secrets: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: community
    base_ref: main
    workdir: true
  - org: ${TEST_INFRA_ORG}
    repo: runtime
    base_ref: main
  {{range $_, $otherService := .Config.AWSServices}}- org: ${TEST_INFRA_ORG}
    repo: {{$otherService}}-controller
    base_ref: main
  {{end}}spec:
    serviceAccountName: post-submit-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "docs") }}
        resources:
          limits:
            cpu: 1
            memory: "2048Mi"
          requests:
            cpu: 1
            memory: "2048Mi"
        command: ["/docs/build-docs.sh"]