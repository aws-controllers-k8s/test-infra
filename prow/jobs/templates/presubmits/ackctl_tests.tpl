  ${TEST_INFRA_ORG}/ackctl:
  - name: unit-test
{{- if $.Config.PresubmitCluster }}
    cluster: {{ $.Config.PresubmitCluster }}
{{- end }}
    decorate: true
    optional: false
    always_run: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "unit-test") }}
        resources:
          limits:
            cpu: 2
            memory: "3Gi"
          requests:
            cpu: 1
            memory: "2Gi"
        command: ["make", "test"]

  # CREATES AND DELETES real AWS resources across three services, all free of charge and
  # carrying a run-unique tag. ACK_TEST_SERVICES narrows the fixtures if the environment
  # lacks permission for some.
  - name: integration-test
{{- if $.Config.PresubmitCluster }}
    cluster: {{ $.Config.PresubmitCluster }}
{{- end }}
    decorate: true
    optional: true
    always_run: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "unit-test") }}
        resources:
          limits:
            cpu: 1
            memory: "1024Mi"
          requests:
            cpu: 500m
            memory: "1024Mi"
        env:
        # GetResources is regional, so the tests refuse to guess.
        - name: AWS_REGION
          value: "us-west-2"
        - name: SERVICE
          value: s3
        command: ["wrapper.sh", "bash", "-c", "make test-integration"]

  # Stands up KIND, installs a released controller chart, and checks that a manifest ack
  # emitted actually adopts. A released chart rather than a source build, because the
  # catalog is generated from release tags.
  - name: kind-e2e
{{- if $.Config.PresubmitCluster }}
    cluster: {{ $.Config.PresubmitCluster }}
{{- end }}
    decorate: true
    optional: true
    always_run: true
    path_alias: github.com/aws-controllers-k8s/ackctl
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
      preset-test-config: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: true
      path_alias: github.com/aws-controllers-k8s/test-infra
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "integration-test") }}
        securityContext:
          privileged: true
        resources:
          limits:
            cpu: 4
            memory: "3072Mi"
          requests:
            cpu: 2
            memory: "2048Mi"
        env:
        - name: SERVICE
          value: s3
        command: ["wrapper.sh", "bash", "-c", "make kind-ack-e2e-test SERVICE=$SERVICE"]
