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
  # deadline (detectAPIChangesRunTimeout) starts after the build (about 2m with the
  # image's warm caches) and should fire first.
  decoration_config:
    timeout: 25m
    grace_period: 5m
  annotations:
    description: Compares the latest AWS API models against ACK controllers and opens a github issue in the ack-private repository when new resources, operations, or fields are found
    # karpenter.sh/do-not-evict is deprecated: https://github.com/aws/karpenter-provider-aws/issues/5394
    karpenter.sh/do-not-disrupt: "true"
  extra_refs:
  - org: ${TEST_INFRA_ORG}
    repo: ${TEST_INFRA_REPO}
    base_ref: ${TEST_INFRA_BRANCH}
    workdir: true
    path_alias: github.com/aws-controllers-k8s/test-infra
  # The oracle is built against code-generator main, which is what controller
  # releases regenerate with (controller_release.tpl).
  - org: ${TEST_INFRA_ORG}
    repo: code-generator
    base_ref: main
    workdir: false
    path_alias: github.com/aws-controllers-k8s/code-generator
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
        # RunSucceeded in ACK/APIChangeNotification feeds the job's CloudWatch alarms.
        #
        # TODO: file in community instead of ack-private once maintainers trust the
        # findings. Issues stay where they are filed: moving repos re-files every
        # service, since the bot only finds its issues in the repo it is pointed at.
        #
        # A failed build publishes no RunSucceeded, which the missing-data alarm catches.
        command: ["/bin/sh", "-ec"]
        args:
          - |
            ref=$(git -C ../code-generator describe --tags --always)
            go mod edit -replace github.com/aws-controllers-k8s/code-generator=../code-generator
            go build -mod=mod -o /tmp/ack-build-tools \
              -ldflags "-X github.com/aws-controllers-k8s/test-infra/prow/jobs/tools/cmd/command/apichanges.codeGeneratorRef=$ref" \
              ./prow/jobs/tools/cmd
            exec /tmp/ack-build-tools detect-api-changes \
              --jobs-config-path ./prow/jobs/jobs_config.yaml \
              --controllers-root .. \
              --github-issues-owner ${TEST_INFRA_ORG} \
              --github-issues-repo ack-private \
              --max-open-issues {{ $.Config.APINotificationMaxOpenIssues }} \
              --metrics-namespace ACK/APIChangeNotification \
              --metrics-region us-west-2
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
