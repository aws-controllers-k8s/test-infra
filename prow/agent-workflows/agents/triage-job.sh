#!/bin/bash
# ACK Autopilot triage workflow, mode api (Plan 8). The Dispatcher starts it
# for one needs-config ledger item; it writes one closed-enum decision into
# agent-result.json (workflows/result.py, key "triage").
#
# It reads only public data: the controller repository at main and the AWS
# SDK model at --sdk-to. It holds no GitHub token, pushes nothing and
# comments nowhere. The model call has no tools (workflows/triage.py).
#
# Arguments (the Dispatcher's; prow/agent-workflows/templates/triage.tpl):
#   --service --resource --field --finding-class --detector-work
#   --change-class --sdk-to --work-item   required
#   --model                               optional Bedrock model id
# Environment:
#   GITHUB_ORG   org of the controller repos (default aws-controllers-k8s)
#   ARTIFACTS    where agent-result.json goes (set by Prow)
#   TRIAGE_CONTROLLER_DIR, TRIAGE_SDK_MODEL, TRIAGE_MODEL_RESPONSE_FILE
#                offline runs only: a local controller checkout, a local
#                model file, and a canned answer instead of Bedrock.
#                triage.tpl never sets them.
# Exit: 0 completed_no_change, 1 needs_human or failed, 2 no result file.
# The Dispatcher checks the recorded outcome against this exit code (Plan 7
# dispatcher.consistent): keep the two in step.
set -euo pipefail

SCRIPT_NAME="triage-job.sh"
WORKFLOW_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULT_PY="$WORKFLOW_DIR/workflows/result.py"
export AGENT_RESULT_FILE="${ARTIFACTS:-/tmp/ack-test-logs}/agent-result.json"
mkdir -p "$(dirname "$AGENT_RESULT_FILE")"
python "$RESULT_PY" init --workflow triage \
  || echo "$SCRIPT_NAME][WARN] could not create $AGENT_RESULT_FILE" >&2

set_result() {
  python "$RESULT_PY" set --outcome "$1" --reason "$2" \
    || echo "$SCRIPT_NAME][WARN] could not record $1 $2" >&2
}

SERVICE="" RESOURCE="" FIELD="" FINDING_CLASS="" DETECTOR_WORK=""
CHANGE_CLASS="" SDK_TO="" WORK_ITEM="" MODEL=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --service|--resource|--field|--finding-class|--detector-work|--change-class|--sdk-to|--work-item|--model)
      if [[ $# -lt 2 ]]; then
        echo "$SCRIPT_NAME][ERROR] $1 needs a value" >&2
        set_result needs_human INVALID_INPUT
        exit 1
      fi
      case "$1" in
        --service) SERVICE="$2" ;;
        --resource) RESOURCE="$2" ;;
        --field) FIELD="$2" ;;
        --finding-class) FINDING_CLASS="$2" ;;
        --detector-work) DETECTOR_WORK="$2" ;;
        --change-class) CHANGE_CLASS="$2" ;;
        --sdk-to) SDK_TO="$2" ;;
        --work-item) WORK_ITEM="$2" ;;
        --model) MODEL="$2" ;;
      esac
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

VALIDATE=(--triage --service "$SERVICE" --resource "$RESOURCE" --field "$FIELD"
  --finding-class "$FINDING_CLASS" --detector-work "$DETECTOR_WORK"
  --change-class "$CHANGE_CLASS" --sdk-to "$SDK_TO" --work-item "$WORK_ITEM")
[ -n "$MODEL" ] && VALIDATE+=(--model "$MODEL")
if ! python "$WORKFLOW_DIR/workflows/validation.py" "${VALIDATE[@]}"; then
  set_result needs_human INVALID_INPUT
  exit 1
fi
python "$RESULT_PY" set --work-item "$WORK_ITEM" \
  || echo "$SCRIPT_NAME][WARN] could not record the work item" >&2

ORG="${GITHUB_ORG:-aws-controllers-k8s}"
if ! [[ "$ORG" =~ ^[A-Za-z0-9-]+$ ]]; then
  echo "$SCRIPT_NAME][ERROR] GITHUB_ORG must be an org name" >&2
  set_result needs_human INVALID_INPUT
  exit 1
fi

cd "$WORKFLOW_DIR"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

CONTROLLER_DIR="${TRIAGE_CONTROLLER_DIR:-}"
if [ -z "$CONTROLLER_DIR" ]; then
  CONTROLLER_DIR="$WORK/$SERVICE-controller"
  # Public, read-only, no credential helper: the pod holds no token.
  if ! git -c credential.helper= clone -q --depth 1 \
      "https://github.com/$ORG/$SERVICE-controller.git" "$CONTROLLER_DIR"; then
    set_result failed CONTROLLER_CLONE_FAILED
    exit 1
  fi
fi

SDK_MODEL="${TRIAGE_SDK_MODEL:-}"
if [ -z "$SDK_MODEL" ]; then
  if ! URL="$(python -m workflows.triage model-url --service "$SERVICE" --sdk-to "$SDK_TO" \
      --controller-dir "$CONTROLLER_DIR")"; then
    set_result needs_human CONTROLLER_UNREADABLE
    exit 1
  fi
  SDK_MODEL="$WORK/model.json"
  if ! curl -fsSL --retry 3 --max-time 300 --max-filesize 67108864 -o "$SDK_MODEL" "$URL"; then
    set_result failed SDK_MODEL_FETCH_FAILED
    exit 1
  fi
fi

RUN=(run --service "$SERVICE" --resource "$RESOURCE" --field "$FIELD"
  --finding-class "$FINDING_CLASS" --detector-work "$DETECTOR_WORK"
  --change-class "$CHANGE_CLASS" --sdk-to "$SDK_TO" --work-item "$WORK_ITEM"
  --controller-dir "$CONTROLLER_DIR" --sdk-model "$SDK_MODEL")
[ -n "$MODEL" ] && RUN+=(--model "$MODEL")
[ -n "${TRIAGE_MODEL_RESPONSE_FILE:-}" ] && RUN+=(--model-response-file "$TRIAGE_MODEL_RESPONSE_FILE")
set +e
python -m workflows.triage "${RUN[@]}"
code=$?
set -e
exit "$code"
