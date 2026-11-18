#!/usr/bin/env bash
# Runs ack-dod (ack-build-tools autopilot dod; ACK Autopilot Plan 5) on the
# controller PR a Prow presubmit checked out, the way the sdk-bump workflow
# (Plan 6) will. Writes $ARTIFACTS/verdict.json and $ARTIFACTS/gate-verdict.txt
# (and, dispatched, $ARTIFACTS/verdict.sig) and exits with dod's code: 0 pass,
# 1 fail, 2 no verdict.
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
# Dispatched mode (ACK Autopilot Plan 9): the Dispatcher's autopilot-dod job is
# a periodic with the controller at main as an extra ref, so Prow sets no
# PULL_* variables. It passes the PR instead, all read from the GitHub API:
#   AUTOPILOT_DOD_PR           PR number
#   AUTOPILOT_DOD_HEAD_SHA     the PR head to gate (40 hex)
#   AUTOPILOT_DOD_BASE_SHA     the PR's base commit (40 hex)
#   AUTOPILOT_DOD_PR_AUTHOR    the PR author's login
#   TEST_INFRA_ORG             the org of the controller repo (default
#                              aws-controllers-k8s)
# The script fetches pull/<pr>/head and gives no verdict (exit 2) unless it is
# AUTOPILOT_DOD_HEAD_SHA: the head moved, and the Dispatcher gates the new one.
# In dispatched mode the build check runs the PR's code as an unprivileged uid
# (autopilot-dod-sandbox.sh; Plan 9 review F1), $ARTIFACTS must be owned by
# this (root) uid and is made 0755, and the verdict and trailer are written to
# a private directory first and copied into $ARTIFACTS after dod exits.
#   AUTOPILOT_DOD_SIGNING_KEY  the verdict signing key (default
#                              /etc/autopilot-dod-signing/key, the secret
#                              autopilot-dod-signing-key mounted 0400, root
#                              only). dod signs the exact verdict bytes into
#                              verdict.sig; the Dispatcher trusts a dod verdict
#                              only with a valid signature, because presubmits
#                              can write the logs bucket (Plan 9 review). No
#                              key, no verdict (exit 2).
#
# dod needs no credentials. The dispatched job mounts only the signing key.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_INFRA_DIR="$(cd "$DIR/.." && pwd)"
WORKSPACE_DIR="$(cd "$TEST_INFRA_DIR/.." && pwd)"
CODEGEN_DIR="$WORKSPACE_DIR/code-generator"
RUNTIME_DIR="$WORKSPACE_DIR/runtime"
# No verdict (exit 2) when an input is missing.
die() { echo "autopilot-dod: $*" >&2; exit 2; }
[ -n "${SERVICE:-}" ] || die "SERVICE must be set"
[ -n "${PULL_BASE_SHA:-}${AUTOPILOT_DOD_HEAD_SHA:-}" ] || die "PULL_BASE_SHA must be set"
TARGET_DIR="$WORKSPACE_DIR/${SERVICE}-controller"
ARTIFACTS="${ARTIFACTS:-/tmp/artifacts}"
mkdir -p "$ARTIFACTS"

ORG="${TEST_INFRA_ORG:-aws-controllers-k8s}"
OUT_DIR="$ARTIFACTS"
sandbox_args=()
sign_args=()
if [ -n "${AUTOPILOT_DOD_HEAD_SHA:-}" ]; then
  # Plan 9 review: sign the verdict. A Kubernetes secret volume's file is a
  # symlink into ..data/, and dod refuses a symlinked key, so resolve it.
  SIGN_KEY_PATH="${AUTOPILOT_DOD_SIGNING_KEY:-/etc/autopilot-dod-signing/key}"
  [ -f "$SIGN_KEY_PATH" ] || die "no verdict signing key at $SIGN_KEY_PATH"
  SIGN_KEY="$(realpath -e "$SIGN_KEY_PATH")" || die "could not resolve $SIGN_KEY_PATH"
  # Plan 9 review F1: the sandboxed build must not be able to write here.
  [ "$(stat -c %u "$ARTIFACTS")" = "$(id -u)" ] || die "$ARTIFACTS is not owned by uid $(id -u)"
  chmod 0755 "$ARTIFACTS" || die "could not chmod $ARTIFACTS"
  OUT_DIR="$(mktemp -d)" || die "could not create a private verdict directory"
  chmod 0700 "$OUT_DIR"
  sandbox_args=(--build-sandbox "$DIR/autopilot-dod-sandbox.sh")
  sign_args=(--sign-key "$SIGN_KEY" --signature-out "$OUT_DIR/verdict.sig")
  [[ "${AUTOPILOT_DOD_PR:-}" =~ ^[1-9][0-9]{0,9}$ ]] || die "AUTOPILOT_DOD_PR must be a PR number"
  [[ "$AUTOPILOT_DOD_HEAD_SHA" =~ ^[0-9a-f]{40}$ ]] || die "AUTOPILOT_DOD_HEAD_SHA must be a full commit SHA"
  [[ "${AUTOPILOT_DOD_BASE_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || die "AUTOPILOT_DOD_BASE_SHA must be a full commit SHA"
  [[ "${AUTOPILOT_DOD_PR_AUTHOR:-}" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}$ ]] || die "AUTOPILOT_DOD_PR_AUTHOR must be a GitHub login"
  [[ "$ORG" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}$ ]] || die "TEST_INFRA_ORG must be a GitHub org"
  git -C "$TARGET_DIR" fetch --quiet --no-tags origin "pull/${AUTOPILOT_DOD_PR}/head" || die "could not fetch pull/${AUTOPILOT_DOD_PR}/head"
  fetched="$(git -C "$TARGET_DIR" rev-parse FETCH_HEAD)"
  [ "$fetched" = "$AUTOPILOT_DOD_HEAD_SHA" ] || die "pull/${AUTOPILOT_DOD_PR}/head is $fetched, not $AUTOPILOT_DOD_HEAD_SHA: the head moved"
  git -C "$TARGET_DIR" cat-file -e "${AUTOPILOT_DOD_BASE_SHA}^{commit}" || die "base $AUTOPILOT_DOD_BASE_SHA is not in the controller checkout"
  PULL_PULL_SHA="$AUTOPILOT_DOD_HEAD_SHA"
  PULL_BASE_SHA="$AUTOPILOT_DOD_BASE_SHA"
  PR_AUTHOR="$AUTOPILOT_DOD_PR_AUTHOR"
  PR_NUMBER="$AUTOPILOT_DOD_PR"
fi
[ -n "${PULL_PULL_SHA:-}" ] || die "PULL_PULL_SHA must be set"
[ -n "${AUTOPILOT_DOD_CLASS:-}" ] || die "AUTOPILOT_DOD_CLASS must be set (from the ledger)"
[ -n "${AUTOPILOT_DOD_WORK_ITEM:-}" ] || die "AUTOPILOT_DOD_WORK_ITEM must be set (from the ledger)"
if [ -z "${AUTOPILOT_DOD_HEAD_SHA:-}" ]; then
  PR_AUTHOR="$(jq -r '.refs.pulls[0].author // empty' <<<"${JOB_SPEC:-}")" || die "JOB_SPEC is not JSON"
  [ -n "$PR_AUTHOR" ] || die "JOB_SPEC has no PR author"
  PR_NUMBER="$(jq -r '.refs.pulls[0].number // empty' <<<"${JOB_SPEC:-}")"
fi

PR_BODY="${AUTOPILOT_DOD_PR_BODY_FILE:-}"
if [ -z "$PR_BODY" ]; then
  [ -n "$PR_NUMBER" ] || die "JOB_SPEC has no PR number"
  PR_BODY="$ARTIFACTS/pr-body.md"
  curl -fsSL "https://api.github.com/repos/${ORG}/${SERVICE}-controller/pulls/${PR_NUMBER}" \
    | jq -r '.body // ""' > "$PR_BODY" || die "could not read the PR body"
fi

(cd "$TEST_INFRA_DIR" && go build -mod=mod -o /tmp/ack-build-tools ./prow/jobs/tools/cmd) || die "could not build ack-build-tools"
make -C "$CODEGEN_DIR" build-ack-generate || die "could not build ack-generate"

args=(autopilot dod
  --repo-dir "$TARGET_DIR" --repo "${ORG}/${SERVICE}-controller" --service "$SERVICE"
  --base-ref "$PULL_BASE_SHA" --head-ref "$PULL_PULL_SHA" --change-class "$AUTOPILOT_DOD_CLASS"
  --work-item "$AUTOPILOT_DOD_WORK_ITEM"
  --pr-author "$PR_AUTHOR" --bot-login "${AUTOPILOT_DOD_BOT_LOGIN:-ack-bot}"
  --bot-email "${AUTOPILOT_DOD_BOT_EMAIL:-ack-bot@users.noreply.github.com}"
  --bot-email 82905295+ack-bot@users.noreply.github.com
  --pr-body-file "$PR_BODY"
  --codegen-dir "$CODEGEN_DIR" --runtime-dir "$RUNTIME_DIR"
  --ack-generate "$CODEGEN_DIR/bin/ack-generate"
  --verdict-out "$OUT_DIR/verdict.json" --trailer-out "$OUT_DIR/gate-verdict.txt"
  ${sandbox_args[@]+"${sandbox_args[@]}"}
  ${sign_args[@]+"${sign_args[@]}"})

set +e
/tmp/ack-build-tools "${args[@]}"
code=$?
set -e
if [ "$OUT_DIR" != "$ARTIFACTS" ]; then
  for f in verdict.json gate-verdict.txt verdict.sig; do
    if [ -f "$OUT_DIR/$f" ]; then
      cp "$OUT_DIR/$f" "$ARTIFACTS/$f" || die "could not copy $f into $ARTIFACTS"
    fi
  done
fi
echo "ack-dod exit code $code (0 pass, 1 fail, 2 no verdict)"
exit "$code"
