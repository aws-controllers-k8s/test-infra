- name: periodic-deploy-docs
  decorate: true
  interval: 12h
  annotations:
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  labels:
    preset-github-secrets: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: docs
    base_ref: main
    workdir: true
  {{range $_, $service := .Config.AWSServices}}- org: ${TEST_INFRA_ORG}
    repo: {{ $service }}-controller
    base_ref: main
  {{end}}spec:
    serviceAccountName: post-submit-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "deploy-docs") }}
        resources:
          limits:
            cpu: 2
            memory: "8Gi"
          requests:
            cpu: 2
            memory: "8Gi"
        command: ["/scripts/deploy-docs.sh"]
