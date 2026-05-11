  aws-controllers-k8s/ack-chart:
  - name: ack-chart-release
    decorate: true
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
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