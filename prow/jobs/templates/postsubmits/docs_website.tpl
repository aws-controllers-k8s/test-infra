  ${TEST_INFRA_ORG}/docs:
  - name: deploy-docs
    decorate: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-github-secrets: "true"
      preset-controller-registry: "true"
    extra_refs:
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
    branches:
    - main
