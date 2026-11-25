#!/bin/sh
# One ACK Autopilot Dispatcher tick: ingests detect-api-changes issues, then
# reconciles, routes and (shadow-)launches work items in the ledger at
# <org>/ack-private under autopilot/ (the repo is AUTOPILOT_ISSUES_REPO), then
# keeps the status comment on each api-change issue current.
#
# Usage: autopilot-dispatch.sh <org>
# AUTOPILOT_LEDGER_WRITE=true commits and pushes; anything else is a dry run that
# only prints what would change. A rejected push (someone else pushed first) is a
# lost compare-and-swap: start again from the new main, up to 3 times. Never
# force-pushes, and commits only changes under autopilot/.
# AUTOPILOT_COMMENT=true posts the status comments (only with ledger writes on);
# anything else prints the planned comment edits.
# AUTOPILOT_METRICS_NAMESPACE and AUTOPILOT_METRICS_REGION (the periodic sets
# ACK/Autopilot, us-west-2) publish the tick's CloudWatch metrics (Plan 10), dry
# run or not; unset publishes nothing. A rejected push re-runs the tick, so its
# counts publish again for that attempt (rare: the job is the only writer).
#
# Optional overrides, for running against a sandbox repo (unset = production):
#   AUTOPILOT_ISSUES_REPO   repo with the issues and the ledger (default ack-private)
#   AUTOPILOT_BOT_LOGIN     account the issues are filed as (default ack-bot)
#   AUTOPILOT_TEAM_OWNER    org that owns the team (default: <org>)
#   AUTOPILOT_TEAM_SLUG     team whose members' commands count (default ack-admin)
#   AUTOPILOT_COMMENT_LOGIN account GITHUB_TOKEN comments as (default ack-bot)
#   AUTOPILOT_GIT_NAME      commit author name (default ack-bot)
#   AUTOPILOT_GIT_EMAIL     commit author email (default ack-bot@users.noreply.github.com)
#
# Requires GITHUB_TOKEN and an ack-build-tools binary at AUTOPILOT_BIN
# (default /tmp/ack-build-tools). Run it from the test-infra checkout.
#
# The launcher is policy/autopilot.yaml's; "prow" also needs
# AUTOPILOT_LAUNCHER_ENABLED=true (the periodic sets "false"), and creates
# ProwJobs as the pod's ServiceAccount. A dry run never starts or aborts one.
#
# The tick reads the PRs its items link to (Plan 9) with GITHUB_TOKEN. It marks a
# gated draft ready only with AUTOPILOT_PR_WRITE=true (the periodic sets "false")
# and AUTOPILOT_PR_GITHUB_TOKEN; a dry run never does. It trusts a dod verdict
# only with a valid signature from AUTOPILOT_DOD_PUBLIC_KEY (base64, the public
# half of autopilot-dod-signing-key; empty trusts none, invalid stops the tick).
#
# The daily health report (ACK Autopilot Plan 10, design 10; oncall reads it
# daily, design 12): the first tick of a UTC day that finds no
# autopilot/reports/<yesterday>.md writes the report for the previous full UTC
# day there (ack-build-tools autopilot health-report --once), so each file is
# complete and written once. It is committed and pushed with the tick's ledger
# changes, only with AUTOPILOT_LEDGER_WRITE=true; a dry run writes it to a temp
# file and prints its path and summary. A report error is printed and never
# fails or blocks the tick.
#
# POSIX sh only: the job image (golang alpine) has no bash.
#
# post-status runs after the push. If it fails, the job exits non-zero, but the
# ledger is already pushed and consistent; the next tick retries the comments. A
# token without Issues: write therefore turns every tick red.
# In a dry run, post-status reads the ledger on main, not this tick's result, so
# its plan lags one tick.
# Push retries can exceed the job timeout. A kill at any point is safe: either
# nothing was pushed or the push already landed.
#
# Do not add `set -x`: GIT_ASKPASS hands the token to git, and tracing would put
# the environment of every command at risk of being logged.
set -eu
# busybox ash and recent dash support pipefail; skip it where unsupported.
# shellcheck disable=SC3040
(set -o pipefail) 2>/dev/null && set -o pipefail

# Git tracing would log the credential exchange, so make sure none is enabled in
# the job environment.
unset GIT_TRACE GIT_TRACE_CURL GIT_CURL_VERBOSE GIT_TRACE_PACKET \
  GIT_TRACE_SETUP GIT_TRACE2 GIT_TRACE2_EVENT GIT_TRACE2_PERF

OWNER="${1:?usage: $0 <org>}"
# The job starts in the test-infra checkout; the prow launcher reads
# agent-workflows.yaml and images_config.yaml from it (the script cds away).
TEST_INFRA_DIR="$(pwd)"
BIN="${AUTOPILOT_BIN:-/tmp/ack-build-tools}"
LEDGER_REPO="${AUTOPILOT_LEDGER_REPO:-/tmp/ack-private}"
LEDGER_DIR="$LEDGER_REPO/autopilot"
BRANCH=main
ISSUES_REPO="${AUTOPILOT_ISSUES_REPO:-ack-private}"
BOT_LOGIN="${AUTOPILOT_BOT_LOGIN:-ack-bot}"
TEAM_OWNER="${AUTOPILOT_TEAM_OWNER:-$OWNER}"
TEAM_SLUG="${AUTOPILOT_TEAM_SLUG:-ack-admin}"
COMMENT_LOGIN="${AUTOPILOT_COMMENT_LOGIN:-ack-bot}"
GIT_NAME="${AUTOPILOT_GIT_NAME:-ack-bot}"
GIT_EMAIL="${AUTOPILOT_GIT_EMAIL:-ack-bot@users.noreply.github.com}"
WRITE="${AUTOPILOT_LEDGER_WRITE:-false}"
COMMENT="${AUTOPILOT_COMMENT:-false}"
METRICS_NAMESPACE="${AUTOPILOT_METRICS_NAMESPACE:-}"
METRICS_REGION="${AUTOPILOT_METRICS_REGION:-}"
: "${GITHUB_TOKEN:?GITHUB_TOKEN is required}"

fail() { echo "autopilot-dispatch: $*" >&2; exit 1; }

[ -x "$BIN" ] || fail "ack-build-tools not found or not executable at $BIN"
if [ -e "$LEDGER_REPO" ]; then
  fail "$LEDGER_REPO already exists; refusing to reuse it"
fi
# Comments describe the pushed ledger, so they need ledger writes.
if [ "$COMMENT" = "true" ] && [ "$WRITE" != "true" ]; then
  fail "AUTOPILOT_COMMENT=true needs AUTOPILOT_LEDGER_WRITE=true"
fi

# The token never appears in a URL, on a command line or in a file: the askpass
# helper reads it from the environment when git asks for the password.
askdir="$(mktemp -d)"
reportdir="$(mktemp -d)"
trap 'rm -rf "$askdir" "$reportdir"' EXIT
askpass="$askdir/askpass.sh"
# The single quotes are deliberate: $GITHUB_TOKEN must expand when git runs the
# helper, not here.
# shellcheck disable=SC2016
printf '#!/bin/sh\nprintf "%%s\\n" "$GITHUB_TOKEN"\n' > "$askpass"
chmod 700 "$askpass"
export GIT_ASKPASS="$askpass" GIT_TERMINAL_PROMPT=0

# An empty credential.helper clears any helper configured in the image, so the
# token is never cached on disk.
git -c credential.helper= clone -q --branch "$BRANCH" \
  "https://x-access-token@github.com/${OWNER}/${ISSUES_REPO}.git" "$LEDGER_REPO" \
  || fail "could not clone ${OWNER}/${ISSUES_REPO}"
[ -d "$LEDGER_REPO/.git" ] || fail "clone did not produce a checkout at $LEDGER_REPO"
cd "$LEDGER_REPO"
git config credential.helper ""
git config user.name "$GIT_NAME"
git config user.email "$GIT_EMAIL"

dispatch() {
  "$BIN" autopilot dispatch \
    --issues-owner "$OWNER" --issues-repo "$ISSUES_REPO" \
    --bot-login "$BOT_LOGIN" --team-owner "$TEAM_OWNER" --team-slug "$TEAM_SLUG" \
    --ledger-dir "$LEDGER_DIR" --test-infra-dir "$TEST_INFRA_DIR" \
    --metrics-namespace "$METRICS_NAMESPACE" --metrics-region "$METRICS_REGION" "$@"
}

health_report() {
  if [ "$WRITE" = "true" ]; then
    "$BIN" autopilot health-report --ledger-dir "$LEDGER_DIR" --once \
      || echo "autopilot-dispatch: health report failed; the tick goes on" >&2
  elif [ -d "$LEDGER_DIR" ]; then
    "$BIN" autopilot health-report --ledger-dir "$LEDGER_DIR" --once \
      --out "$reportdir/health-report.md" \
      || echo "autopilot-dispatch: health report failed; the tick goes on" >&2
  else
    echo "no $LEDGER_DIR yet: no health report"
  fi
}

post_status() {
  if [ "$COMMENT" = "true" ]; then
    "$BIN" autopilot post-status --issues-owner "$OWNER" --issues-repo "$ISSUES_REPO" \
      --comment-login "$COMMENT_LOGIN" --bot-login "$BOT_LOGIN" --ledger-dir "$LEDGER_DIR"
  else
    "$BIN" autopilot post-status --issues-owner "$OWNER" --issues-repo "$ISSUES_REPO" \
      --comment-login "$COMMENT_LOGIN" --bot-login "$BOT_LOGIN" --ledger-dir "$LEDGER_DIR" --dry-run
  fi
}

if [ "$WRITE" != "true" ]; then
  echo "dry run (AUTOPILOT_LEDGER_WRITE is not \"true\")"
  # The CLI accepts a missing ledger dir in a dry run (before the bootstrap).
  dispatch --dry-run
  health_report
  post_status
  exit 0
fi

for attempt in 1 2 3; do
  git fetch -q origin "$BRANCH"
  git reset -q --hard "origin/$BRANCH"
  # Drop files a failed attempt left behind so the retry starts from main alone.
  git clean -q -fd -- autopilot
  [ -d "$LEDGER_DIR" ] \
    || fail "$LEDGER_DIR does not exist on ${OWNER}/${ISSUES_REPO} ${BRANCH}; bootstrap the ledger before enabling writes"
  dispatch
  health_report
  # Assign first so that a failing git status stops the script under set -e.
  changes="$(git status --porcelain -- autopilot)"
  if [ -z "$changes" ]; then
    echo "ledger unchanged"
    post_status
    exit 0
  fi
  git add -A -- autopilot
  # Stage nothing but autopilot/: anything else in the index is a bug.
  staged="$(git diff --cached --name-only)"
  while IFS= read -r path; do
    case "$path" in
      "" | autopilot/*) ;;
      *) fail "refusing to commit changes outside autopilot/: $path" ;;
    esac
  done <<STAGED
$staged
STAGED
  git commit -q -m "autopilot: dispatcher tick" -m "Producer: autopilot/dispatcher"
  if git push -q origin "HEAD:refs/heads/$BRANCH"; then
    echo "ledger pushed"
    post_status
    exit 0
  fi
  echo "push rejected (attempt $attempt); retrying on the new $BRANCH" >&2
done
fail "could not push the ledger after 3 attempts"
