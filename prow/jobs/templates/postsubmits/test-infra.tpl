  aws-controllers-k8s/test-infra:
  - name: build-prow-images
    decorate: true
    run_if_changed: ^(prow/jobs/images_config.yaml)
    annotations:
      karpenter.sh/do-not-evict: "true"
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
          command: ["ack-build-tools", 
            "build-prow-images", 
            "--images-config-path", "./prow/jobs/images_config.yaml", 
            "--jobs-config-path", "./prow/jobs/jobs_config.yaml",
            "--jobs-templates-path", "./prow/jobs/templates/",
            "--jobs-output-path", "./prow/jobs/jobs.yaml",
            "--prow-ecr-repository", "prow"
            ]
