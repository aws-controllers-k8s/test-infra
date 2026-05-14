  aws-controllers-k8s/controller-bootstrap:
  - name: auto-update-controllers
    decorate: true
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
    {{range $_, $service := .Config.AWSServices}}- org: aws-controllers-k8s
      repo: {{ $service }}-controller
      base_ref: main
      workdir: false
    {{end}}spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "auto-update-controllers") }}
          resources:
            limits:
              cpu: 2
              memory: "500Mi"
            requests:
              cpu: 2
              memory: "500Mi"
          command: ["/bin/bash", "-c", "./cd/auto-update/project-static-files.sh"]
    branches: #supports tags too.
    - ^v[0-9]+\.[0-9]+\.[0-9]+$