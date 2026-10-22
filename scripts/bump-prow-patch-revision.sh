#!/usr/bin/env bash
#
# Rebuilds the pinned Prow images against current OS packages, unattended.
#
# Each run bumps prowPatchRevision with `upgrade-prow.sh --bump-patch`, opens a
# PR, and merges it itself once verify-prow-patch-revision.sh confirms the PR
# changes nothing but the revision. Argo CD then runs the prow-mirror Job, which
# rebuilds every mirrored image under the new suffix, and rolls Prow onto it.
#
# This never upgrades Prow. prowVersion, toolsVersion and the ProwJob CRD stay
# where they are; moving them remains a manual, reviewed change.
#
# What bounds the auto-merge:
#   - Only PRs opened by this token, from an ack-bot/prow-patch-revision-*
#     branch in the same repo, are ever merged.
#   - The verifier runs from this checkout of the base branch, never from the
#     PR, against the PR's head commit as GitHub has it.
#   - The merge is pinned to the verified commit (GitHub rejects it if the
#     branch moved), and only happens while the base branch is still the commit
#     the PR was verified against.
#   - A do-not-merge/* label on an open bot PR pauses the automation.
#   - No bump starts until the current revision is in every mirrored ECR
#     repository, so a failed rebuild is never stacked on.
#
# The next revision is one above the highest -ack.<n> in any mirrored
# repository, not just one above git. After a revert, git's value + 1 can name
# a build that already exists, and the mirror would skip straight back to it.
#
# Usage:
#   bump-prow-patch-revision.sh --source-owner <org> --source-repo <repo> \
#     --base-branch <branch> [--dry-run]
#
# --dry-run does every read plus the local bump and verification, and writes
# nothing to GitHub. GITHUB_TOKEN is optional with --dry-run.
#
# Requires: bash, git, yq (v4), jq, curl, aws, and ack-build-tools unless
# --dry-run. AWS credentials need ecr:ListImages on the stack's prow/*
# repositories.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VERIFY="$SCRIPT_DIR/verify-prow-patch-revision.sh"

BRANCH_PREFIX="ack-bot/prow-patch-revision-"
FILES=(prow/config/values.yaml prow/jobs/values.yaml flux/prow/charts/prow-mirror/values.yaml)
MIRROR_JOB="flux/prow/charts/prow-mirror/templates/mirror-job.yaml"

export AWS_PAGER=""

log()  { echo "bump-prow-patch-revision: $*"; }
fail() { echo "bump-prow-patch-revision: ERROR: $*" >&2; exit 1; }
usage() {
  echo "Usage: $0 --source-owner <org> --source-repo <repo> --base-branch <branch> [--dry-run]" >&2
}

OWNER="" REPO="" BASE_BRANCH="" DRY_RUN=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-owner) OWNER="${2:-}"; shift 2 ;;
    --source-repo)  REPO="${2:-}"; shift 2 ;;
    --base-branch)  BASE_BRANCH="${2:-}"; shift 2 ;;
    --dry-run)      DRY_RUN=true; shift ;;
    -h|--help)      usage; exit 0 ;;
    *)              usage; fail "unknown argument: $1" ;;
  esac
done

[[ "$OWNER" =~ ^[A-Za-z0-9._-]+$ ]] || { usage; fail "--source-owner is required (got '$OWNER')"; }
[[ "$REPO" =~ ^[A-Za-z0-9._-]+$ ]] || { usage; fail "--source-repo is required (got '$REPO')"; }
[[ "$BASE_BRANCH" =~ ^[A-Za-z0-9._/-]+$ ]] || { usage; fail "--base-branch is required (got '$BASE_BRANCH')"; }
if [[ "$DRY_RUN" != "true" && -z "${GITHUB_TOKEN:-}" ]]; then
  fail "GITHUB_TOKEN is not set (expected from the preset-github-secrets preset)"
fi
for tool in git yq jq curl aws; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
if [[ "$DRY_RUN" != "true" ]]; then
  command -v ack-build-tools >/dev/null 2>&1 || fail "ack-build-tools is required"
fi

cd "$REPO_ROOT"
REPO_URL="https://github.com/${OWNER}/${REPO}.git"
API="https://api.github.com/repos/${OWNER}/${REPO}"

WORK=$(mktemp -d)
cleanup() {
  if [[ -d "$WORK/tree" ]]; then git worktree remove --force "$WORK/tree" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# The token goes in a header file, so it never appears in a process listing.
AUTH_HEADER=()
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  ( umask 077; printf 'Authorization: Bearer %s\n' "$GITHUB_TOKEN" > "$WORK/auth" )
  AUTH_HEADER=(-H "@$WORK/auth")
fi

# gh_api <method> <url> [json-body]: prints the response body, fails on non-2xx.
gh_api() {
  local method="$1" url="$2" body="${3:-}" code
  local args=(-sS -o "$WORK/resp" -w '%{http_code}' -X "$method"
              -H "Accept: application/vnd.github+json"
              -H "X-GitHub-Api-Version: 2022-11-28"
              ${AUTH_HEADER[@]+"${AUTH_HEADER[@]}"})
  if [[ -n "$body" ]]; then
    args+=(-H "Content-Type: application/json" --data "$body")
  fi
  if ! code=$(curl "${args[@]}" "$url"); then
    echo "bump-prow-patch-revision: $method $url: request failed" >&2
    return 1
  fi
  if [[ "$code" != 2* ]]; then
    echo "bump-prow-patch-revision: $method $url: HTTP $code: $(head -c 500 "$WORK/resp")" >&2
    return 1
  fi
  cat "$WORK/resp"
}

remote_base_sha() {
  git ls-remote "$REPO_URL" "refs/heads/${BASE_BRANCH}" | cut -f1
}

delete_branch() {
  if [[ "$DRY_RUN" == "true" ]]; then log "[dry-run] would delete branch $1"; return 0; fi
  gh_api DELETE "$API/git/refs/heads/$1" >/dev/null || log "could not delete branch $1"
}

close_pr() {
  local n="$1" ref="$2" reason="$3" body
  if [[ "$DRY_RUN" == "true" ]]; then log "[dry-run] would close #$n: $reason"; return 0; fi
  body=$(jq -cn --arg r "$reason" '{body: ("Closing: " + $r + "\n\nThe `bump-prow-patch-revision` periodic opens a fresh PR on its next run.")}')
  gh_api POST "$API/issues/$n/comments" "$body" >/dev/null
  gh_api PATCH "$API/pulls/$n" '{"state":"closed"}' >/dev/null
  delete_branch "$ref"
  log "closed #$n: $reason"
}

# verify_pr <number> <head sha>: fetches the PR head GitHub has and verifies it
# against the base this run started from.
verify_pr() {
  local n="$1" sha="$2" fetched
  git fetch -q "$REPO_URL" "refs/pull/$n/head"
  fetched=$(git rev-parse FETCH_HEAD)
  if [[ "$fetched" != "$sha" ]]; then
    log "#$n moved while being checked ($sha -> $fetched)"
    return 1
  fi
  "$VERIFY" "$BASE_SHA" "$sha"
}

# merge_pr <pr json>: squash-merges a verified PR, pinned to its verified head.
merge_pr() {
  local pr="$1" n sha ref title body payload result
  n=$(jq -r .number <<< "$pr")
  sha=$(jq -r .head.sha <<< "$pr")
  ref=$(jq -r .head.ref <<< "$pr")
  title=$(jq -r .title <<< "$pr")
  body=$(jq -r '.body // ""' <<< "$pr")

  if [[ "$(remote_base_sha)" != "$BASE_SHA" ]]; then
    log "${BASE_BRANCH} moved after #$n was verified; leaving it for the next run"
    exit 0
  fi
  if [[ "$DRY_RUN" == "true" ]]; then log "[dry-run] would merge #$n at $sha"; return 0; fi

  # Same title and body shape as Tide's merge_commit_template.
  payload=$(jq -cn --arg sha "$sha" --arg t "$title (#$n)" --arg m "$body" \
              '{merge_method: "squash", sha: $sha, commit_title: $t, commit_message: $m}')
  result=$(gh_api PUT "$API/pulls/$n/merge" "$payload")
  [[ "$(jq -r .merged <<< "$result")" == "true" ]] || fail "merge of #$n did not report merged: $result"
  log "merged #$n as $(jq -r .sha <<< "$result")"
  delete_branch "$ref"
}

# ── 1. Read the pinned versions from the current base ───────────────────────

git fetch -q "$REPO_URL" "refs/heads/${BASE_BRANCH}"
BASE_SHA=$(git rev-parse FETCH_HEAD)
log "base ${OWNER}/${REPO}@${BASE_BRANCH} is $BASE_SHA"

# field <file> <yq path>: a value from the base commit.
field() { git show "$BASE_SHA:$1" | yq "$2" -; }

prow_version=$(field prow/jobs/values.yaml .prowVersion)
tools_version=$(field prow/jobs/values.yaml .toolsVersion)
revision=$(field prow/jobs/values.yaml .prowPatchRevision)

for check in \
  "prow/config/values.yaml|.imageMirror.prowVersion|$prow_version" \
  "flux/prow/charts/prow-mirror/values.yaml|.prowVersion|$prow_version" \
  "flux/prow/charts/prow-mirror/values.yaml|.toolsVersion|$tools_version" \
  "prow/config/values.yaml|.imageMirror.prowPatchRevision|$revision" \
  "flux/prow/charts/prow-mirror/values.yaml|.prowPatchRevision|$revision"; do
  IFS='|' read -r f path want <<< "$check"
  got=$(field "$f" "$path")
  [[ "$got" == "$want" ]] || fail "$f $path is '$got' but prow/jobs/values.yaml has '$want'; the copies must agree before this can run"
done
[[ "$revision" =~ ^[0-9]+$ ]] || fail "prowPatchRevision '$revision' is not numeric"
log "prowVersion $prow_version, toolsVersion $tools_version, prowPatchRevision $revision"

# ── 2. Check the current revision finished rolling out ─────────────────────

mapfile -t images < <(git show "$BASE_SHA:$MIRROR_JOB" \
  | sed -nE 's/^[[:space:]]*(PROW|TOOLS)\|([A-Za-z0-9_.-]+)[[:space:]]*$/\1|\2/p')
[[ ${#images[@]} -gt 0 ]] || fail "found no image list in $MIRROR_JOB"

account=$(aws sts get-caller-identity --query Account --output text)
missing=()
max_used=0
for entry in "${images[@]}"; do
  class="${entry%%|*}" img="${entry#*|}"
  if [[ "$class" == "PROW" ]]; then ver="$prow_version"; else ver="$tools_version"; fi

  tags=$(aws ecr list-images --registry-id "$account" --repository-name "prow/$img" \
           --filter tagStatus=TAGGED --query 'imageIds[].imageTag' --output json | jq -r '.[]')

  if ! grep -qxF "${ver}-ack.${revision}" <<< "$tags"; then
    missing+=("prow/${img}:${ver}-ack.${revision}")
  fi
  while read -r used; do
    if [[ -n "$used" ]] && (( 10#$used > max_used )); then max_used=$((10#$used)); fi
  done < <(sed -nE 's/.*-ack\.([0-9]+)$/\1/p' <<< "$tags")
done

if [[ ${#missing[@]} -gt 0 ]]; then
  fail "revision $revision is not in every mirrored repository, so the prow-mirror Job for it is still running or failed. Not stacking another bump on it. Missing: ${missing[*]}"
fi
log "revision $revision is present in all ${#images[@]} mirrored repositories (highest suffix in use: $max_used)"

# ── 3. Finish or clear a PR from an earlier run ─────────────────────────────

me=""
if [[ -n "${GITHUB_TOKEN:-}" ]]; then
  me=$(gh_api GET "https://api.github.com/user" | jq -r .login)
  log "acting as $me"
fi

open_prs=$(gh_api GET "$API/pulls?state=open&base=${BASE_BRANCH}&per_page=100")
candidates=$(jq -c --arg p "$BRANCH_PREFIX" --arg repo "${OWNER}/${REPO}" --arg me "$me" \
  '[.[] | select(.head.ref | startswith($p))
        | select(.head.repo.full_name == $repo)
        | select($me == "" or .user.login == $me)]' <<< "$open_prs")

while read -r pr; do
  [[ -n "$pr" ]] || continue
  n=$(jq -r .number <<< "$pr")
  held=$(jq -r '[.labels[].name | select(startswith("do-not-merge/"))] | join(",")' <<< "$pr")
  if [[ -n "$held" || "$(jq -r .draft <<< "$pr")" == "true" ]]; then
    log "#$n is held (${held:-draft}); nothing to do until that is removed"
    exit 0
  fi
  if verify_pr "$n" "$(jq -r .head.sha <<< "$pr")"; then
    merge_pr "$pr"
    exit 0
  fi
  close_pr "$n" "$(jq -r .head.ref <<< "$pr")" "it is no longer a single prowPatchRevision bump on top of ${BASE_BRANCH} ($BASE_SHA)"
done < <(jq -c '.[]' <<< "$candidates")

# ── 4. Bump, open and merge ──────────────────────────────────────────────────

next=$(( (10#$revision > max_used ? 10#$revision : max_used) + 1 ))
branch="${BRANCH_PREFIX}${next}"
log "next revision is $next"

if [[ -n "$(git ls-remote "$REPO_URL" "refs/heads/$branch")" ]]; then
  others=$(gh_api GET "$API/pulls?state=open&head=${OWNER}:${branch}" | jq length)
  [[ "$others" == "0" ]] || fail "branch $branch already has an open PR that this job did not open"
  log "removing stale branch $branch"
  delete_branch "$branch"
fi

git worktree add -q --detach "$WORK/tree" "$BASE_SHA"
(cd "$WORK/tree" && ./scripts/upgrade-prow.sh --bump-patch --patch-revision="$next")

changed=$(git -C "$WORK/tree" status --porcelain | awk '{print $2}' | sort)
[[ "$changed" == "$(printf '%s\n' "${FILES[@]}" | sort)" ]] \
  || fail "upgrade-prow.sh changed unexpected files:"$'\n'"$changed"

git -C "$WORK/tree" add -- "${FILES[@]}"
git -C "$WORK/tree" -c user.name=bump-prow-patch-revision -c user.email=bump-prow-patch-revision@localhost \
  commit -q -m "Bump Prow patch revision to $next"
"$VERIFY" "$BASE_SHA" "$(git -C "$WORK/tree" rev-parse HEAD)"

subject="Bump Prow patch revision to ${next}"
description=$(cat <<EOF
Rebuilds the pinned Prow images against current OS packages. prowVersion \`${prow_version}\` and toolsVersion \`${tools_version}\` are unchanged; prowPatchRevision goes from ${revision} to ${next}.

Opened and merged by the \`bump-prow-patch-revision\` periodic once \`scripts/verify-prow-patch-revision.sh\` confirmed the PR changes the revision and nothing else. Upgrading Prow itself stays a manual change (\`scripts/upgrade-prow.sh\`).
EOF
)

if [[ "$DRY_RUN" == "true" ]]; then
  log "[dry-run] would open \"$subject\" from $branch:"
  git diff "$BASE_SHA" "$(git -C "$WORK/tree" rev-parse HEAD)"
  exit 0
fi

source_files=""
for f in "${FILES[@]}"; do source_files+="$WORK/tree/$f:$f,"; done
ack-build-tools publish-pr \
  --source-owner "$OWNER" \
  --source-repo "$REPO" \
  --base-branch "$BASE_BRANCH" \
  --commit-branch "$branch" \
  --subject "$subject" \
  --description "$description" \
  --source-files "${source_files%,}"

pr=$(gh_api GET "$API/pulls?state=open&head=${OWNER}:${branch}" | jq -c '.[0] // empty')
[[ -n "$pr" ]] || fail "opened a PR from $branch but cannot find it"
n=$(jq -r .number <<< "$pr")
[[ "$(jq -r .user.login <<< "$pr")" == "$me" ]] || fail "#$n is not authored by $me"
log "opened #$n"

if ! verify_pr "$n" "$(jq -r .head.sha <<< "$pr")"; then
  if [[ "$(remote_base_sha)" != "$BASE_SHA" ]]; then
    close_pr "$n" "$branch" "${BASE_BRANCH} moved while it was being opened"
    exit 0
  fi
  close_pr "$n" "$branch" "it failed verification"
  fail "#$n failed verification; this job produced a bad PR"
fi
merge_pr "$pr"
log "Argo CD now runs prow-mirror for -ack.${next} and rolls Prow onto it"
