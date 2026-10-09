- name: detect-api-changes
  decorate: true
  # UTC. aws-sdk-go-v2 releases on weekdays around 18:15-19:30 (occasionally later);
  # 20:00 catches the day's release and 08:00 picks up late ones.
  cron: "0 8,20 * * *"
  # Retry transient GitHub/network errors. Safe to re-run: the fingerprint prevents
  # duplicate issues; at worst one "change set has changed" comment repeats.
  retry:
    attempts: 2
    interval: 30m
  # Don't let overlapping runs race on the same issues.
  max_concurrency: 1
  # Backstop inside the 30m retry interval (default is 48h). The tool's own 20m
  # deadline (detectAPIChangesRunTimeout) should fire first.
  decoration_config:
    timeout: 25m
    grace_period: 5m
  annotations:
    description: Compares the latest AWS API models against ACK controllers and opens a github issue in the community repository when new resources, operations, or fields are found
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: ${TEST_INFRA_REPO}
    base_ref: ${TEST_INFRA_BRANCH}
    workdir: true
    path_alias: github.com/aws-controllers-k8s/test-infra
  {{- range $_, $service := .Config.APINotificationServices }}
  - org: ${TEST_INFRA_ORG}
    repo: {{ $service }}-controller
    base_ref: main
    workdir: false
    path_alias: github.com/aws-controllers-k8s/{{ $service }}-controller
  {{- end }}
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
        # RunSucceeded in ACK/APIChangeNotification is watched by the
        # ACK-APIChangeNotification alarms (ACKTestInfraCDK).
        command: ["ack-build-tools", "detect-api-changes",
            "--jobs-config-path", "./prow/jobs/jobs_config.yaml",
            "--controllers-root", "..",
            "--github-issues-owner", "${TEST_INFRA_ORG}",
            "--github-issues-repo", "community",
            "--max-open-issues", "{{ $.Config.APINotificationMaxOpenIssues }}",
            "--metrics-namespace", "ACK/APIChangeNotification",
            "--metrics-region", "us-west-2"]
        volumeMounts:
          - name: github-token-censor
            mountPath: /etc/github-censor
            readOnly: true
    # Mounted only so the sidecar censors the PAT in logs: it reads secrets only from
    # Secret volumes, not the preset's CSI volume or env var. Same Secret the CSI syncs.
    volumes:
      - name: github-token-censor
        secret:
          secretName: prowjob-github-pat-token
