# ACK Autopilot Dispatcher: builder-security review checklist (Plans 7-9)

The real launcher lets the `autopilot-dispatch` periodic create ProwJobs. This
is the checklist the builder-security review signs off before it goes live
(design §9 control 8, owner decisions 2026-11-13 and 2026-11-16). Until every
box in "Going live" is ticked, `AUTOPILOT_LAUNCHER_ENABLED` stays `"false"` in
`prow/jobs/templates/periodics/autopilot-dispatch.tpl`, `policy/autopilot.yaml`
says `launcher: shadow`, and nothing in this directory is applied.

Nothing in this directory is deployed by Argo CD or Flux
(`argocd/applications/values.yaml` has no path under `prow/autopilot`;
`prowlaunch.TestNothingDeploysThisDirectory` checks it). A human applies it.

## What changes

| Before (Plan 2) | After (Plan 7, live) |
|---|---|
| The Dispatcher logs the runs it would start (shadow launcher) | It creates ProwJobs in `prow`: `agent-workflow-add-field` (config-only lane, at most 2 at once), `autopilot-sdk-bump` (SDK-bump lane and `--mode refresh`), `agent-workflow-triage` (triage lane, Plan 8), `agent-workflow-add-resource` (new-resource lane, at most 1 at once and 1 a day, Plan 9) and `autopilot-dod` (its own gate job on a PR head, Plan 9) |
| Runs as `periodic-service-account` | Runs as `autopilot-dispatcher` (test-pods) with the Role in `rbac.yaml` and the AWS role in `pod-identity.yaml` |
| No cluster API calls | `create`, `get`, `list`, `patch` on `prowjobs` in `prow` |
| No result reads | One S3 GET per finished run: `logs/<job>/<build id>/artifacts/agent-result.json`, at most 64 KiB; and, for GATING (Plan 9), `verdict.json` of a finished `autopilot-sdk-bump` or `autopilot-dod` job, at most 1 MiB |
| No PR reads or writes (Plan 2-8) | Plan 9: reads, with `GITHUB_TOKEN`, only the PRs the ledger's `links.pr` names (pull, commits, reviews, commit tree, compare) and each queued repo's open PRs (runtime-release back-off). Its one write, marking a gated draft ready for review, needs `AUTOPILOT_PR_WRITE=true` and a separate `AUTOPILOT_PR_GITHUB_TOKEN`; both are off/absent in the periodic |
| No other ProwJob reads | Plan 9: `list` of agent-plugin's `address-feedback` jobs for one PR (label selector), to wait while one runs; read only, never adopted, aborted or reconciled |

No listener is opened anywhere: the Dispatcher is a client of the Kubernetes
API (pod ServiceAccount token, cluster CA, no proxy) and of S3 (SigV4 with the
pod's Pod Identity credentials). There is no new endpoint, port, Service or
Ingress, and no new public storage.

## Identities and credentials

| Credential | Where it lives | Who can read it | Lifetime |
|---|---|---|---|
| `autopilot-dispatcher` ServiceAccount token | projected into every container of the Dispatcher pod (test container, clonerefs, initupload, sidecar) | code running in that pod | bound token, rotated by the kubelet (about 1 h); the client re-reads it on every request |
| Pod Identity credentials of `<stack>-autopilot-dispatcher-role` | EKS Pod Identity agent, per pod | code running in that pod | short-lived STS credentials |
| ack-bot PAT (`ack/prow/github-pat-token`) | `prow-secrets` CSI mount, as today through `preset-github-secrets` | code running in that pod | long-lived (Plan 1's `ack-autopilot` App replaces it before the Dispatcher writes the ledger) |

Code in the Dispatcher pod is test-infra `main`, built in the pod at every tick
(`go build -mod=mod`): whoever can merge to test-infra `main`, or publish a Go
module version that test-infra resolves, controls these credentials. That is
the same trust as every other test-infra periodic. Keep the dependency set
small: the launcher uses no client-go, no sigs.k8s.io/prow and no new AWS SDK
module (only the core module's SigV4 signer and `config`, already required).

## What the token can do

RBAC (`rbac.yaml`): `create`, `get`, `list`, `patch` on `prowjobs` in `prow`.
No `update`, `delete`, `deletecollection`, `watch`; nothing else in any
namespace; no ClusterRole.

- **Why `patch`.** Abort sets `status.state=aborted` (Prow's plank then deletes
  the pod: `syncAbortedJob`, sigs.k8s.io/prow v20260908 `pkg/plank/reconciler.go`).
  The ProwJob CRD has `subresources: {}` (no status subresource,
  `flux/prow/crds/prowjob_customresourcedefinition.yaml`), so this is a patch
  of the object. The 2026-11-13 decision listed `create/get/list/watch`; Plan 7
  replaces `watch` (never used: each tick polls) with `patch` (decision 2).
- **What RBAC cannot stop.** A ProwJob is a pod spec Prow runs with its own
  identity. With `create` alone, the token could start any image, as any
  ServiceAccount, with any secret in `test-pods` (including the ack-bot PAT and
  the postsubmit role's ECR publish rights), or privileged pods on the build
  cluster. With `patch` it could rewrite any job's spec. agent-plugin's
  ClusterRole has the same reach today (`create/get/list/watch/update/patch`
  on `prowjobs`, cluster-wide).
- **What closes it: `admission-policy.yaml`.** A ValidatingAdmissionPolicy that
  matches only this ServiceAccount's requests (`failurePolicy: Fail`,
  `Deny`). CREATE: only the three allowlisted job names, named by a launch id,
  labelled `ack-autopilot/managed-by=autopilot-dispatcher`; for each, exactly
  the cluster, ServiceAccount, image repository and tag prefix, command,
  secret, SecretProviderClass and volumes `prowlaunch.Build` makes; no
  `spec.namespace`, `refs`, init or ephemeral containers, host namespaces,
  `envFrom`, credential secrets in the decoration config, or other utility
  images; extra_refs only `main` of four org repos; only the annotations
  `Build` sets (Prow copies a job's annotations to its pod, so no AppArmor,
  seccomp or other pod annotation), with `karpenter.sh/do-not-disrupt: "true"`
  only on `autopilot-sdk-bump`; `agent-workflow-triage` only with no volume,
  no secret in its environment, no extra ref and no security context, and
  named by a triage launch id (`-t<n>`, which no other job may carry).
  UPDATE: only an abort of a
  job it made (spec, labels and annotations unchanged, state `aborted`).
  `prowlaunch.TestAdmissionPolicyAllowsWhatBuildMakes` fails when `Build`
  gains an annotation or secret volume the policy does not name.
- **Residual with the policy applied.** The token can start the three workflows
  for any args the policy does not check (service, resource, field), so a
  stolen token can start up to "add-field on any controller" runs (5 h, e2e
  pod, ack-agent fork PAT) and sdk-bump runs (ack-bot token, pushes only
  `autopilot/<svc>/sdk-bump`, Plan 6's push guard) and triage runs (30 min,
  no token, one Bedrock call each). It can abort only the
  Dispatcher's own jobs, and read their state.

## What the Dispatcher can launch (code-side controls)

- **Allowlist** (`prowlaunch.Allowed`): `add-field` -> `agent-workflow-add-field`
  (from `prow/agent-workflows/agent-workflows.yaml`, the same entry agent-plugin
  uses) and `sdk-bump` -> `autopilot-sdk-bump` (defined in code, kept out of
  `agent-workflows.yaml` so no `/agent` comment can start it with ack-bot's
  token), and `triage` -> `agent-workflow-triage` (from `agent-workflows.yaml`;
  Plan 8). Plan 9 adds `add-resource` -> `agent-workflow-add-resource` (from
  `agent-workflows.yaml`, add-field's pod shape: the admission policy's
  `agentJob` rules) and `dod` -> `autopilot-dod` (defined in code: no secret,
  no volume, no privilege, `pre-submit-service-account` on the default cluster,
  the controller repo at `main` as its only extra ref beyond add-field's).
  `address-feedback` is refused.
- **Why a separate dod job** (Plan 9 decision 1): the authoring pod
  (add-field, add-resource) runs an LLM with a GitHub token and can write any
  file in its artifacts, including a passing `verdict.json` for its real head
  tree (the Plan 3 finding). The Dispatcher therefore never reads a
  `verdict.json` from an agent job (`Launcher.Verdict` refuses any workflow but
  `sdk-bump` and `dod`), and gates agent PRs with its own job. sdk-bump's
  in-process verdict is trusted because that pod runs no LLM; it is still bound
  to the PR body's `Gate-Verdict` trailer and checked against the head with
  `dod.VerifyWith` (tree and ancestry through the GitHub API).
  The dod job itself runs the PR's code in its `build` check (go build, vet,
  test, mod tidy). So that this code cannot forge the verdict (Plan 9 review
  F1), dispatched mode runs every build step through
  `scripts/autopilot-dod-sandbox.sh` (`--build-sandbox`): as uid/gid 65532
  (`setpriv --clear-groups --no-new-privs`), with a clean environment and its
  own HOME, Go caches and TMPDIR, and every process of that uid is killed
  (`pkill -9 -U 65532`) after each step; a survivor fails the step.
  `verdict.json` and `gate-verdict.txt` are written only by root: first into a
  private `mktemp -d` directory, then copied into `$ARTIFACTS`, which must be
  root-owned and is made 0755. It fails closed: if the pod cannot `setpriv`
  (not root, no `setpriv`/`pkill`/`pgrep`), the build check records
  `BUILD_SANDBOX_FAILED`, nothing runs unsandboxed, and the Dispatcher counts
  it as an environment failure (RETRY_ENV, then ESCALATED); it never passes.
- **Args**: every arg must be in the workflow's `required_args`/`optional_args`
  and match a fixed shape (`prowlaunch` `validators`); `work-item` must be the
  launch id's key hash; `change-class` must be the workflow's class. Triage has
  its own shapes (`prowlaunch` `workflowValidators`: `finding-class`, any
  lower-case detector work name, change class `config-only` or
  `hand-written-code`). Args reach the pod as container args or environment
  variables, never through a shell.
- **Names**: the job name is the launch id
  (`autopilot-<16 hex>-g<gen>-(r<run>|f<8 hex>|t<run>|d<run>)`), so Launch is
  idempotent and adopts a job only when its `ack-autopilot/request` digest
  matches. A `t` id names only a triage run, and a triage run only a `t` id; a
  `d` id names only a dod job, and a dod job only a `d` id (Plan 9).
  add-field and add-resource jobs must have an `r` id.
- **Golden tests**: `prowlaunch.TestAddFieldMatchesAgentPlugin` and agent-plugin's
  `TestAutopilotGolden` pin the add-field job to what agent-plugin builds.
  The same pair pins add-resource (`TestAddResourceMatchesAgentPlugin`,
  `TestAutopilotGolden/add-resource`); `TestDodGolden` pins the dod job.
  `prowlaunch.TestTriageGolden` pins the triage job, which has **no** secrets
  volume, secret env, extra ref or privilege (`NewCatalog` refuses a triage
  entry with any of them, and the admission policy denies such a pod).
- **Residual**: a team member's `/agent triage` through agent-plugin still gets
  the `agent-secrets` volume (agent-plugin's generator mounts it on every pod);
  the Dispatcher never reconciles such a run.

## Log censoring

Prow's sidecar censors the values of **secret volumes** mounted in the test
container (`censor_secrets: true`); it does not read the Secrets Store CSI
mounts.
- `autopilot-sdk-bump` mounts `prowjob-github-pat-token` read-only at
  `/etc/github-censor` for that reason, as Plan 6's periodic and
  `autopilot-dispatch` do; its `GITHUB_TOKEN` is censored in the logs.
- `agent-workflow-add-field` is agent-plugin's job byte for byte (golden):
  its `agent-github-pat-token` reaches the pod as an env var and through the
  `agent-secrets` CSI mount only, so the sidecar does not censor it. That is
  agent-plugin's behaviour today for every `/agent add-field`; changing it is
  an agent-plugin change (and the golden moves with it). Reviewer: decide
  whether it blocks going live.

## Results are untrusted

The agents share the pod and can write `agent-result.json`. The Dispatcher:
- reads at most 64 KiB (`dispatcher.MaxResultBytes`), from one key shape;
- requires the file's `work_item`, `prow_job_id` and `workflow` to be the
  job's;
- cross-checks the outcome against the job's exit state (a failed job may only
  report `failed`/`needs_human`, a succeeded one only
  `pr_opened`/`pr_updated`/`completed_no_change`); a mismatch is retried as
  `RESULT_STATE_MISMATCH` and nothing from the file reaches the ledger;
- keeps only token-shaped reasons and PR links in the item's repo (Plan 2/3);
- reads a triage verdict only after the cross-check passes, and checks it
  again with `dispatcher.ParseTriage`, which fails closed to `undecided`; the
  worst case is a wrong proposal that waits for a maintainer.

Residual: an agent can still make a failed run look like a different failure,
or a succeeded run report "no change". Plan 10's gate-agreement record and
the ledger's `links.pr` (not trailers) are the checks after that.

## Launch ids are used once

A launch id is consumed once the ledger records it (the item's claim and
`links.prowjobs`; `attempts.runs` picks the next run's id). The launcher does
not remember ids after Prow's sinker deletes a job; it need not: a claimed run
whose job is gone reads as unknown, is reaped after its deadline plus grace,
and the next run gets a new id (`dispatcher.TestTickNeverRelaunchesAGoneJob`).
Refresh ids are recorded in the bump's `links.prowjobs` the same way. The gap
is a tick whose ledger push is lost: the next tick repeats the same id and
adopts the job while it exists (the sinker keeps jobs for days; a tick is 30
minutes), and the inventory counts it against the caps.

## Kill switches and caps

| Switch | Who flips it | Effect |
|---|---|---|
| `AUTOPILOT_LAUNCHER_ENABLED` in `autopilot-dispatch.tpl` | test-infra PR | not `"true"`: the prow launcher is refused; nothing reconciled or launched |
| `launcher: shadow` in `policy/autopilot.yaml` | ack-private PR | no runs start; runs in flight report unknown and are reaped after deadline + grace |
| `paused: true` | ack-private PR | no launches (runs in flight continue) |
| `abort_inflight: true` | ack-private PR | every run in flight (and every orphan) is aborted on the next tick, its item ESCALATED `ABORTED_BY_POLICY`; every triage run in flight is aborted too (its item returns to `DETECTED`, `TRIAGE_FAILED ABORTED_BY_POLICY`); nothing launches |
| `paused_repos`, `services` | ack-private PR | per repo / per service |
| delete the RoleBinding | cluster admin | every create, read and abort fails; the tick reports problems and changes nothing |

Caps (`policy/budgets.yaml`, design §8) plus a code ceiling: the config-only
lane never runs more than 2 at once whatever the budgets say. Runs in flight
that no item claims (a lost ledger push) are counted against the caps, and if
the run list cannot be read, nothing launches. Triage has its own caps
(`policy/triage.yaml`, default 1 at once and 10 a day); without that file no
triage run starts. A tick is at most 30 minutes
apart, so the time to stop everything is one tick after the switch merges.

## Blast radius (worst case per lane, Phase 1 caps)

| Lane | At once | Per day | Each run can |
|---|---|---|---|
| config-only (add-field) | 2 | bounded by caps and `max_runs_per_item` | 5 h privileged e2e pod on the build cluster; push to the ack-agent fork; open a PR (human-merge); real-AWS e2e in the agent test account |
| SDK-bump | 1 per repo | 5 (`max_sdk_bump_launches_per_day`) | 3 h on 8 CPU / 8 Gi, not evicted by Karpenter (`do-not-disrupt`); push `autopilot/<svc>/sdk-bump` with ack-bot's token; open a draft PR |
| sdk-bump refresh | one per change of a bump's dependents | small | edit the bump PR's body, labels, status and draft state |
| new-resource (add-resource, Plan 9) | 1 (lane `MaxRunning`) | 1 (`policy/new-resource.yaml`, ceiling 1); off unless that file says `enabled: true`; 1 open PR per repo | as add-field: 5 h privileged e2e pod; push to the ack-agent fork; open a **draft** PR that stays draft (draft level) |
| dod gate (Plan 9) | 1 per item in GATING; holds the repo like a run | at most `ledger.MaxEnvRetries` (2) env retries per head, plus one per head move | 2 h unprivileged pod, no credentials; reads public GitHub and the controller at `main`; writes only `verdict.json` and `gate-verdict.txt` |
| PR ready (Plan 9) | one GraphQL call per gated PR | once per gate pass | `markPullRequestReadyForReview` on a PR `links.pr` names, by the bot the lane names, after a passing verdict for its current head; never while `autopilot/blocked-server-default` is on; never in a dry run |
| triage | 1 (max_concurrent_triage_runs) | 10 (max_triage_launches_per_day) | 30-min unprivileged pod on the build cluster as workflow-runner; no GitHub token or secrets volume; reads public GitHub; one tool-less Bedrock Converse call through workflow-runner's Pod Identity (which can do more than that: Plan 8 carry-over); writes only agent-result.json |

All PRs stay human-merge (Phases 1-4); Plan P's rulesets make that a control.

## Going live (all required, in order)

- [ ] Plan P done: org rulesets on `main` of every controller repo (bypass: Tide only), ack-bot not admin.
- [ ] This review signed off by the builder-security reviewer.
- [ ] Plan 1 Task 12 done (ledger writes on, with the dedicated token).
- [ ] Apply `serviceaccount.yaml`, `rbac.yaml`, then `admission-policy.yaml` (values resolved; `kubectl apply --server-side`).
- [ ] Verify the policy before anything else: as the ServiceAccount (`kubectl create token autopilot-dispatcher -n test-pods`), a create of a job with another image is denied, and `kubectl auth can-i --as=system:serviceaccount:test-pods:autopilot-dispatcher delete prowjobs -n prow` says no.
- [ ] Apply `pod-identity.yaml` (values resolved) and wait for the ACK Role and PodIdentityAssociation to be synced.
- [ ] test-infra PR: `serviceAccountName: autopilot-dispatcher` and `AUTOPILOT_LAUNCHER_ENABLED: "true"` in `autopilot-dispatch.tpl`; `make prow-gen`.
- [ ] Watch one dry tick on the new ServiceAccount (`AUTOPILOT_LEDGER_WRITE` unchanged): no `problem inventory:` line, Status reads work.
- [ ] ack-private PR: `launcher: prow`, `services:` reduced to one low-risk service first.
- [ ] Plan 9, before any lane gates for real: verify the `autopilot-dod` rule too (a dod job with a secret env, a volume or another controller repo is denied), and that `pre-submit-service-account` in `test-pods` carries no AWS identity (Plan 5 Open risk 7).
- [ ] Plan 9, before any lane gates for real: one staging `autopilot-dod` run shows the `build` check passing under the sandbox (its log has no `build sandbox probe` failure and the verdict has no `build:BUILD_SANDBOX_FAILED`), so the pod can `setpriv` to uid 65532.
- [ ] Plan 9, to mark drafts ready: a test-infra PR adds `AUTOPILOT_PR_GITHUB_TOKEN` (a controller-repo token with Pull requests: write; ack-bot's PAT in Phase 1) to `autopilot-dispatch.tpl` and sets `AUTOPILOT_PR_WRITE: "true"`. Until then every gated draft stays draft and a person marks it ready.
- [ ] After the first runs: check the Dispatcher's own logs upload (`OwnLogs` in `pod-identity.yaml` is the minimum Prow needs; widen only inside `logs/autopilot-dispatch/`).

## Rollback

`launcher: shadow` (ledger PR) or `AUTOPILOT_LAUNCHER_ENABLED: "false"` (test-infra
PR) stops new runs; `abort_inflight: true` stops running ones. Deleting the
RoleBinding is the cluster-side stop. Items left RUNNING are reaped after
their deadline plus grace (Plan 2).
