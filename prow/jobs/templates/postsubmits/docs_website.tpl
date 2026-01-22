  aws-controllers-k8s/docs:
  - name: deploy-docs
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    {{range $_, $service := .Config.AWSServices}}- org: aws-controllers-k8s
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
