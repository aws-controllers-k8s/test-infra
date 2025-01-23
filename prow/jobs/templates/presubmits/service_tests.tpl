{{ range $_, $service := .Config.AWSServices }}
  aws-controllers-k8s/{{ $service }}-controller:
  - name: {{ $service }}-kind-e2e
    decorate: true
    optional: false
    always_run: true
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
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "integration-test") }}
        securityContext:
          privileged: true
        resources:
          limits:
            cpu: 16
            memory: "8192Mi"
          requests:
            cpu: 16
            memory: "8192Mi"
        env:
        - name: SERVICE
          value: {{ $service }}
        # If not provided, the default will be picked from the version directive in
        # the 'go.mod' file of the controller.
        - name: GOLANG_VERSION
          value: "1.22.5"
        {{ if contains $.Config.CarmTestServices $service }}
        - name: CARM_TESTS_ENABLED
          value: "true"
        {{ end }}
        {{ if contains $.Config.AddoptionTestServices $service }}
        - name: FEATURE_GATES
          value: "ResourceAdoption=true"
        {{ end -}}
        command: ["wrapper.sh", "bash", "-c", "make kind-test SERVICE=$SERVICE"]

  - name: {{ $service }}-release-test
    decorate: true
    optional: false
    always_run: true
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
    annotations:
      karpenter.sh/do-not-evict: "true"
    labels:
      preset-test-config: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
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
    annotations:
      karpenter.sh/do-not-evict: "true"
    spec:
      serviceAccountName: pre-submit-service-account
      containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "unit-test") }}
        resources:
          limits:
            cpu: 8
            memory: "4048Mi"
          requests:
            cpu: 8
            memory: "4048Mi"
        env:
        - name: SERVICE
          value: {{ $service }}
        command: ["make", "test"]

  - name: {{ $service }}-metadata-file-test
    decorate: true
    optional: false
    always_run: true
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
{{ end }}
