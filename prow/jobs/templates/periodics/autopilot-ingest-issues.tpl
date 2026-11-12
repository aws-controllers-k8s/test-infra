- name: autopilot-ingest-issues
  decorate: true
  # 30 minutes after each detect-api-changes run ("0 8,20 * * *"), so the issues
  # it reads are fresh. Safe to re-run by hand: ingestion is idempotent.
  cron: "30 8,20 * * *"
  # One writer of the ledger at a time.
  max_concurrency: 1
  decoration_config:
    timeout: 20m
    grace_period: 5m
  annotations:
    description: Reads detect-api-changes issues in ack-private and records their findings as ACK Autopilot work items under ack-private/autopilot
    karpenter.sh/do-not-disrupt: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: ${TEST_INFRA_REPO}
    base_ref: ${TEST_INFRA_BRANCH}
    workdir: true
    path_alias: github.com/aws-controllers-k8s/test-infra
  labels:
    preset-github-secrets: "true"
  agent: kubernetes
  spec:
    serviceAccountName: periodic-service-account
    containers:
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "detect-api-changes") }}
        resources:
          limits:
            cpu: 1
            memory: "1Gi"
          requests:
            cpu: 1
            memory: "1Gi"
        env:
          # "true" commits and pushes the ledger; anything else is a dry run.
          # Do not set to true until this job uses the dedicated least-privilege
          # autopilot-ledger-github-token instead of preset-github-secrets, and
          # ack-private/autopilot/ is bootstrapped (plan Task 12).
          - name: AUTOPILOT_LEDGER_WRITE
            value: "false"
        command: ["/bin/sh", "-ec"]
        args:
          - |
            go build -mod=mod -o /tmp/ack-build-tools ./prow/jobs/tools/cmd
            exec sh ./scripts/autopilot-ingest.sh ${TEST_INFRA_ORG}
        volumeMounts:
          - name: github-token-censor
            mountPath: /etc/github-censor
            readOnly: true
    # Mounted only so the sidecar censors the PAT in logs (same as detect-api-changes).
    volumes:
      - name: github-token-censor
        secret:
          secretName: prowjob-github-pat-token
