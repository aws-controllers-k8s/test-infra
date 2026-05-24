  ${TEST_INFRA_ORG}/runtime:
  - name: runtime-docs-release
    decorate: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-github-secrets: "true"
      preset-controller-registry: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: community
      base_ref: main
      workdir: true
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
    {{range $_, $service := .Config.AWSServices}}- org: ${TEST_INFRA_ORG}
      repo: {{ $service }}-controller
      base_ref: main
    {{ end }}spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "docs") }}
          resources:
            limits:
              cpu: 1
              memory: "500Mi"
            requests:
              cpu: 1
              memory: "500Mi"
          command: ["/docs/build-docs.sh"]
    run_if_changed: "apis/core/.*"
    branches:
    - main
