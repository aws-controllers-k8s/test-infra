  ${TEST_INFRA_ORG}/code-generator:
  - name: auto-generate-controllers
    decorate: true
    path_alias: github.com/aws-controllers-k8s/code-generator
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: true
      path_alias: github.com/aws-controllers-k8s/test-infra
    - org: ${TEST_INFRA_ORG}
      repo: runtime
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/runtime
    {{range $_, $service := .Config.AWSServices}}- org: ${TEST_INFRA_ORG}
      repo: {{ $service }}-controller
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    {{end}}spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "auto-generate-controllers") }}
          resources:
            limits:
              cpu: 8
              memory: "8192Mi"
            requests:
              cpu: 8
              memory: "8192Mi"
          command: ["/bin/bash", "-c", "./cd/auto-generate/auto-generate-controllers.sh"]
    branches: #supports tags too.
    - ^v[0-9]+\.[0-9]+\.[0-9]+$