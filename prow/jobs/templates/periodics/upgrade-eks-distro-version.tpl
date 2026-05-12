- name: upgrade-eks-distro-version
  decorate: true
  interval: 168h
  annotations:
    description: Querys eks-distro version in ECR and compare it with version in build_config.yaml. Creates a PR with updated eks-distro version and bumped prow image versions if outdated
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: ${TEST_INFRA_REPO}
    base_ref: ${TEST_INFRA_BRANCH}
    workdir: true
    path_alias: github.com/aws-controllers-k8s/test-infra
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "upgrade-go-version") }} #Use upgrade-go-version image for now
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["ack-build-tools", "upgrade-eks-distro-version", 
            "--images-config-path", "./prow/jobs/images_config.yaml"]
