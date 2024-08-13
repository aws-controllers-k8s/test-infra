  aws-controllers-k8s/ack-chart:
  - name: ack-chart-release
    decorate: true
    annotations:
      karpenter.sh/do-not-evict: "true"
    extra_refs:
    - org: aws-controllers-k8s
      repo: test-infra
      base_ref: main
      workdir: true
    spec:
      serviceAccountName: post-submit-service-account
      containers:
        - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "deploy") }}
          resources:
            limits:
              cpu: 2
              memory: "2048Mi"
            requests:
              cpu: 2
              memory: "2048Mi"
          command: ["/bin/bash", "-c", "cd cd/ack-chart && ./upload-chart.sh"]
    branches:
    - ^[0-9]+\.[0-9]+\.[0-9]+$