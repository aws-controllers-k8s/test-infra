- name: periodic-stale
  interval: 6h
  decorate: true
  annotations:
    description: Adds lifecycle/stale to issues after 90d of inactivity
    karpenter.sh/do-not-evict: "true"
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: gcr.io/k8s-prow/commenter:v20210422-d12e80af3e
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command:
          - /app/robots/commenter/app.binary
        args:
          - --query=org:aws-controllers-k8s -label:lifecycle/frozen -label:lifecycle/rotten -label:lifecycle/stale
          - --updated=4320h
          - --token=/etc/github/token
          - |-
            --comment=Issues go stale after 180d of inactivity.
            Mark the issue as fresh with `/remove-lifecycle stale`.
            Stale issues rot after an additional 60d of inactivity and eventually close.
            If this issue is safe to close now please do so with `/close`.
            Provide feedback via https://github.com/aws-controllers-k8s/community.
            /lifecycle stale
          - --template
          - --confirm
          - --ceiling=10