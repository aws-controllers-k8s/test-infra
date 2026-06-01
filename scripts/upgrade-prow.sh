#!/usr/bin/env bash
#
# Upgrades Prow to the latest version.
#
# Updates the PROW_VERSION in the prow-version ConfigMap and upgrades the
# ProwJob CRD from upstream. All image references use Flux variable
# substitution (${PROW_IMAGE_REGISTRY}/${component}:${PROW_VERSION}), so
# updating the ConfigMap is all that's needed to roll out new images.
#
# Usage:
#   ./scripts/upgrade-prow.sh              # auto-detect latest tag
#   ./scripts/upgrade-prow.sh <tag>        # use a specific tag
#   ./scripts/upgrade-prow.sh --dry-run    # show what would change
#   ./scripts/upgrade-prow.sh --crd-only   # only upgrade the CRD
#
# Requires: curl, yq
# Optional: crane (faster tag detection), kubectl (CRD validation)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

REGISTRY="us-docker.pkg.dev/k8s-infra-prow/images"
VERSION_FILE="$REPO_ROOT/flux/prow/version/prow-version-configmap.yaml"
CRD_FILE="$REPO_ROOT/flux/prow/crds/prowjob_customresourcedefinition.yaml"
CRD_UPSTREAM="https://raw.githubusercontent.com/kubernetes-sigs/prow/main/config/prow/cluster/prowjob-crd/prowjob_customresourcedefinition.yaml"
CHART_FILE="$REPO_ROOT/prow/config/Chart.yaml"

# --- Parse arguments ---
DRY_RUN=false
CRD_ONLY=false
TARGET_TAG=""

for arg in "$@"; do
  case "$arg" in
    --dry-run)  DRY_RUN=true ;;
    --crd-only) CRD_ONLY=true ;;
    --help|-h)
      echo "Usage: $0 [--dry-run] [--crd-only] [<tag>]"
      echo ""
      echo "Upgrades Prow version and CRD."
      echo ""
      echo "Options:"
      echo "  --dry-run    Show what would change without modifying files"
      echo "  --crd-only   Only upgrade the ProwJob CRD"
      echo "  <tag>        Specific image tag (e.g., v20260519-c47e31ece)"
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
  CURRENT_TAG=$(yq '.data.PROW_VERSION' "$VERSION_FILE")

  if [[ "$CURRENT_TAG" == "$TARGET_TAG" ]]; then
    echo "  Already at $TARGET_TAG — no version change needed."
  else
    echo "  Current: $CURRENT_TAG"
    echo "  Target:  $TARGET_TAG"

    if [[ "$DRY_RUN" == "false" ]]; then
      yq -i ".data.PROW_VERSION = \"${TARGET_TAG}\"" "$VERSION_FILE"
      echo "  Updated: $VERSION_FILE"

      # Bump chart version
      if [[ -f "$CHART_FILE" ]]; then
        current_ver=$(yq '.version' "$CHART_FILE")
        IFS='.' read -r major minor patch <<< "$current_ver"
        new_ver="${major}.${minor}.$((patch + 1))"
        yq -i ".version = \"${new_ver}\"" "$CHART_FILE"
        echo "  Chart: $current_ver → $new_ver"
      fi
    else
      echo "  [dry-run] Would update $VERSION_FILE: $CURRENT_TAG → $TARGET_TAG"
    fi
  fi

  # --- Tools version (label_sync, commenter from gcr.io/k8s-staging-test-infra) ---
  echo ""
  echo "Detecting latest tools image tag (label_sync, commenter)..."

  TOOLS_TAG=""
  if command -v crane >/dev/null 2>&1; then
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
    CURRENT_TOOLS=$(yq '.data.TOOLS_VERSION' "$VERSION_FILE")
    if [[ "$CURRENT_TOOLS" == "$TOOLS_TAG" ]]; then
      echo "  Tools already at $TOOLS_TAG"
    else
      echo "  Current: $CURRENT_TOOLS"
      echo "  Latest:  $TOOLS_TAG"
      if [[ "$DRY_RUN" == "false" ]]; then
        yq -i ".data.TOOLS_VERSION = \"${TOOLS_TAG}\"" "$VERSION_FILE"
        echo "  Updated TOOLS_VERSION"
      else
        echo "  [dry-run] Would update TOOLS_VERSION: $CURRENT_TOOLS → $TOOLS_TAG"
      fi
    fi
  else
    echo "  Could not detect tools version, skipping."
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
