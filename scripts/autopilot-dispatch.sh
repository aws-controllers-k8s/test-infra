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
# (default /tmp/ack-build-tools).
#
# POSIX sh only: the job image (golang alpine) has no bash.
#
# Do not add `set -x`: GIT_ASKPASS hands the token to git, and tracing would put
# the environment of every command at risk of being logged.
set -eu
# busybox ash and recent dash support pipefail; skip it where unsupported.
# shellcheck disable=SC3040
(set -o pipefail) 2>/dev/null && set -o pipefail

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
COMMENT_LOGIN="${AUTOPILOT_COMMENT_LOGIN:-ack-bot}"
GIT_NAME="${AUTOPILOT_GIT_NAME:-ack-bot}"
GIT_EMAIL="${AUTOPILOT_GIT_EMAIL:-ack-bot@users.noreply.github.com}"
WRITE="${AUTOPILOT_LEDGER_WRITE:-false}"
COMMENT="${AUTOPILOT_COMMENT:-false}"
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

askdir="$(mktemp -d)"
trap 'rm -rf "$askdir"' EXIT
askpass="$askdir/askpass.sh"
# shellcheck disable=SC2016
printf '#!/bin/sh\nprintf "%%s\\n" "$GITHUB_TOKEN"\n' > "$askpass"
chmod 700 "$askpass"
export GIT_ASKPASS="$askpass" GIT_TERMINAL_PROMPT=0

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
    --ledger-dir "$LEDGER_DIR" "$@"
}

post_status() {
  if [ "$COMMENT" = "true" ]; then
    "$BIN" autopilot post-status --issues-owner "$OWNER" --issues-repo "$ISSUES_REPO" \
      --comment-login "$COMMENT_LOGIN" --ledger-dir "$LEDGER_DIR"
  else
    "$BIN" autopilot post-status --issues-owner "$OWNER" --issues-repo "$ISSUES_REPO" \
      --comment-login "$COMMENT_LOGIN" --ledger-dir "$LEDGER_DIR" --dry-run
  fi
}

if [ "$WRITE" != "true" ]; then
  echo "dry run (AUTOPILOT_LEDGER_WRITE is not \"true\")"
  # The CLI accepts a missing ledger dir in a dry run (before the bootstrap).
  dispatch --dry-run
  post_status
  exit 0
fi

for attempt in 1 2 3; do
  git fetch -q origin "$BRANCH"
  git reset -q --hard "origin/$BRANCH"
  git clean -q -fd -- autopilot
  [ -d "$LEDGER_DIR" ] \
    || fail "$LEDGER_DIR does not exist on ${OWNER}/${ISSUES_REPO} ${BRANCH}; bootstrap the ledger before enabling writes"
  dispatch
  changes="$(git status --porcelain -- autopilot)"
  if [ -z "$changes" ]; then
    echo "ledger unchanged"
    post_status
    exit 0
  fi
  git add -A -- autopilot
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
