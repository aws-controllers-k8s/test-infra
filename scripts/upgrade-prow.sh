#!/usr/bin/env bash
#
# Upgrades Prow to the latest version.
#
# Updates the PROW_VERSION in the prow-version ConfigMap and upgrades the
# ProwJob CRD from upstream. All image references use Flux variable
# substitution (${PROW_IMAGE_REGISTRY}/${component}:${PROW_VERSION}-ack.${PROW_PATCH_REVISION}),
# so updating the ConfigMap is all that's needed to roll out new images.
#
# PROW_PATCH_REVISION is a monotonic counter. It increments whenever an upstream
# tag moves or --bump-patch is passed, and is never reset, so the mirror always
# targets a tag that does not yet exist. Use --bump-patch to rebuild the current
# Prow version against newly published OS security updates without moving to a
# new upstream release.
#
# Usage:
#   ./scripts/upgrade-prow.sh              # auto-detect latest tag
#   ./scripts/upgrade-prow.sh <tag>        # use a specific tag
#   ./scripts/upgrade-prow.sh --dry-run    # show what would change
#   ./scripts/upgrade-prow.sh --crd-only   # only upgrade the CRD
#   ./scripts/upgrade-prow.sh --bump-patch # re-patch current version (OS updates)
#
# Requires: curl, yq
# Optional: crane (faster tag detection), kubectl (CRD validation)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

REGISTRY="us-docker.pkg.dev/k8s-infra-prow/images"
# The versions used to live in one place, the prow-version ConfigMap that Flux substituted
# from. They are now chart defaults in three values files, because Argo CD has no equivalent of
# postBuild substitution and static git-authored values belong with the structure. All three
# MUST agree: prow-config composes the component image references, prow-jobs stamps the job
# config, and prow-mirror decides which tags to mirror into ECR.
#
# yq paths differ per file, so each is addressed explicitly rather than by a shared key.
CONFIG_VALUES="$REPO_ROOT/prow/config/values.yaml"
JOBS_VALUES="$REPO_ROOT/prow/jobs/values.yaml"
MIRROR_VALUES="$REPO_ROOT/flux/prow/charts/prow-mirror/values.yaml"

# read_version <key>            -> the value, read from prow-jobs (the file that holds all three)
# write_version <key> <value>   -> writes every copy, and fails if any file is missing
read_version() {
  case "$1" in
    PROW_VERSION)        yq '.prowVersion' "$JOBS_VALUES" ;;
    TOOLS_VERSION)       yq '.toolsVersion' "$JOBS_VALUES" ;;
    PROW_PATCH_REVISION) yq '.prowPatchRevision' "$JOBS_VALUES" ;;
  esac
}
write_version() {
  local key="$1" val="$2" f
  for f in "$CONFIG_VALUES" "$JOBS_VALUES" "$MIRROR_VALUES"; do
    [ -f "$f" ] || { echo "ERROR: $f missing -- the three version copies must stay in sync" >&2; exit 1; }
  done
  case "$key" in
    PROW_VERSION)
      yq -i ".imageMirror.prowVersion = \"${val}\"" "$CONFIG_VALUES"
      yq -i ".prowVersion = \"${val}\"" "$JOBS_VALUES"
      yq -i ".prowVersion = \"${val}\"" "$MIRROR_VALUES" ;;
    TOOLS_VERSION)
      # prow-config does not carry toolsVersion; only jobs and mirror consume it
      yq -i ".toolsVersion = \"${val}\"" "$JOBS_VALUES"
      yq -i ".toolsVersion = \"${val}\"" "$MIRROR_VALUES" ;;
    PROW_PATCH_REVISION)
      yq -i ".imageMirror.prowPatchRevision = \"${val}\"" "$CONFIG_VALUES"
      yq -i ".prowPatchRevision = \"${val}\"" "$JOBS_VALUES"
      yq -i ".prowPatchRevision = \"${val}\"" "$MIRROR_VALUES" ;;
  esac
}
CRD_FILE="$REPO_ROOT/flux/prow/crds/prowjob_customresourcedefinition.yaml"
CRD_UPSTREAM="https://raw.githubusercontent.com/kubernetes-sigs/prow/main/config/prow/cluster/prowjob-crd/prowjob_customresourcedefinition.yaml"
CHART_FILE="$REPO_ROOT/prow/config/Chart.yaml"

# --- Parse arguments ---
DRY_RUN=false
CRD_ONLY=false
BUMP_PATCH=false
TARGET_TAG=""

for arg in "$@"; do
  case "$arg" in
    --dry-run)    DRY_RUN=true ;;
    --crd-only)   CRD_ONLY=true ;;
    --bump-patch) BUMP_PATCH=true ;;
    --help|-h)
      echo "Usage: $0 [--dry-run] [--crd-only] [--bump-patch] [<tag>]"
      echo ""
      echo "Upgrades Prow version and CRD."
      echo ""
      echo "Options:"
      echo "  --dry-run     Show what would change without modifying files"
      echo "  --crd-only    Only upgrade the ProwJob CRD"
      echo "  --bump-patch  Increment PROW_PATCH_REVISION to rebuild the current"
      echo "                Prow version against new OS security updates"
      echo "  <tag>         Specific image tag (e.g., v20260519-c47e31ece)"
      exit 0
      ;;
    v*) TARGET_TAG="$arg" ;;
    *)
      echo "error: unknown argument: $arg" >&2
      exit 1
      ;;
  esac
done

# ─────────────────────────────────────────────────────────────────────────────
# Version upgrade
# ─────────────────────────────────────────────────────────────────────────────

if [[ "$CRD_ONLY" == "false" ]]; then

  if ! command -v yq >/dev/null 2>&1; then
    echo "error: yq is required (https://github.com/mikefarah/yq)" >&2
    exit 1
  fi

  # Tracks whether an upstream tag moved. A new upstream tag has never been
  # patched, so the patch revision restarts at 1 rather than incrementing.
  VERSION_CHANGED=false

  # --bump-patch re-patches the versions already pinned instead of moving them.
  if [[ "$BUMP_PATCH" == "true" && -z "$TARGET_TAG" ]]; then
    TARGET_TAG=$(read_version PROW_VERSION)
    echo "Re-patching pinned Prow version: $TARGET_TAG"
  fi

  # Detect latest tag
  if [[ -z "$TARGET_TAG" ]]; then
    echo "Detecting latest Prow image tag..."

    if command -v crane >/dev/null 2>&1; then
      TARGET_TAG=$(crane ls "${REGISTRY}/deck" 2>/dev/null \
        | grep -E '^v[0-9]{8}-[a-f0-9]+$' \
        | sort -V \
        | tail -1)
    fi

    if [[ -z "$TARGET_TAG" ]]; then
      TARGET_TAG=$(curl -sL "https://us-docker.pkg.dev/v2/k8s-infra-prow/images/deck/tags/list" \
        | grep -oE '"v[0-9]{8}-[a-f0-9]+"' \
        | tr -d '"' \
        | sort -V \
        | tail -1)
    fi

    if [[ -z "$TARGET_TAG" ]]; then
      echo "error: could not detect latest tag" >&2
      echo "  Provide it manually: $0 v20260519-c47e31ece" >&2
      exit 1
    fi

    echo "  Latest: $TARGET_TAG"
  fi

  # Read current version
  CURRENT_TAG=$(read_version PROW_VERSION)

  if [[ "$CURRENT_TAG" == "$TARGET_TAG" ]]; then
    echo "  Already at $TARGET_TAG — no version change needed."
  else
    echo "  Current: $CURRENT_TAG"
    echo "  Target:  $TARGET_TAG"

    VERSION_CHANGED=true

    if [[ "$DRY_RUN" == "false" ]]; then
      write_version PROW_VERSION "$TARGET_TAG"
      echo "  Updated: prow/config, prow/jobs and prow-mirror values"

      # Bump chart version
      if [[ -f "$CHART_FILE" ]]; then
        current_ver=$(yq '.version' "$CHART_FILE")
        IFS='.' read -r major minor patch <<< "$current_ver"
        new_ver="${major}.${minor}.$((patch + 1))"
        yq -i ".version = \"${new_ver}\"" "$CHART_FILE"
        echo "  Chart: $current_ver → $new_ver"
      fi
    else
      echo "  [dry-run] Would update all three values files: $CURRENT_TAG → $TARGET_TAG"
    fi
  fi

  # --- Tools version (label_sync, commenter from gcr.io/k8s-staging-test-infra) ---
  echo ""
  echo "Detecting latest tools image tag (label_sync, commenter)..."

  TOOLS_TAG=""
  if [[ "$BUMP_PATCH" == "true" ]]; then
    TOOLS_TAG=$(read_version TOOLS_VERSION)
  fi

  if [[ -z "$TOOLS_TAG" ]] && command -v crane >/dev/null 2>&1; then
    TOOLS_TAG=$(crane ls "gcr.io/k8s-staging-test-infra/label_sync" 2>/dev/null \
      | grep -E '^v[0-9]{8}-[a-f0-9]+$' \
      | sort -V \
      | tail -1)
  fi

  if [[ -z "$TOOLS_TAG" ]]; then
    TOOLS_TAG=$(curl -sL "https://gcr.io/v2/k8s-staging-test-infra/label_sync/tags/list" \
      | grep -oE '"v[0-9]{8}-[a-f0-9]+"' \
      | tr -d '"' \
      | sort -V \
      | tail -1)
  fi

  if [[ -n "$TOOLS_TAG" ]]; then
    CURRENT_TOOLS=$(read_version TOOLS_VERSION)
    if [[ "$CURRENT_TOOLS" == "$TOOLS_TAG" ]]; then
      echo "  Tools already at $TOOLS_TAG"
    else
      echo "  Current: $CURRENT_TOOLS"
      echo "  Latest:  $TOOLS_TAG"
      VERSION_CHANGED=true
      if [[ "$DRY_RUN" == "false" ]]; then
        write_version TOOLS_VERSION "$TOOLS_TAG"
        echo "  Updated TOOLS_VERSION"
      else
        echo "  [dry-run] Would update TOOLS_VERSION: $CURRENT_TOOLS → $TOOLS_TAG"
      fi
    fi
  else
    echo "  Could not detect tools version, skipping."
  fi

  # --- Patch revision (suffix on the mirrored, OS-patched image tags) ---
  echo ""
  echo "Resolving patch revision..."

  CURRENT_REV=$(read_version PROW_PATCH_REVISION)
  if [[ -z "$CURRENT_REV" || "$CURRENT_REV" == "null" ]]; then
    CURRENT_REV=0
  fi

  # Monotonic counter, never reset. PROW_VERSION and TOOLS_VERSION move
  # independently but share this one suffix, so resetting on a version change
  # would repoint the *untouched* tag at a revision that already exists in ECR.
  # The mirror's existence check would then skip the rebuild and that component
  # would silently roll back to its least-patched build. Always incrementing
  # guarantees a tag that does not exist yet, so every image gets rebuilt and
  # repatched. Cost is rebuilding images whose upstream tag did not move, which
  # is a fresh patch rather than a regression.
  if [[ "$VERSION_CHANGED" == "true" || "$BUMP_PATCH" == "true" ]]; then
    TARGET_REV=$((CURRENT_REV + 1))
    if [[ "$VERSION_CHANGED" == "true" ]]; then
      REV_REASON="upstream version changed"
    else
      REV_REASON="--bump-patch requested"
    fi
  else
    TARGET_REV="$CURRENT_REV"
    REV_REASON="no rebuild needed"
  fi

  if [[ "$TARGET_REV" == "$CURRENT_REV" ]]; then
    echo "  PROW_PATCH_REVISION stays at $CURRENT_REV ($REV_REASON)"
  elif [[ "$DRY_RUN" == "false" ]]; then
    write_version PROW_PATCH_REVISION "$TARGET_REV"
    echo "  PROW_PATCH_REVISION: $CURRENT_REV → $TARGET_REV ($REV_REASON)"
  else
    echo "  [dry-run] Would set PROW_PATCH_REVISION: $CURRENT_REV → $TARGET_REV ($REV_REASON)"
  fi

  echo ""
fi

# ─────────────────────────────────────────────────────────────────────────────
# CRD upgrade
# ─────────────────────────────────────────────────────────────────────────────

echo "Upgrading ProwJob CRD..."

tmpfile="$(mktemp)"
trap 'rm -f "$tmpfile"' EXIT

http_code=$(curl -sL -w "%{http_code}" -o "$tmpfile" "$CRD_UPSTREAM")
if [[ "$http_code" != "200" ]]; then
  echo "  error: download failed (HTTP $http_code)" >&2
  exit 1
fi

if ! grep -q "name: prowjobs.prow.k8s.io" "$tmpfile"; then
  echo "  error: not a valid ProwJob CRD" >&2
  exit 1
fi

if [[ -f "$CRD_FILE" ]] && diff -q "$CRD_FILE" "$tmpfile" >/dev/null 2>&1; then
  echo "  CRD already up to date."
else
  if [[ "$DRY_RUN" == "false" ]]; then
    cp "$tmpfile" "$CRD_FILE"
    crd_ver=$(grep "controller-gen.kubebuilder.io/version" "$CRD_FILE" | head -1 | sed 's/.*: //')
    echo "  CRD updated (controller-gen ${crd_ver})"
  else
    echo "  [dry-run] CRD has changes — would update."
  fi
fi

echo ""
echo "Done. Review with: git diff"
