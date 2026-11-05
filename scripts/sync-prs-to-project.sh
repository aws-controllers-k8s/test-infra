#!/usr/bin/env bash
#
# Adds open PRs from community authors, created in the last --window-hours, to
# an org GitHub Project. Skips MEMBER/OWNER/COLLABORATOR authors, "[bot]"
# logins, every login in --exclude-owners-aliases and --exclude-author. Only
# recent PRs are searched, so PRs removed from the project are not re-added.
#
# Usage:
#   sync-prs-to-project.sh --org <org> --project-number <n> \
#     [--window-hours <h>] [--exclude-owners-aliases <file>] \
#     [--exclude-author <login>]... [--dry-run]
#
# GITHUB_TOKEN needs write access to the project.
#
# Requires: bash, gh, jq, date, and yq (v4) with --exclude-owners-aliases.

set -euo pipefail

log()  { echo "sync-prs-to-project: $*"; }
fail() { echo "sync-prs-to-project: ERROR: $*" >&2; exit 1; }
usage() {
  echo "Usage: $0 --org <org> --project-number <n> [--window-hours <h>]" \
       "[--exclude-owners-aliases <file>] [--exclude-author <login>]... [--dry-run]" >&2
}

ORG="" PROJECT_NUMBER="" WINDOW_HOURS=72 DRY_RUN=false OWNERS_ALIASES=""
EXCLUDE_AUTHORS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --org)            ORG="${2:-}"; shift 2 ;;
    --project-number) PROJECT_NUMBER="${2:-}"; shift 2 ;;
    --window-hours)   WINDOW_HOURS="${2:-}"; shift 2 ;;
    --exclude-owners-aliases) OWNERS_ALIASES="${2:-}"; shift 2 ;;
    --exclude-author) EXCLUDE_AUTHORS+=("${2:-}"); shift 2 ;;
    --dry-run)        DRY_RUN=true; shift ;;
    -h|--help)        usage; exit 0 ;;
    *)                usage; fail "unknown argument: $1" ;;
  esac
done

[[ "$ORG" =~ ^[A-Za-z0-9._-]+$ ]] || { usage; fail "--org is required (got '$ORG')"; }
[[ "$PROJECT_NUMBER" =~ ^[0-9]+$ ]] || { usage; fail "--project-number is required (got '$PROJECT_NUMBER')"; }
[[ "$WINDOW_HOURS" =~ ^[0-9]+$ && "$WINDOW_HOURS" -gt 0 ]] || { usage; fail "--window-hours must be a positive integer (got '$WINDOW_HOURS')"; }
# authorAssociation depends on the token's view: maintainers with private org
# membership show as CONTRIBUTOR to a non-member token, so exclude them by name.
if [[ -n "$OWNERS_ALIASES" ]]; then
  command -v yq >/dev/null 2>&1 || fail "yq is required for --exclude-owners-aliases"
  [[ -r "$OWNERS_ALIASES" ]] || fail "cannot read --exclude-owners-aliases '$OWNERS_ALIASES'"
  maintainers=$(yq -r '.aliases[][]' "$OWNERS_ALIASES") || fail "could not parse '$OWNERS_ALIASES'"
  [[ -n "$maintainers" ]] || fail "no logins found in '$OWNERS_ALIASES'"
  while IFS= read -r m; do EXCLUDE_AUTHORS+=("$m"); done <<< "$maintainers"
fi
# Allows "[bot]" logins. "]" must come first in a bash bracket expression.
login_re='^[][A-Za-z0-9._-]+$'
for a in ${EXCLUDE_AUTHORS[@]+"${EXCLUDE_AUTHORS[@]}"}; do
  [[ "$a" =~ $login_re ]] || fail "invalid --exclude-author '$a'"
done
[[ -n "${GITHUB_TOKEN:-}${GH_TOKEN:-}" ]] || fail "GITHUB_TOKEN is not set (expected from the preset-github-secrets preset)"
for tool in gh jq date; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done

export GH_TOKEN="${GH_TOKEN:-$GITHUB_TOKEN}"
export GH_PROMPT_DISABLED=1 GH_NO_UPDATE_NOTIFIER=1

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# GNU date, falling back to BSD date.
since=$(date -u -d "-${WINDOW_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
        || date -u -v-"${WINDOW_HOURS}"H +%Y-%m-%dT%H:%M:%SZ)

title=$(gh project view "$PROJECT_NUMBER" --owner "$ORG" --format json --jq .title) \
  || fail "could not read project $PROJECT_NUMBER in $ORG (does the token have project access?)"
log "project: $title ($ORG #$PROJECT_NUMBER)"
log "searching open PRs created since $since"

# Search returns at most 1000 results; fail rather than silently drop PRs.
SEARCH_LIMIT=1000
gh search prs --owner "$ORG" --state open --created ">=${since}" --limit "$SEARCH_LIMIT" \
  --json url,authorAssociation,author > "$WORK/prs.json" \
  || fail "PR search failed"
seen=$(jq 'length' "$WORK/prs.json")
[[ "$seen" -lt "$SEARCH_LIMIT" ]] || fail "search hit the $SEARCH_LIMIT-result cap; shorten --window-hours"

# Logins are case-insensitive.
authors_json=$(printf '%s\n' ${EXCLUDE_AUTHORS[@]+"${EXCLUDE_AUTHORS[@]}"} | jq -R . | jq -cs 'map(select(length>0) | ascii_downcase)')

# gh search reports is_bot=false for dependabot[bot], so also match the suffix.
FILTER='
  .[]
  | select(.authorAssociation | IN("MEMBER","OWNER","COLLABORATOR") | not)
  | select((.author.is_bot // false) | not)
  | select((.author.login // "") | endswith("[bot]") | not)
  | select((.author.login // "" | ascii_downcase) as $l | $authors | index($l) | not)
  | .url'

jq -r --argjson authors "$authors_json" "$FILTER" \
  "$WORK/prs.json" > "$WORK/urls"
candidates=$(wc -l < "$WORK/urls" | tr -d ' ')
log "found $seen open PR(s) in the window; $candidates from community authors"

added=0 failed=0
while IFS= read -r url; do
  [[ -n "$url" ]] || continue
  if [[ "$DRY_RUN" == "true" ]]; then
    log "[dry-run] would add $url"
    added=$((added + 1))
    continue
  fi
  if gh project item-add "$PROJECT_NUMBER" --owner "$ORG" --url "$url" >/dev/null; then
    log "added $url"
    added=$((added + 1))
  else
    log "FAILED to add $url"
    failed=$((failed + 1))
  fi
done < "$WORK/urls"

log "done: $added added$([[ "$DRY_RUN" == "true" ]] && echo ' (dry-run)'), $failed failed"
[[ "$failed" -eq 0 ]] || exit 1
