- name: periodic-rotten
  interval: 6h
  decorate: true
  annotations:
    description: Moves stale issues to rotten state after 30d of inactivity
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
          - --query=org:${TEST_INFRA_ORG} -label:lifecycle/frozen -label:lifecycle/rotten label:lifecycle/stale
          - --updated=1440h
          - --token=/etc/github/token
          - |-
            --comment=Stale issues rot after 60d of inactivity.
            Mark the issue as fresh with `/remove-lifecycle rotten`.
            Rotten issues close after an additional 60d of inactivity.
            If this issue is safe to close now please do so with `/close`.
            Provide feedback via https://github.com/${TEST_INFRA_ORG}/community.
            /lifecycle rotten
          - --template
          - --confirm
          - --ceiling=10