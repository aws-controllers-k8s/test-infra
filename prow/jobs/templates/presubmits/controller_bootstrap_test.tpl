  aws-controllers-k8s/controller-bootstrap:
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
            memory: "1024Mi"
          requests:
            cpu: 1
            memory: "1024Mi"
        command: ["make", "test"]