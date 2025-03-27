  aws-controllers-k8s/code-generator:
  - name: auto-generate-controllers
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    - org: aws-controllers-k8s
      repo: runtime
      base_ref: main
      workdir: false
    {{range $_, $service := .Config.AWSServices}}- org: aws-controllers-k8s
      repo: {{ $service }}-controller
      base_ref: main
      workdir: false
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