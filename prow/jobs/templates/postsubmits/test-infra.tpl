  aws-controllers-k8s/test-infra:
  - name: build-prow-images
    decorate: true
    run_if_changed: ^(prow\/.*\/images_config.yaml)
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-github-secrets: "true"
    spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "build-prow-images") }}
          resources:
            limits:
              cpu: 2
              memory: "4096Mi"
            requests:
              cpu: 2
              memory: "4096Mi"
          command: ["./prow/jobs/tools/cmd/build-prow-images.sh"]
    branches:
    - main                
