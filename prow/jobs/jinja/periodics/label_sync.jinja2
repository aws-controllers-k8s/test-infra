- name: label-sync
  decorate: true
  interval: 6h
  annotations:
    description: Runs label_sync to synchronize GitHub repo labels with the label config defined in label_sync/labels.yaml.
    karpenter.sh/do-not-evict: "true"
  labels:
    app: label-sync
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
    - name: label-sync
      image: gcr.io/k8s-prow/label_sync:v20221205-a1b0b85d88
      resources:
        limits:
          cpu: 1
          memory: "500Mi"
        requests:
          cpu: 1
          memory: "500Mi"
      command:
      - label_sync
      args:
      - --config=/etc/config/labels.yaml
      - --confirm=true
      - --orgs=aws-controllers-k8s
      - --github-token-path=/etc/github/token
      - --github-endpoint=http://ghproxy.prow.svc.cluster.local
      - --github-endpoint=https://api.github.com
      - --debug
      volumeMounts:
      - name: config
        mountPath: /etc/config
        readOnly: true
    volumes:
    - name: config
      configMap:
        name: label-config