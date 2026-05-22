- name: upgrade-go-version
  decorate: true
  interval: 168h
  annotations:
    description: Querys go version in ECR and compare it with versuib in repository. Raises a PR with updated GO_VERSION and bumped prow image versions
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
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
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "upgrade-go-version") }}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["ack-build-tools", "upgrade-go-version",
        "--build-config-path", "./build_config.yaml",
        "--images-config-path", "./prow/jobs/images_config.yaml",
        "--golang-ecr-repository", "v2/docker/library/golang",
        "--source-owner", "${TEST_INFRA_ORG}",
        "--source-repo", "${TEST_INFRA_REPO}"]
