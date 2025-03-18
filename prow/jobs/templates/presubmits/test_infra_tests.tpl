  aws-controllers-k8s/test-infra:
{{- range $_, $service := .Config.ACKTestPresubmitServices }}
  - name: acktest-{{ $service }}-e2e-tests
    decorate: true
    optional: false
    # only if src/acktest/ code changed
    run_if_changed: ^(src/acktest/.*|requirements.txt)
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
            cpu: 2
            memory: "3072Mi"
        securityContext:
          privileged: true
        env:
        - name: SERVICE
          value: {{ $service }}
        - name: LOCAL_ACKTEST_LIBRARY
          value: "true"
        {{ if contains $.Config.CarmTestServices $service }}
        - name: CARM_TESTS_ENABLED
          value: "true"
        {{ end }}
        - name: FEATURE_GATES
          value: "ResourceAdoption=true"
        command:
        - "wrapper.sh"
        - "bash"
        - "-c"
        - "make kind-test SERVICE=$SERVICE LOCAL_ACKTEST_LIBRARY=$LOCAL_ACKTEST_LIBRARY"
{{ end }}