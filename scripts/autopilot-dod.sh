#!/usr/bin/env bash
# Runs ack-dod (ack-build-tools autopilot dod; ACK Autopilot Plan 5) on the
# controller PR a Prow presubmit checked out, the way the sdk-bump workflow
# (Plan 6) will. Writes $ARTIFACTS/verdict.json and $ARTIFACTS/gate-verdict.txt
# and exits with dod's code: 0 pass, 1 fail, 2 no verdict.
#
# Rendered only for services in jobs_config.yaml autopilot_dod_services (empty
# in Phase 1), as an optional job started by hand: /test <service>-autopilot-dod.
#
# Environment:
#   SERVICE                  service alias (required)
#   PULL_BASE_SHA            the PR's base commit (set by Prow; required)
#   ARTIFACTS                where the verdict goes (set by Prow)
#   AUTOPILOT_DOD_CLASS      change class (default: the head commit's
#                            Change-Class trailer, else SDK-bump)
#   AUTOPILOT_DOD_BOT_EMAIL  author email of autopilot commits
#                            (default ack-bot@users.noreply.github.com)
#
# dod needs no credentials, and this job mounts none.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_INFRA_DIR="$(cd "$DIR/.." && pwd)"
WORKSPACE_DIR="$(cd "$TEST_INFRA_DIR/.." && pwd)"
CODEGEN_DIR="$WORKSPACE_DIR/code-generator"
RUNTIME_DIR="$WORKSPACE_DIR/runtime"
: "${SERVICE:?SERVICE must be set}"
: "${PULL_BASE_SHA:?PULL_BASE_SHA must be set}"
TARGET_DIR="$WORKSPACE_DIR/${SERVICE}-controller"
ARTIFACTS="${ARTIFACTS:-/tmp/artifacts}"
mkdir -p "$ARTIFACTS"

(cd "$TEST_INFRA_DIR" && go build -mod=mod -o /tmp/ack-build-tools ./prow/jobs/tools/cmd)
make -C "$CODEGEN_DIR" build-ack-generate

trailer() {
  git -C "$TARGET_DIR" log -1 --format="%(trailers:key=$1,valueonly,separator=%x20)" HEAD
}
CLASS="${AUTOPILOT_DOD_CLASS:-$(trailer Change-Class)}"
CLASS="${CLASS:-SDK-bump}"
WORK_ITEM="$(trailer Work-Item)"

args=(autopilot dod
  --repo-dir "$TARGET_DIR" --repo "aws-controllers-k8s/${SERVICE}-controller" --service "$SERVICE"
  --base-ref "$PULL_BASE_SHA" --head-ref HEAD --change-class "$CLASS"
  --bot-email "${AUTOPILOT_DOD_BOT_EMAIL:-ack-bot@users.noreply.github.com}"
  --codegen-dir "$CODEGEN_DIR" --runtime-dir "$RUNTIME_DIR"
  --ack-generate "$CODEGEN_DIR/bin/ack-generate"
  --verdict-out "$ARTIFACTS/verdict.json" --trailer-out "$ARTIFACTS/gate-verdict.txt")
if [ -n "$WORK_ITEM" ]; then
  args+=(--work-item "$WORK_ITEM")
fi

set +e
/tmp/ack-build-tools "${args[@]}"
code=$?
set -e
echo "ack-dod exit code $code (0 pass, 1 fail, 2 no verdict)"
exit "$code"
