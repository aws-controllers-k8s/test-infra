  aws-controllers-k8s/runtime:
  - name: runtime-docs-release
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: community
      base_ref: main
      workdir: true
    {{range $_, $service := .Config.AWSServices}}- org: aws-controllers-k8s
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
