#!/usr/bin/env bash
# Sourced by prow-job.sh.
#
# resolve_github_identity sets GITHUB_ACTOR and GITHUB_EMAIL_PREFIX to the
# login and user id of the account GITHUB_TOKEN belongs to. `gh repo fork`
# always forks into that account, so the sync, the push, the PR head
# (<login>:<branch>) and the commit identity (<id>+<login>@users.noreply...)
# must name it too. A configured GITHUB_ACTOR or GITHUB_EMAIL_PREFIX that names
# another account is logged and overridden: agent-workflows.yaml carries
# production's ack-agent, while staging's token is ack-test-agent, and the
# mismatch made `gh repo sync ack-agent/<repo>` 404 there.
#
# Returns non-zero, changing nothing, when gh fails or answers something that
# is not a GitHub login and a numeric id. Works with bash 3.2.
resolve_github_identity() {
  local out login id
  out="$(gh api user --jq '"\(.login) \(.id)"')" || return 1
  read -r login id <<<"$out"
  [[ "$login" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}$ ]] || return 1
  [[ "$id" =~ ^[0-9]+$ ]] || return 1
  local lc_actor lc_login
  lc_actor="$(printf '%s' "${GITHUB_ACTOR:-}" | tr '[:upper:]' '[:lower:]')"
  lc_login="$(printf '%s' "$login" | tr '[:upper:]' '[:lower:]')"
  if [ -n "${GITHUB_ACTOR:-}" ] && [ "$lc_actor" != "$lc_login" ]; then
    echo "github-identity][WARN] GITHUB_ACTOR is $GITHUB_ACTOR but the token belongs to $login; using $login" >&2
  fi
  if [ -n "${GITHUB_EMAIL_PREFIX:-}" ] && [ "$GITHUB_EMAIL_PREFIX" != "$id" ]; then
    echo "github-identity][WARN] GITHUB_EMAIL_PREFIX is $GITHUB_EMAIL_PREFIX but $login's user id is $id; using $id" >&2
  fi
  GITHUB_ACTOR="$login"
  GITHUB_EMAIL_PREFIX="$id"
}
