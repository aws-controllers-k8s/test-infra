- name: periodic-close
  interval: 6h
  decorate: true
  annotations:
    description: Closes rotten issues after 30d of inactivity
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: ${PROW_MIRROR_REGISTRY}/commenter:${TOOLS_VERSION}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command:
          - commenter
        args:
          - --query=org:${TEST_INFRA_ORG} -label:lifecycle/frozen label:lifecycle/rotten
          - --updated=1440h
          - --token=/etc/github/token
          - |-
            --comment=Rotten issues close after 60d of inactivity.
            Reopen the issue with `/reopen`.
            Provide feedback via https://github.com/${TEST_INFRA_ORG}/community.
            /close
          - --template
          - --confirm
          - --ceiling=10