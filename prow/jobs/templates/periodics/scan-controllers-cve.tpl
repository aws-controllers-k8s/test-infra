- name: scan-controllers-cve
  decorate: true
  interval: 720h
  annotations:
    description: Scans ack supported AWS service controllers for CVE's. If they exist, creates a github issue in commmunity repository
    karpenter.sh/do-not-evict: "true"
  extra_refs:
  - org: aws-controllers-k8s
    repo: test-infra
    base_ref: main
    workdir: true
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "scan-controllers-cve") }}
        resources:
          limits:
            cpu: 1
            memory: "500Mi"
          requests:
            cpu: 1
            memory: "500Mi"
        command: ["ack-build-tools", "scan-controllers-cve", 
            "--jobs-config-path", "./prow/jobs/jobs_config.yaml" ]
