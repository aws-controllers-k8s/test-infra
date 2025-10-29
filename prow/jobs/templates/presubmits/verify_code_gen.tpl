{{ range $_, $service := .Config.AWSServices }}
  aws-controllers-k8s/{{ $service }}-controller:
  - name: {{ $service }}-verify-code-gen
    always_run: true
    decorate: true
    optional: true
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
{{ end }}
