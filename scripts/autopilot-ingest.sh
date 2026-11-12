#!/usr/bin/env bash
# Records the findings in detect-api-changes issues as ACK Autopilot work items
# in <org>/ack-private under autopilot/.
#
# Usage: autopilot-ingest.sh <org>
# AUTOPILOT_LEDGER_WRITE=true commits and pushes; anything else is a dry run that
# only prints what would change. A rejected push (someone else pushed first) is a
# lost compare-and-swap: start again from the new main, up to 3 times. Never
# force-pushes, and commits only changes under autopilot/.
#
# Requires GITHUB_TOKEN (from the preset-github-secrets preset) and an
# ack-build-tools binary at AUTOPILOT_BIN (default /tmp/ack-build-tools).
#
# Do not add `set -x`: GIT_ASKPASS hands the token to git, and tracing would put
# the environment of every command at risk of being logged.
set -euo pipefail

OWNER="${1:?usage: $0 <org>}"
BIN="${AUTOPILOT_BIN:-/tmp/ack-build-tools}"
LEDGER_REPO="${AUTOPILOT_LEDGER_REPO:-/tmp/ack-private}"
LEDGER_DIR="$LEDGER_REPO/autopilot"
BRANCH=main
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
  "https://x-access-token@github.com/${OWNER}/ack-private.git" "$LEDGER_REPO" \
  || fail "could not clone ${OWNER}/ack-private"
[ -d "$LEDGER_REPO/.git" ] || fail "clone did not produce a checkout at $LEDGER_REPO"
cd "$LEDGER_REPO"
git config credential.helper ""
git config user.name "ack-bot"
git config user.email "ack-bot@users.noreply.github.com"

ingest() {
  "$BIN" autopilot ingest-issues \
    --issues-owner "$OWNER" --issues-repo ack-private \
    --bot-login ack-bot --team-slug ack-admin \
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
    || fail "$LEDGER_DIR does not exist on ${OWNER}/ack-private ${BRANCH}; bootstrap the ledger before enabling writes"
  ingest
  if [ -z "$(git status --porcelain -- autopilot)" ]; then
    echo "ledger unchanged"
    exit 0
  fi
  git add -A -- autopilot
  # Stage nothing but autopilot/: anything else in the index is a bug.
  if git diff --cached --name-only | grep -qv '^autopilot/'; then
    fail "refusing to commit changes outside autopilot/"
  fi
  git commit -q -m "autopilot: ingest api-change issues" -m "Producer: autopilot/issue-finder"
  if git push -q origin "HEAD:refs/heads/$BRANCH"; then
    echo "ledger pushed"
    exit 0
  fi
  echo "push rejected (attempt $attempt); retrying on the new $BRANCH" >&2
done
fail "could not push the ledger after 3 attempts"
