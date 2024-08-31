  aws-controllers-k8s/pkg:
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
            cpu: 1
            memory: "1536Mi"
          requests:
            cpu: 1
            memory: "1536Mi"
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
          value: pkg
        - name: OUTPUT_PATH
          value: "/tmp/generated_attribution.md"
        - name: DEBUG
          value: "true"
        command:
        - "/bin/bash"
        - "-c"
        - "./cd/scripts/verify-attribution.sh"