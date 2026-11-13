#!/bin/sh
# Records the findings in detect-api-changes issues as ACK Autopilot work items
# in <org>/ack-private under autopilot/ (the repo is AUTOPILOT_ISSUES_REPO).
#
# Usage: autopilot-ingest.sh <org>
# AUTOPILOT_LEDGER_WRITE=true commits and pushes; anything else is a dry run that
# only prints what would change. A rejected push (someone else pushed first) is a
# lost compare-and-swap: start again from the new main, up to 3 times. Never
# force-pushes, and commits only changes under autopilot/.
#
# Optional overrides, for running against a sandbox repo (unset = production):
#   AUTOPILOT_ISSUES_REPO  repo with the issues and the ledger (default ack-private)
#   AUTOPILOT_BOT_LOGIN    account the issues are filed as (default ack-bot)
#   AUTOPILOT_TEAM_OWNER   org that owns the team (default: <org>)
#   AUTOPILOT_TEAM_SLUG    team whose members' commands count (default ack-admin)
#   AUTOPILOT_GIT_NAME     commit author name (default ack-bot)
#   AUTOPILOT_GIT_EMAIL    commit author email (default ack-bot@users.noreply.github.com)
#
# Requires GITHUB_TOKEN (from the preset-github-secrets preset) and an
# ack-build-tools binary at AUTOPILOT_BIN (default /tmp/ack-build-tools).
#
# POSIX sh only: the job image (golang alpine) has no bash.
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
BIN="${AUTOPILOT_BIN:-/tmp/ack-build-tools}"
LEDGER_REPO="${AUTOPILOT_LEDGER_REPO:-/tmp/ack-private}"
LEDGER_DIR="$LEDGER_REPO/autopilot"
BRANCH=main
ISSUES_REPO="${AUTOPILOT_ISSUES_REPO:-ack-private}"
BOT_LOGIN="${AUTOPILOT_BOT_LOGIN:-ack-bot}"
TEAM_OWNER="${AUTOPILOT_TEAM_OWNER:-$OWNER}"
TEAM_SLUG="${AUTOPILOT_TEAM_SLUG:-ack-admin}"
GIT_NAME="${AUTOPILOT_GIT_NAME:-ack-bot}"
GIT_EMAIL="${AUTOPILOT_GIT_EMAIL:-ack-bot@users.noreply.github.com}"
: "${GITHUB_TOKEN:?GITHUB_TOKEN is required}"

fail() { echo "autopilot-ingest: $*" >&2; exit 1; }

[ -x "$BIN" ] || fail "ack-build-tools not found or not executable at $BIN"
if [ -e "$LEDGER_REPO" ]; then
  fail "$LEDGER_REPO already exists; refusing to reuse it"
fi

# The token never appears in a URL, on a command line or in a file: the askpass
# helper reads it from the environment when git asks for the password.
askdir="$(mktemp -d)"
trap 'rm -rf "$askdir"' EXIT
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

ingest() {
  "$BIN" autopilot ingest-issues \
    --issues-owner "$OWNER" --issues-repo "$ISSUES_REPO" \
    --bot-login "$BOT_LOGIN" --team-owner "$TEAM_OWNER" --team-slug "$TEAM_SLUG" \
    --ledger-dir "$LEDGER_DIR" "$@"
}

if [ "${AUTOPILOT_LEDGER_WRITE:-false}" != "true" ]; then
  echo "dry run (AUTOPILOT_LEDGER_WRITE is not \"true\")"
  # The CLI accepts a missing ledger dir in a dry run (before the bootstrap).
  ingest --dry-run
  exit 0
fi

for attempt in 1 2 3; do
  git fetch -q origin "$BRANCH"
  git reset -q --hard "origin/$BRANCH"
  # Drop files a failed attempt left behind so the retry starts from main alone.
  git clean -q -fd -- autopilot
  [ -d "$LEDGER_DIR" ] \
    || fail "$LEDGER_DIR does not exist on ${OWNER}/${ISSUES_REPO} ${BRANCH}; bootstrap the ledger before enabling writes"
  ingest
  # Assign first so that a failing git status stops the script under set -e.
  changes="$(git status --porcelain -- autopilot)"
  if [ -z "$changes" ]; then
    echo "ledger unchanged"
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
  git commit -q -m "autopilot: ingest api-change issues" -m "Producer: autopilot/issue-finder"
  if git push -q origin "HEAD:refs/heads/$BRANCH"; then
    echo "ledger pushed"
    exit 0
  fi
  echo "push rejected (attempt $attempt); retrying on the new $BRANCH" >&2
done
fail "could not push the ledger after 3 attempts"
