  ${TEST_INFRA_ORG}/ack-chart:
  - name: ack-chart-release
    decorate: true
    path_alias: github.com/aws-controllers-k8s/ack-chart
    annotations:
      # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
    labels:
      preset-controller-registry: "true"
    extra_refs:
    - org: ${TEST_INFRA_ORG}
      repo: ${TEST_INFRA_REPO}
      base_ref: ${TEST_INFRA_BRANCH}
      workdir: true
      path_alias: github.com/aws-controllers-k8s/test-infra
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
          securityContext:
            privileged: true
          command: ["/bin/bash", "-c", "cd cd/ack-chart && ./upload-chart.sh"]
    branches:
    - ^[0-9]+\.[0-9]+\.[0-9]+$