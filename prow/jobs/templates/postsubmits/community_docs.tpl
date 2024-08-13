  aws-controllers-k8s/community:
  - name: community-docs-release
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: runtime
      base_ref: main
    {{range $_, $service := .Config.AWSServices}}- org: aws-controllers-k8s
      repo: {{ $service }}-controller
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
    run_if_changed: "docs/.*"
    branches:
    - main