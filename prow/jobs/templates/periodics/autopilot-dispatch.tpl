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
    # Becomes autopilot-dispatcher (prow/autopilot/deploy/) when the real
    # launcher goes live; periodic-service-account cannot create ProwJobs.
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
          # "true" lets policy/autopilot.yaml `launcher: prow` create ProwJobs
          # (ACK Autopilot Plan 7). Do not set until every item of
          # prow/autopilot/deploy/SECURITY-REVIEW.md "Going live" is done:
          # Plan P's rulesets, the review, the manifests in that directory
          # applied, and serviceAccountName above set to autopilot-dispatcher.
          - name: AUTOPILOT_LAUNCHER_ENABLED
            value: "false"
          # "true" lets the Dispatcher mark a gated draft PR ready for review
          # (ACK Autopilot Plan 9), with AUTOPILOT_PR_GITHUB_TOKEN (a
          # controller-repo token, mounted the same way as GITHUB_TOKEN). Off:
          # gated drafts stay draft and a person marks them ready.
          - name: AUTOPILOT_PR_WRITE
            value: "false"
          # The base64 public half of the secret autopilot-dod-signing-key,
          # printed by "ack-build-tools autopilot dod-keygen" (Plan 9 review).
          # It is not a secret. The Dispatcher trusts a dod verdict only with
          # a valid signature from that key; empty trusts none, and dod gates
          # wait until they escalate GATE_STALLED.
          - name: AUTOPILOT_DOD_PUBLIC_KEY
            value: ""
          # The tick publishes RunSucceeded (Job=autopilot-dispatch),
          # WorkItemsReapedToday, GateFirstPass, GateFirstFail, RunsCompleted,
          # RunsFailed and ItemsEscalated here, once per tick, dry run or not
          # (ACK Autopilot Plan 10); the periodic role's
          # CloudWatchAutopilotMetrics allows this namespace only. Empty
          # publishes nothing.
          - name: AUTOPILOT_METRICS_NAMESPACE
            value: "ACK/Autopilot"
          - name: AUTOPILOT_METRICS_REGION
            value: "us-west-2"
          # The prow launcher resolves agent-workflows.yaml's image repository
          # with it, as the agent-workflow-config chart does for agent-plugin.
          - name: PROW_IMAGES_REPO_URI
            value: "${PROW_IMAGES_REPO_URI}"
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
