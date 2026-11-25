#!/usr/bin/env bash
# One ACK Autopilot sdk-bump run (Plan 6; ack-build-tools autopilot sdk-bump) for
# one controller: regenerate at the ledger item's SDK model, gate with ack-dod,
# and publish a draft PR from autopilot/<service>/sdk-bump.
#
# Rendered only for services in jobs_config.yaml autopilot_sdk_bump_services
# (empty in Phase 1). AUTOPILOT_SDK_BUMP_WRITE=true adds --write; anything else
# is a dry run that writes nothing to GitHub and pushes nothing.
#
# Environment:
#   SERVICE                        service alias (required)
#   SDK_TO, WORK_ITEM              target and key hash (Plan 7 passes them);
#                                  unset = the service's open item in the ledger
#   AUTOPILOT_SDK_BUMP_MODE        bump (default) or refresh
#   AUTOPILOT_SDK_BUMP_WRITE       "true" to push and write to GitHub
#   AUTOPILOT_ORG                  default aws-controllers-k8s
#   AUTOPILOT_LEDGER_REPO_NAME     default ack-private (read only)
#   AUTOPILOT_CRD_COMPAT_REF       code-generator ref with crd-compat-check
#                                  --output json (default main)
#   AUTOPILOT_GIT_NAME/EMAIL/LOGIN default ack-bot / ack-bot@users.noreply.github.com / ack-bot
#   GITHUB_TOKEN                   ack-bot's token (preset-github-secrets)
#   ARTIFACTS                      set by Prow
#
# Do not add `set -x`: GIT_ASKPASS hands the token to git.
set -euo pipefail
unset GIT_TRACE GIT_TRACE_CURL GIT_CURL_VERBOSE GIT_TRACE_PACKET GIT_TRACE_SETUP \
  GIT_TRACE2 GIT_TRACE2_EVENT GIT_TRACE2_PERF

: "${SERVICE:?SERVICE must be set}"
ORG="${AUTOPILOT_ORG:-aws-controllers-k8s}"
LEDGER_REPO_NAME="${AUTOPILOT_LEDGER_REPO_NAME:-ack-private}"
CRD_COMPAT_REF="${AUTOPILOT_CRD_COMPAT_REF:-main}"
MODE="${AUTOPILOT_SDK_BUMP_MODE:-bump}"
WRITE="${AUTOPILOT_SDK_BUMP_WRITE:-false}"
ARTIFACTS="${ARTIFACTS:-/tmp/artifacts}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_INFRA_DIR="$(cd "$DIR/.." && pwd)"
WORK="$(mktemp -d)"
mkdir -p "$ARTIFACTS"

askdir="$(mktemp -d)"
trap 'rm -rf "$askdir"' EXIT
if [ -n "${GITHUB_TOKEN:-}" ]; then
  # shellcheck disable=SC2016  # expands when git runs the helper
  printf '#!/bin/sh\nprintf "%%s\\n" "$GITHUB_TOKEN"\n' > "$askdir/askpass.sh"
  chmod 700 "$askdir/askpass.sh"
  export GIT_ASKPASS="$askdir/askpass.sh" GIT_TERMINAL_PROMPT=0
fi
clone() { git -c credential.helper= clone -q "$@"; }

(cd "$TEST_INFRA_DIR" && go build -mod=mod -o /tmp/ack-build-tools ./prow/jobs/tools/cmd)
clone "https://github.com/$ORG/${SERVICE}-controller.git" "$WORK/controller"
clone "https://github.com/$ORG/code-generator.git" "$WORK/code-generator"
clone "https://github.com/$ORG/runtime.git" "$WORK/runtime"
git -C "$WORK/code-generator" worktree add -q --detach "$WORK/crd-compat" "$CRD_COMPAT_REF"
make -C "$WORK/crd-compat" build-ack-generate

args=(autopilot sdk-bump --service "$SERVICE" --mode "$MODE" --org "$ORG"
  --controller-dir "$WORK/controller" --codegen-repo "$WORK/code-generator" --runtime-repo "$WORK/runtime"
  --ack-generate "$WORK/crd-compat/bin/ack-generate" --cache-dir "$WORK/cache"
  --bot-name "${AUTOPILOT_GIT_NAME:-ack-bot}" --bot-email "${AUTOPILOT_GIT_EMAIL:-ack-bot@users.noreply.github.com}"
  --bot-login "${AUTOPILOT_GIT_LOGIN:-ack-bot}" --artifacts-dir "$ARTIFACTS")
if [ -n "${GITHUB_TOKEN:-}" ] && clone --depth 1 "https://x-access-token@github.com/$ORG/$LEDGER_REPO_NAME.git" "$WORK/ledger"; then
  args+=(--ledger-dir "$WORK/ledger/autopilot")
else
  echo "autopilot-sdk-bump: ledger not readable; server-default dependents are unknown and the PR stays blocked" >&2
fi
if [ -n "${SDK_TO:-}" ]; then
  args+=(--sdk-to "$SDK_TO" --work-item "${WORK_ITEM:?WORK_ITEM must come with SDK_TO}" --change-class SDK-bump)
else
  args+=(--from-ledger)
fi
if [ "$WRITE" = "true" ]; then
  args+=(--write)
fi

set +e
/tmp/ack-build-tools "${args[@]}"
code=$?
set -e
echo "autopilot-sdk-bump: exit code $code (0 pr_opened/no change, 1 needs_human/failed, 2 no result)"
exit "$code"
