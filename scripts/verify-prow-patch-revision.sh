#!/usr/bin/env bash
#
# Verifies that <head> is a Prow patch-revision bump and nothing else.
#
# The bump-prow-patch-revision periodic merges its own PRs without review, so
# this check, not the code that produced the change, is what limits what it can
# merge. It reads only git objects and deliberately shares no code with
# upgrade-prow.sh.
#
# <head> passes only if ALL of these hold:
#   - it is a single non-merge commit whose parent is <base>
#   - it modifies exactly the three files that carry prowPatchRevision, with no
#     adds, deletes, renames or mode changes (so the ProwJob CRD, Chart.yaml and
#     every other file are untouched)
#   - each file changes exactly one line, and both sides of it are the
#     prowPatchRevision key
#   - each file is otherwise identical once parsed, so prowVersion, toolsVersion
#     and every other value are unchanged
#   - the new revision is the same in all three files and greater than the old one
#
# Usage: verify-prow-patch-revision.sh <base> <head>

set -euo pipefail

# <file>|<yq path of its prowPatchRevision>
TARGETS=(
  "prow/config/values.yaml|.imageMirror.prowPatchRevision"
  "prow/jobs/values.yaml|.prowPatchRevision"
  "flux/prow/charts/prow-mirror/values.yaml|.prowPatchRevision"
)
LINE_RE='^[-+] *prowPatchRevision: "[0-9]+"$'

fail() { echo "verify-prow-patch-revision: REJECT: $*" >&2; exit 1; }

[[ $# -eq 2 ]] || { echo "Usage: $0 <base> <head>" >&2; exit 2; }
command -v yq >/dev/null 2>&1 || { echo "error: yq is required" >&2; exit 2; }

base=$(git rev-parse --verify --quiet "$1^{commit}") || fail "unknown commit: $1"
head=$(git rev-parse --verify --quiet "$2^{commit}") || fail "unknown commit: $2"

read -r -a parents <<< "$(git rev-list --parents -n 1 "$head")"
[[ ${#parents[@]} -eq 2 && "${parents[1]}" == "$base" ]] \
  || fail "$head is not a single commit on top of $base"

files=()
for t in "${TARGETS[@]}"; do files+=("${t%%|*}"); done

expected=$(printf 'M\t%s\n' "${files[@]}" | sort)
actual=$(git diff --no-renames --name-status "$base" "$head" | sort)
[[ "$actual" == "$expected" ]] \
  || fail "must modify exactly ${files[*]}; got:"$'\n'"$actual"

summary=$(git diff --no-renames --summary "$base" "$head")
[[ -z "$summary" ]] || fail "unexpected mode or file changes: $summary"

old_rev="" new_rev=""
for t in "${TARGETS[@]}"; do
  f="${t%%|*}" path="${t#*|}"

  numstat=$(git diff --no-renames --numstat "$base" "$head" -- "$f")
  [[ "$numstat" == $'1\t1\t'"$f" ]] || fail "$f must change exactly one line; numstat: $numstat"

  changed=$(git diff --no-renames --no-color -U0 "$base" "$head" -- "$f" \
              | grep -E '^[-+]' | grep -vE '^(---|\+\+\+) ' || true)
  while IFS= read -r line; do
    [[ "$line" =~ $LINE_RE ]] || fail "$f: changed line is not prowPatchRevision: $line"
  done <<< "$changed"

  before=$(git show "$base:$f")
  after=$(git show "$head:$f")

  # Everything except the revision must parse identically.
  if ! diff <(yq -o=json "del($path)" - <<< "$before") \
            <(yq -o=json "del($path)" - <<< "$after") >/dev/null; then
    fail "$f: something besides $path changed"
  fi

  o=$(yq "$path" - <<< "$before")
  n=$(yq "$path" - <<< "$after")
  [[ "$o" =~ ^[0-9]+$ && "$n" =~ ^[0-9]+$ ]] || fail "$f: $path is not numeric ($o -> $n)"

  if [[ -z "$old_rev" ]]; then
    old_rev="$o" new_rev="$n"
  elif [[ "$o" != "$old_rev" || "$n" != "$new_rev" ]]; then
    fail "$f: revision $o -> $n disagrees with $old_rev -> $new_rev in ${files[0]}"
  fi
done

(( 10#$new_rev > 10#$old_rev )) || fail "revision must increase ($old_rev -> $new_rev)"

echo "verify-prow-patch-revision: OK: prowPatchRevision $old_rev -> $new_rev, nothing else changed"
