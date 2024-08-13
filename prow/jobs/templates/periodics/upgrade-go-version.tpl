- name: upgrade-go-version
  decorate: true
  interval: 12h
  annotations:
    description: Querys go version in ECR and compare it with versuib in repository. Raises a PR with updated GO_VERSION and bumped prow image versions
    karpenter.sh/do-not-evict: "true"
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "upgrade-go-version") }}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["ack-build-tools", "upgrade-go-version", "--build-config-path", "./build_config.yaml", "images-config-path", "./prow/jobs/images_config.yaml","--golang-ecr-repository", "docker/library/golang"]
