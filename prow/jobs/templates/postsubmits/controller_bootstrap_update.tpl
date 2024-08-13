  aws-controllers-k8s/controller-bootstrap:
  - name: auto-update-controllers
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-github-secrets: "true"
    extra_refs:
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