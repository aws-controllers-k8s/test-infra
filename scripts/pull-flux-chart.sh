#!/usr/bin/env bash
#
# Pulls the flux2 community Helm chart and vendors it into charts/flux2/.
# The chart is committed extracted (not as a .tgz) so Flux can reference
# it directly as a local chart path from the GitRepository source.
#
# Usage:
#   ./scripts/pull-flux-chart.sh          # pulls version from flux/flux-version.yaml
#   ./scripts/pull-flux-chart.sh 2.8.6    # pulls a specific version
#
# Requires: helm, yq. Safe to re-run; replaces the chart directory in place.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CHARTS_DIR="$REPO_ROOT/charts"
VERSION_FILE="$REPO_ROOT/flux/flux-self/version-configmap.yaml"
UPSTREAM_REPO="https://fluxcd-community.github.io/helm-charts"

if ! command -v helm >/dev/null 2>&1; then
  echo "error: helm is required but not on PATH" >&2
  exit 1
fi

version="${1:-}"
if [[ -z "$version" ]]; then
  if ! command -v yq >/dev/null 2>&1; then
    echo "error: yq is required to read version from $VERSION_FILE" >&2
    exit 1
  fi
  version="$(yq '.data.FLUX_VERSION' "$VERSION_FILE")"
  if [[ -z "$version" || "$version" == "null" ]]; then
    echo "error: could not determine version from $VERSION_FILE" >&2
    echo "  pass the version as the first argument" >&2
    exit 1
  fi
fi

# Strip leading 'v' if present - Helm chart versions don't use it
version="${version#v}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

echo "Pulling flux2 chart version $version from $UPSTREAM_REPO"
helm pull flux2 \
  --repo "$UPSTREAM_REPO" \
  --version "$version" \
  --destination "$tmpdir" \
  --untar

src="$tmpdir/flux2"
if [[ ! -d "$src" ]]; then
  echo "error: helm pull did not produce extracted chart at $src" >&2
  exit 1
fi

# Replace the vendored chart directory
rm -rf "$CHARTS_DIR/flux2-"*
mkdir -p "$CHARTS_DIR"
mv "$src" "$CHARTS_DIR/flux2-${version}"

echo "Wrote charts/flux2-${version}/ (version $version)"

# Update the version file if a version was passed explicitly
if [[ -n "${1:-}" ]]; then
  if command -v yq >/dev/null 2>&1; then
    yq -i ".data.FLUX_VERSION = \"$version\"" "$VERSION_FILE"
    echo "Updated $VERSION_FILE to version $version"
  fi
fi

echo ""
echo "Next steps:"
echo "  git add charts/flux2-${version}/ flux/flux-self/version-configmap.yaml"
echo "  git commit -m \"chore(flux): vendor flux2 chart ${version}\""
