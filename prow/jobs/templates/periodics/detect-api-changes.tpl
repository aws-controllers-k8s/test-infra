- name: detect-api-changes
  decorate: true
  interval: 24h
  # Retries absorb transient GitHub and network errors. Re-running is safe: the
  # fingerprint keeps a retry from filing a duplicate issue, and the worst case is
  # one repeated "change set has changed" comment when a run fails between the
  # comment and the body update it announces. Horologium counts any final state
  # other than success as a failure and measures the interval from the failed
  # run's start.
  retry:
    attempts: 2
    interval: 30m
  # One run at a time: a scheduled run or retry that comes due while another is still
  # going waits for it rather than racing it on the same issues.
  max_concurrency: 1
  # Bounds a stalled run well inside the 30m retry interval instead of the 48h
  # default_decoration_configs timeout. The tool's own deadline
  # (detectAPIChangesRunTimeout, 20m) fires first so it exits with its own error and
  # tally; this is the backstop, after which the entrypoint interrupts the process
  # and kills it at the end of the grace period.
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
        command: ["ack-build-tools", "detect-api-changes",
            "--jobs-config-path", "./prow/jobs/jobs_config.yaml",
            "--controllers-root", "..",
            "--github-issues-owner", "${TEST_INFRA_ORG}",
            "--github-issues-repo", "community",
            "--max-open-issues", "{{ $.Config.APINotificationMaxOpenIssues }}"]
        volumeMounts:
          - name: github-token-censor
            mountPath: /etc/github-censor
            readOnly: true
    # Mounted only so Prow's sidecar censors the PAT from this job's log. The
    # sidecar loads secrets solely from Secret-type volumes on the test container;
    # preset-github-secrets delivers the token as a CSI volume and a secretKeyRef
    # env var, neither of which it reads, so censor_secrets alone censors nothing.
    # The Secret is the one the preset's CSI volume syncs.
    volumes:
      - name: github-token-censor
        secret:
          secretName: prowjob-github-pat-token
