  ${TEST_INFRA_ORG}/code-generator:
  - name: unit-test
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
            memory: "3072Mi"
          requests:
            cpu: 2
            memory: "3072Mi"
        command: ["make", "test"]

  - name: verify-attribution
    # We probably want to uncomment the following line once we have the attribution
    # files verified for all the controlelrs
    # run_if_changed: "go.mod"
    always_run: true
    decorate: true
    optional: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: true
      path_alias: github.com/aws-controllers-k8s/test-infra
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "verify-attribution") }}
        resources:
          limits:
            cpu: 1000m
            memory: "512Mi"
          requests:
            cpu: 250m
            memory: "512Mi"
        securityContext:
          runAsUser: 0
        env:
        - name: REPOSITORY_NAME
          value: code-generator
        - name: OUTPUT_PATH
          value: "/tmp/generated_attribution.md"
        - name: DEBUG
          value: "true"
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/verify-attribution.sh"

  - name: s3-olm-test
    decorate: true
    optional: true
    always_run: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: runtime
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/runtime
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: false
      path_alias: github.com/aws-controllers-k8s/test-infra
    - org: ${TEST_INFRA_ORG}
      repo: s3-controller
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/s3-controller
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "olm-test") }}
        resources:
          limits:
            cpu: 8
            memory: "8192Mi"
          requests:
            cpu: 8
            memory: "8192Mi"
        env:
        - name: SERVICE
          value: "s3"
        - name: ACK_GENERATE_OLM
          value: "true"
        command: ["make", "build-controller"]
  - name: crd-compat-check
    decorate: true
    optional: true
    always_run: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: runtime
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/runtime
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: true
      path_alias: github.com/aws-controllers-k8s/test-infra
    {{- range $_, $service := .Config.CodegenPresubmitServices }}
    - org: ${TEST_INFRA_ORG}
      repo: {{ $service }}-controller
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    {{- end }}
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "unit-test") }}
        resources:
          limits:
            cpu: 4
            memory: "8192Mi"
          requests:
            cpu: 4
            memory: "8192Mi"
        env:
        - name: SERVICES
          value: "{{ range $i, $service := .Config.CodegenPresubmitServices }}{{ if $i }} {{ end }}{{ $service }}{{ end }}"
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/check-crd-compatibility-codegen.sh"

  {{- range $_, $service := .Config.CodegenPresubmitServices }}
  - name: {{ $service }}-controller-test
    decorate: true
    optional: false
    always_run: true
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
      preset-test-config: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: runtime
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/runtime
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: true
      path_alias: github.com/aws-controllers-k8s/test-infra
    - org: ${TEST_INFRA_ORG}
      repo: {{ $service }}-controller
      base_ref: main
      workdir: true
      path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "integration-test") }}
        resources:
          limits:
            cpu: 8
            memory: "8192Mi"
          requests:
            cpu: 8
            memory: "8192Mi"
        securityContext:
          privileged: true
        env:
        - name: SERVICE
          value: {{ $service }}
        {{ if contains $.Config.CarmTestServices $service -}}
        - name: CARM_TESTS_ENABLED
          value: "true"
        {{ else if contains $.Config.IRSTestServices $service -}}
        - name: IRS_TESTS_ENABLED
          value: "true"
        {{ end -}}
        - name: FEATURE_GATES
          value: "ResourceAdoption=true"
        command: ["wrapper.sh", "bash", "-c", "./cd/core-validator/generate-test-controller.sh"]
{{ end }}