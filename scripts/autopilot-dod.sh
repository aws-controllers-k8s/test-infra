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
#   SERVICE                    service alias (required)
#   PULL_BASE_SHA              the PR's base commit (set by Prow; required)
#   PULL_PULL_SHA              the PR's head commit (set by Prow; required).
#                              Prow's checkout HEAD is a merge commit, so it
#                              is never used as the head.
#   JOB_SPEC                   Prow's job spec; .refs.pulls[0].author is the
#                              PR author (required)
#   ARTIFACTS                  where the verdict goes (set by Prow)
#   AUTOPILOT_DOD_CLASS        change class (required)
#   AUTOPILOT_DOD_WORK_ITEM    ledger key hash (required)
#                              Both come from the ledger through the job's
#                              trigger (Plan 7), never from the PR's commits
#                              or body, which the PR author controls. Until
#                              Plan 7 supplies them, the job gives no verdict.
#   AUTOPILOT_DOD_BOT_LOGIN    GitHub login of the autopilot bot (default
#                              ack-bot); the PR author decides "autopilot PR"
#   AUTOPILOT_DOD_BOT_EMAIL    author email of autopilot commits
#                              (default ack-bot@users.noreply.github.com;
#                              ack-bot's GitHub noreply address
#                              82905295+ack-bot@… is always accepted too)
#   AUTOPILOT_DOD_PR_BODY_FILE the PR description to scan for leaks (default:
#                              fetched anonymously from the GitHub API)
#
# dod needs no credentials, and this job mounts none.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_INFRA_DIR="$(cd "$DIR/.." && pwd)"
WORKSPACE_DIR="$(cd "$TEST_INFRA_DIR/.." && pwd)"
CODEGEN_DIR="$WORKSPACE_DIR/code-generator"
RUNTIME_DIR="$WORKSPACE_DIR/runtime"
# No verdict (exit 2) when an input is missing.
die() { echo "autopilot-dod: $*" >&2; exit 2; }
[ -n "${SERVICE:-}" ] || die "SERVICE must be set"
[ -n "${PULL_BASE_SHA:-}" ] || die "PULL_BASE_SHA must be set"
TARGET_DIR="$WORKSPACE_DIR/${SERVICE}-controller"
ARTIFACTS="${ARTIFACTS:-/tmp/artifacts}"
mkdir -p "$ARTIFACTS"

[ -n "${PULL_PULL_SHA:-}" ] || die "PULL_PULL_SHA must be set"
[ -n "${AUTOPILOT_DOD_CLASS:-}" ] || die "AUTOPILOT_DOD_CLASS must be set (from the ledger)"
[ -n "${AUTOPILOT_DOD_WORK_ITEM:-}" ] || die "AUTOPILOT_DOD_WORK_ITEM must be set (from the ledger)"
PR_AUTHOR="$(jq -r '.refs.pulls[0].author // empty' <<<"${JOB_SPEC:-}")" || die "JOB_SPEC is not JSON"
[ -n "$PR_AUTHOR" ] || die "JOB_SPEC has no PR author"
PR_NUMBER="$(jq -r '.refs.pulls[0].number // empty' <<<"${JOB_SPEC:-}")"

PR_BODY="${AUTOPILOT_DOD_PR_BODY_FILE:-}"
if [ -z "$PR_BODY" ]; then
  [ -n "$PR_NUMBER" ] || die "JOB_SPEC has no PR number"
  PR_BODY="$ARTIFACTS/pr-body.md"
  curl -fsSL "https://api.github.com/repos/aws-controllers-k8s/${SERVICE}-controller/pulls/${PR_NUMBER}" \
    | jq -r '.body // ""' > "$PR_BODY" || die "could not read the PR body"
fi

(cd "$TEST_INFRA_DIR" && go build -mod=mod -o /tmp/ack-build-tools ./prow/jobs/tools/cmd) || die "could not build ack-build-tools"
make -C "$CODEGEN_DIR" build-ack-generate || die "could not build ack-generate"

args=(autopilot dod
  --repo-dir "$TARGET_DIR" --repo "aws-controllers-k8s/${SERVICE}-controller" --service "$SERVICE"
  --base-ref "$PULL_BASE_SHA" --head-ref "$PULL_PULL_SHA" --change-class "$AUTOPILOT_DOD_CLASS"
  --work-item "$AUTOPILOT_DOD_WORK_ITEM"
  --pr-author "$PR_AUTHOR" --bot-login "${AUTOPILOT_DOD_BOT_LOGIN:-ack-bot}"
  --bot-email "${AUTOPILOT_DOD_BOT_EMAIL:-ack-bot@users.noreply.github.com}"
  --bot-email 82905295+ack-bot@users.noreply.github.com
  --pr-body-file "$PR_BODY"
  --codegen-dir "$CODEGEN_DIR" --runtime-dir "$RUNTIME_DIR"
  --ack-generate "$CODEGEN_DIR/bin/ack-generate"
  --verdict-out "$ARTIFACTS/verdict.json" --trailer-out "$ARTIFACTS/gate-verdict.txt")

set +e
/tmp/ack-build-tools "${args[@]}"
code=$?
set -e
echo "ack-dod exit code $code (0 pass, 1 fail, 2 no verdict)"
exit "$code"
