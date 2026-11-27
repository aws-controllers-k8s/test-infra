- name: autopilot-dispatch
  decorate: true
  # The Dispatcher tick (design 5.4: 30 minutes). Ingests the issues
  # detect-api-changes files at 08:00 and 20:00 within half an hour. Safe to
  # re-run by hand: a tick with nothing new changes nothing.
  cron: "*/30 * * * *"
  # The ledger has one writer at a time.
  max_concurrency: 1
  decoration_config:
    # Below the 30-minute tick (timeout ladder, design 5.4).
    timeout: 20m
    grace_period: 5m
  annotations:
    description: ACK Autopilot Dispatcher tick. Ingests detect-api-changes issues into ack-private/autopilot, routes and caps work items, and logs the launches it would make (shadow launcher)
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
          # ack-private/autopilot/ (workitems/ and policy/) has been bootstrapped.
          - name: AUTOPILOT_LEDGER_WRITE
            value: "false"
          # "true" posts status comments on the api-change issues; needs ledger
          # writes, and Issues: write on the dedicated token.
          - name: AUTOPILOT_COMMENT
            value: "false"
        command: ["/bin/sh", "-ec"]
        args:
          - |
            go build -mod=mod -o /tmp/ack-build-tools ./prow/jobs/tools/cmd
            exec sh ./scripts/autopilot-dispatch.sh ${TEST_INFRA_ORG}
        volumeMounts:
          - name: github-token-censor
            mountPath: /etc/github-censor
            readOnly: true
    # Mounted only so the sidecar censors the PAT in logs (same as detect-api-changes).
    volumes:
      - name: github-token-censor
        secret:
          secretName: prowjob-github-pat-token
