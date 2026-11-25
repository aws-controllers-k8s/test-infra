{{- range $_, $service := .Config.AutopilotSDKBumpServices }}
- name: autopilot-sdk-bump-{{ $service }}
  decorate: true
  # ACK Autopilot Plan 6. Daily dry run for one controller; Plan 7's launcher
  # replaces it. Rerun by hand from Deck. One run per controller at a time.
  cron: "0 3 * * *"
  max_concurrency: 1
  decoration_config:
    # build-controller twice plus ack-dod (go test) stay well below this.
    timeout: 3h
    grace_period: 10m
  annotations:
    description: ACK Autopilot sdk-bump for {{ $service }}-controller (dry run). Regenerates at the ledger's SDK model, runs ack-dod, plans the draft PR
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
      - image: {{printf "%s:%s" $.ImageContext.ImageRepo (index $.ImageContext.Images "auto-generate-controllers") }}
        resources:
          limits:
            cpu: 8
            memory: "8192Mi"
          requests:
            cpu: 8
            memory: "8192Mi"
        env:
          - name: SERVICE
            value: "{{ $service }}"
          # "true" pushes autopilot/{{ $service }}/sdk-bump and opens the draft PR as ack-bot.
          # Do not set before Plan P's rulesets are applied and the owner approves.
          - name: AUTOPILOT_SDK_BUMP_WRITE
            value: "false"
        command: ["/bin/bash", "-c", "./scripts/autopilot-sdk-bump.sh"]
        volumeMounts:
          - name: github-token-censor
            mountPath: /etc/github-censor
            readOnly: true
    volumes:
      - name: github-token-censor
        secret:
          secretName: prowjob-github-pat-token
{{ end }}