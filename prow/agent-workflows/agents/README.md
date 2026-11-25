# ACK agent workflows

Role-based agent workflows that change ACK service controllers. They run as
Prow jobs started by the agent-plugin (`/agent` comments and review events),
or locally from this directory.

## Workflows

| Workflow | CLI command | What it does |
|---|---|---|
| add-resource | `resource-addition` | Adds a new resource to an existing service controller |
| add-field | `field-addition` | Adds one field (identifier or dotted path) to an existing resource |
| address-feedback | `address-feedback` | Addresses team members' review feedback on an agent pull request (started by the agent-plugin from a review, never by `/agent`) |
| triage | `triage-job.sh` (not `python -m workflows`) | ACK Autopilot triage, mode `api`: one needs-config ledger item to one closed-enum decision. Started by the Dispatcher only |

The first three run one Strands graph (`roles/`):

1. **Planner** writes a plan from the AWS API model and the ack-dev-skills SOPs.
2. **Plan reviewer** approves it or asks for a revision (at most 1 re-plan).
3. **Implementer** applies the plan and builds the controller (at most 4 iterations).
4. **Implementation reviewer** returns `## Decision: APPROVE` or `## Decision: REVISE`. Anything unclear counts as REVISE (`roles/verdict.py`).
5. **E2E** runs `make kind-test` when `RUN_E2E=true`, with at most 2 fix attempts. SKIPPED counts as a failure (`roles/e2e.py`).
6. **PR writer** composes the pull request description.

The role prompts come from ack-dev-skills (`roles/*.md`), delivered to the
job as a Prow extra_ref.

## Run result

Every Prow run writes `$ARTIFACTS/agent-result.json` (schema
`ack.autopilot.result/v1`, `workflows/result.py`): outcome, reason, pull
request, model tokens, iterations and the e2e result. ACK Autopilot's
Dispatcher reads it. When `prow-job.sh` gets `--work-item` and
`--change-class` (the agent-plugin passes them from the workflow's
`optional_args` when the Dispatcher sets them; they are not
`python -m workflows` options), the run also adds `Producer`, `Work-Item`
and `Change-Class` trailers to its commit and pull request body. The
trailers record what the run was given, not where it came from: a team
member's `/agent` comment can pass the same arguments, so the ledger's link
to the pull request is the authoritative record.

The sdk-bump workflow (no LLM; `ack-build-tools autopilot sdk-bump`, Plan 6)
writes the same file from Go, with the same bytes `result.py` would write
(`autopilot/sdkbump/testdata/agent-result-sdk-bump-v1.json` is checked by
both test suites). It does not run `prow-job.sh` or `validation.py`; the Go
command validates its own inputs with the same rules.

## Triage (ACK Autopilot Plan 8)

`triage-job.sh` triages one "Needs config or code" ledger item. It is not a
Strands graph: it builds an evidence pack by code (`workflows/triage_evidence.py`:
the SDK model at `--sdk-to`, the controller's CRDs and `generator.yaml`), makes
one Bedrock Converse call with no tools (`utils/bedrock.converse_text`), and
checks the answer (`workflows/triage.py`): a closed-enum decision
(`resource`, `field-config`, `field-hook`, `not-worth-modelling`,
`undecided`) whose evidence references must exist in the model or the CRD.
Anything else becomes `undecided`. The verdict goes into `agent-result.json`
under `triage` (`ack.autopilot.triage/v1`). The job reads only public data and
gets no GitHub token. The Dispatcher turns the verdict into a proposal that a
maintainer accepts or rejects on the api-change issue; nothing is applied
without that reply.

Offline run (no network, no Bedrock):

    TRIAGE_CONTROLLER_DIR=tests/fixtures/triage/controller \
    TRIAGE_SDK_MODEL=tests/fixtures/triage/model.json \
    TRIAGE_MODEL_RESPONSE_FILE=tests/fixtures/triage/answer-field-hook.json \
    ARTIFACTS=/tmp/triage ./triage-job.sh --service s3 --resource Bucket \
      --field ObjectLockConfiguration.Rule.DefaultRetention.DefaultEventHold \
      --finding-class spec-field --detector-work from-sibling \
      --change-class hand-written-code --sdk-to service/s3/v1.114.10 \
      --work-item sha256:0123456789abcdef

## Running locally

Prerequisites: Python 3.10+ (the job image uses 3.12), [uv](https://github.com/astral-sh/uv), AWS
credentials with Amazon Bedrock access to the configured model, and
checkouts of code-generator, the service controller and ack-dev-skills.

```bash
uv sync
make list-workflows
make workflow-add-resource SERVICE=s3 RESOURCE=AccessPoint
make workflow-add-field SERVICE=s3 RESOURCE=Bucket FIELD=BucketKeyEnabled
```

### Arguments

- `--service`, `--resource`, `--field` (add-field; optional for address-feedback)
- `--model`: Bedrock model ID (default `us.anthropic.claude-opus-5-5`, `config/defaults.py`)
- `--aws-sdk-version`: aws-sdk-go-v2 version for `make build-controller` (default: detected from the controller)
- `--debug`: debug logging

### Environment variables

| Variable | Use |
|---|---|
| `CONTROLLER_DIR`, `CODEGEN_DIR`, `ACK_DEV_SKILLS_DIR`, `TEST_INFRA_DIR` | Checkouts the workflow reads and edits |
| `RUN_E2E` | `true` runs the e2e phase |
| `AGENT_MODEL_ID`, `AGENT_PLANNER_MODEL_ID`, `AGENT_IMPLEMENTER_MODEL_ID`, `AGENT_REVIEWER_MODEL_ID` | Model overrides |
| `AGENT_TEMPERATURE`, `AGENT_MAX_TOKENS`, `AGENT_SHELL_TIMEOUT` | Sampling, output cap and shell-command timeout |
| `AWS_REGION`, `AWS_DEFAULT_REGION` | Bedrock region, in that order (default `us-west-2`) |
| `AWS_SDK_GO_VERSION` | Fallback for `--aws-sdk-version` |
| `ARTIFACTS`, `AGENT_RESULT_FILE` | Where logs and `agent-result.json` go. `prow-job.sh` sets `AGENT_RESULT_FILE` to `${ARTIFACTS:-/tmp/ack-test-logs}/agent-result.json` and creates the file; the workflow only updates an existing file, so a local `python -m workflows` run writes no result |

## Tests

```bash
make test-offline   # no AWS and no model calls
```

## License

This project is licensed under the Apache License 2.0.
