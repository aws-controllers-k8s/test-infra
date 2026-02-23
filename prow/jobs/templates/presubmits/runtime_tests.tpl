  aws-controllers-k8s/runtime:
  - name: unit-test
    decorate: true
    optional: false
    always_run: true
    annotations:
      karpenter.sh/do-not-evict: "true"
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
      karpenter.sh/do-not-evict: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
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
          value: runtime
        - name: OUTPUT_PATH
          value: "/tmp/generated_attribution.md"
        - name: DEBUG
          value: "true"
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/verify-attribution.sh"

  - name: runtime-crd-compat-check
    decorate: true
    optional: false
    run_if_changed: "^(config/crd/|helm/crds/)"
    annotations:
      karpenter.sh/do-not-evict: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: code-generator
      base_ref: main
      workdir: false
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
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
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/check-crd-compatibility.sh"

{{ range $_, $service := .Config.RuntimePresubmitServices }}
  - name: {{ $service }}-controller-test
    decorate: true
    optional: false
    run_if_changed: ^(pkg|apis|go.mod|go.sum)
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-dind-enabled: "true"
      preset-kind-volume-mounts: "true"
      preset-test-config: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: code-generator
      base_ref: main
      workdir: false
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    - org: aws-controllers-k8s
      repo: {{ $service }}-controller
      base_ref: main
      workdir: false
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "integration-test") }}
        resources:
          limits:
            cpu: 8
            memory: "3072Mi"
          requests:
            cpu: 8
            memory: "3072Mi"
        securityContext:
          privileged: true
        env:
        - name: SERVICE
          value: {{ $service }}
        - name: LOCAL_MODULES
          value: "true"
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
        command: ["wrapper.sh", "bash", "-c", "make kind-test SERVICE=$SERVICE"]

{{ end }}