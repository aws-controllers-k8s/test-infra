{{ range $_, $service := .Config.AWSServices }}
  ${TEST_INFRA_ORG}/{{ $service }}-controller:
  - name: {{ $service }}-kind-e2e
    decorate: true
    optional: false
    always_run: true
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
      preset-test-config: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: code-generator
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/code-generator
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
            cpu: 8
            memory: "3072Mi"
          requests:
            cpu: 8
            memory: "3072Mi"
        env:
        - name: SERVICE
          value: {{ $service }}
        # If not provided, the default will be picked from the version directive in
        # the 'go.mod' file of the controller.
        - name: GOLANG_VERSION
          value: "1.22.5"
        {{ if contains $.Config.CarmTestServices $service -}}
        - name: CARM_TESTS_ENABLED
          value: "true"
        {{ else if contains $.Config.IRSTestServices $service -}}
        - name: IRS_TESTS_ENABLED
          value: "true"
        {{ end -}}
        - name: FEATURE_GATES
          value: "ResourceAdoption=true"
        {{ $additionalControllers := index $.Config.AdditionalControllerTestServices $service -}}
        {{ if $additionalControllers -}}
        # Additional ACK controllers required by this service's e2e tests
        # (cross-controller reference tests). Installed into the KIND cluster by
        # the e2e test scripts.
        - name: ADDITIONAL_CONTROLLERS
          value: "{{ $additionalControllers }}"
        {{ end -}}
        command: ["wrapper.sh", "bash", "-c", "make kind-test SERVICE=$SERVICE"]

  - name: {{ $service }}-release-test
    decorate: true
    optional: false
    always_run: true
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
      preset-test-config: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: code-generator
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/code-generator
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
            cpu: 8
            memory: "3072Mi"
          requests:
            cpu: 8
            memory: "3072Mi"
        env:
        - name: SERVICE
          value: {{ $service }}
        # If not provided, the default will be picked from the version directive in
        # the 'go.mod' file of the controller.
        - name: GOLANG_VERSION
          value: "1.22.5"
        command: ["wrapper.sh", "bash", "-c", "make kind-helm-test SERVICE=$SERVICE"]

  - name: {{ $service }}-recommended-policy-test
    decorate: true
    optional: false
    always_run: true
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
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
        resources:
          limits:
            cpu: 250m
            memory: "256Mi"
          requests:
            cpu: 250m
            memory: "256Mi"
        securityContext:
          runAsUser: 0
        env:
        - name: SERVICE
          value: {{ $service }}
        command: ["wrapper.sh", "bash", "-c", "make test-recommended-policy SERVICE=$SERVICE"]

  - name: {{ $service }}-unit-test
    decorate: true
    optional: false
    always_run: true
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "unit-test") }}
        resources:
          limits:
            cpu: 8
            memory: "4048Mi"
          requests:
            cpu: 2
            memory: "4048Mi"
        env:
        - name: SERVICE
          value: {{ $service }}
        command: ["make", "test"]

  - name: {{ $service }}-metadata-file-test
    decorate: true
    optional: false
    always_run: true
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
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
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "unit-test") }}
        resources:
          limits:
            cpu: 250m
            memory: "256Mi"
          requests:
            cpu: 250m
            memory: "256Mi"
        env:
        - name: SERVICE
          value: {{ $service }}
        command: ["bash", "-c", "make test-metadata-file SERVICE=$SERVICE"]

  - name: {{ $service }}-verify-attribution
    # We probably want to uncomment the following line once we have the attribution
    # files verified for all the controlelrs
    # run_if_changed: "go.mod"
    always_run: true
    decorate: true
    optional: true
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
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
        - name: SERVICE
          value: "{{ $service }}"
        - name: OUTPUT_PATH
          value: "/tmp/generated_attribution.md"
        - name: DEBUG
          value: "true"
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/verify-attribution.sh"

  - name: {{ $service }}-verify-code-gen
    always_run: true
    decorate: true
    optional: false
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: code-generator
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/code-generator
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
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "auto-generate-controllers") }}
        resources:
          limits:
            cpu: 2
            memory: "4096Mi"
          requests:
            cpu: 2
            memory: "4096Mi"
        env:
        - name: SERVICE
          value: "{{ $service }}"
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/verify-code-gen.sh"

  - name: {{ $service }}-crd-compat-check
    decorate: true
    optional: false
    run_if_changed: "^(config/crd/|helm/crds/)"
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: code-generator
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/code-generator
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: true
      path_alias: github.com/aws-controllers-k8s/test-infra
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "unit-test") }}
        resources:
          limits:
            cpu: 2
            memory: "4048Mi"
          requests:
            cpu: 2
            memory: "4048Mi"
        env:
        - name: SERVICE
          value: {{ $service }}
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/check-crd-compatibility.sh"
{{ end }}
