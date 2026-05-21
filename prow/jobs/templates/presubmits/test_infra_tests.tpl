  ${TEST_INFRA_ORG}/test-infra:
{{- range $_, $service := .Config.ACKTestPresubmitServices }}
  - name: acktest-{{ $service }}-e2e-tests
    decorate: true
    optional: false
    # only if src/acktest/ code changed
    run_if_changed: ^(src/acktest/.*|requirements.txt)
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
      repo: {{ $service }}-controller
      base_ref: main
      workdir: false
      path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
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
        {{ if contains $.Config.CarmTestServices $service -}}
        - name: CARM_TESTS_ENABLED
          value: "true"
        {{ else if contains $.Config.IRSTestServices $service -}}
        - name: IRS_TESTS_ENABLED
          value: "true"
        {{ end -}}
        - name: FEATURE_GATES
          value: "ResourceAdoption=true"
        command:
        - "wrapper.sh"
        - "bash"
        - "-c"
        - "make kind-test SERVICE=$SERVICE LOCAL_ACKTEST_LIBRARY=$LOCAL_ACKTEST_LIBRARY"
{{ end }}