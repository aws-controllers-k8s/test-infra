#!/usr/bin/env bash
#
# Upgrades the aws-secrets-store-csi-driver-provider EKS addon version in
# the ACK addon manifests.
#
# Discovers the latest addon version using `aws eks describe-addon-versions`
# and updates the Addon resources in both test-infra and test-infra-upgrade
# manifest files to pin that version.
#
# Usage:
#   ./scripts/upgrade-secrets-csi-driver.sh              # auto-detect latest
#   ./scripts/upgrade-secrets-csi-driver.sh --default    # use EKS default version (recommended)
#   ./scripts/upgrade-secrets-csi-driver.sh <version>    # use specific version
#   ./scripts/upgrade-secrets-csi-driver.sh --dry-run    # show what would change
#
# Requires: aws CLI, yq
# Optional: --kubernetes-version flag to constrain compatibility

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

ADDON_NAME="aws-secrets-store-csi-driver-provider"
ADDON_FILES=(
  "$REPO_ROOT/flux/ack/cluster/addons/addons.yaml"
)

# Also update the upgrade test infra if it exists as a sibling repo
UPGRADE_REPO="$(cd "$REPO_ROOT/../test-infra-upgrade" 2>/dev/null && pwd || echo "")"
if [[ -n "$UPGRADE_REPO" && -f "$UPGRADE_REPO/flux/ack/cluster/addons/addons.yaml" ]]; then
  ADDON_FILES+=("$UPGRADE_REPO/flux/ack/cluster/addons/addons.yaml")
fi

# --- Parse arguments ---
DRY_RUN=false
USE_DEFAULT=false
TARGET_VERSION=""
K8S_VERSION=""

for arg in "$@"; do
  case "$arg" in
    --dry-run)
      DRY_RUN=true
      ;;
    --default)
      USE_DEFAULT=true
      ;;
    --help|-h)
      echo "Usage: $0 [--dry-run] [--default] [--kubernetes-version=X.XX] [<version>]"
      echo ""
      echo "Upgrades the $ADDON_NAME EKS addon version."
      echo ""
      echo "Options:"
      echo "  --default                  Use the EKS default version for the Kubernetes version (recommended)"
      echo "  --dry-run                  Show what would change without modifying files"
      echo "  --kubernetes-version=X.XX  Constrain to versions compatible with this K8s version"
      echo "  <version>                  Specific addon version (e.g., v1.0.0-eksbuild.1)"
      exit 0
      ;;
    --kubernetes-version=*)
      K8S_VERSION="${arg#--kubernetes-version=}"
      ;;
    v*)
      TARGET_VERSION="$arg"
      ;;
    *)
      echo "error: unknown argument: $arg" >&2
      exit 1
      ;;
  esac
done

# ─────────────────────────────────────────────────────────────────────────────
# Prerequisites
# ─────────────────────────────────────────────────────────────────────────────

if ! command -v aws >/dev/null 2>&1; then
  echo "error: aws CLI is required" >&2
  exit 1
fi

if ! command -v yq >/dev/null 2>&1; then
  echo "error: yq is required (https://github.com/mikefarah/yq)" >&2
  exit 1
fi

# ─────────────────────────────────────────────────────────────────────────────
# Discover version
# ─────────────────────────────────────────────────────────────────────────────

if [[ -z "$TARGET_VERSION" ]]; then
  if [[ "$USE_DEFAULT" == "true" ]]; then
    echo "Discovering default $ADDON_NAME version..."

    # Query for the version marked as default for the given Kubernetes version.
    # The defaultVersion field in compatibilities indicates EKS's recommended version.
    EKS_ARGS=(eks describe-addon-versions --addon-name "$ADDON_NAME"
      --query 'addons[0].addonVersions[?compatibilities[0].defaultVersion==`true`].addonVersion | [0]'
      --output text)

    if [[ -n "$K8S_VERSION" ]]; then
      EKS_ARGS+=(--kubernetes-version "$K8S_VERSION")
      echo "  Constraining to Kubernetes version: $K8S_VERSION"
    fi
  else
    echo "Discovering latest $ADDON_NAME version..."

    EKS_ARGS=(eks describe-addon-versions --addon-name "$ADDON_NAME"
      --query 'addons[0].addonVersions[0].addonVersion'
      --output text)

    if [[ -n "$K8S_VERSION" ]]; then
      EKS_ARGS+=(--kubernetes-version "$K8S_VERSION")
      echo "  Constraining to Kubernetes version: $K8S_VERSION"
    fi
  fi

  TARGET_VERSION=$(aws "${EKS_ARGS[@]}" 2>/dev/null)

  if [[ -z "$TARGET_VERSION" || "$TARGET_VERSION" == "None" ]]; then
    echo "error: could not discover version for $ADDON_NAME" >&2
    echo "  Ensure the AWS CLI is configured and has EKS access." >&2
    echo "  Or provide a version manually: $0 v1.0.0-eksbuild.1" >&2
    exit 1
  fi

  if [[ "$USE_DEFAULT" == "true" ]]; then
    echo "  Default: $TARGET_VERSION"
  else
    echo "  Latest: $TARGET_VERSION"
  fi
fi

echo ""

# ─────────────────────────────────────────────────────────────────────────────
# Update manifests
# ─────────────────────────────────────────────────────────────────────────────

for addon_file in "${ADDON_FILES[@]}"; do
  if [[ ! -f "$addon_file" ]]; then
    echo "  Skipping (not found): $addon_file"
    continue
  fi

  echo "Processing: $addon_file"

  # Find the document index for the secrets-store-csi addon
  # The file contains multiple YAML documents separated by ---
  doc_index=$(yq eval-all 'select(.metadata.name == "secrets-store-csi") | documentIndex' "$addon_file" 2>/dev/null)

  if [[ -z "$doc_index" ]]; then
    echo "  warning: no secrets-store-csi Addon found in $addon_file" >&2
    continue
  fi

  # Check current version (may be empty if not pinned)
  current_version=$(yq eval "select(documentIndex == $doc_index) | .spec.addonVersion // \"(not pinned)\"" "$addon_file")

  if [[ "$current_version" == "$TARGET_VERSION" ]]; then
    echo "  Already at $TARGET_VERSION — no change needed."
    continue
  fi

  echo "  Current: $current_version"
  echo "  Target:  $TARGET_VERSION"

  if [[ "$DRY_RUN" == "false" ]]; then
    yq eval -i "select(documentIndex == $doc_index) | .spec.addonVersion = \"$TARGET_VERSION\"" "$addon_file"
    echo "  Updated."
  else
    echo "  [dry-run] Would set .spec.addonVersion = \"$TARGET_VERSION\""
  fi

  echo ""
done

echo "Done. Review with: git diff"
