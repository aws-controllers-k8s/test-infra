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
        {{ if contains $.Config.CarmTestServices $service }}- name: CARM_TESTS_ENABLED
          value: "true"
        {{ end }}
        command: ["wrapper.sh", "bash", "-c", "make kind-test SERVICE=$SERVICE"]

{{ end }}