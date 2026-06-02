#!/usr/bin/env bash

# generate_attribution installs attribution-gen (if needed) and regenerates
# ATTRIBUTION.md for a given controller directory.
#
# Usage:
#   generate_attribution "/path/to/controller-dir"
#
# Returns 0 on success, 1 on failure to install attribution-gen,
# and 0 (with a warning) if generation itself fails.
generate_attribution() {
  if [[ $# -ne 1 ]]; then
    echo "attribution.sh][ERROR] generate_attribution requires one argument: CONTROLLER_DIR"
    return 1
  fi

  local __controller_dir=$1

  if ! command -v attribution-gen &>/dev/null; then
    echo "attribution.sh][INFO] Installing attribution-gen ..."
    if ! go install github.com/awslabs/attribution-gen@latest >/dev/null 2>&1; then
      echo "attribution.sh][ERROR] Failed to install attribution-gen"
      return 1
    fi
  fi

  echo -n "attribution.sh][INFO] Generating ATTRIBUTION.md for $(basename "$__controller_dir") ... "
  if attribution-gen --modfile "$__controller_dir/go.mod" --output "$__controller_dir/ATTRIBUTION.md" >/dev/null 2>&1; then
    echo "ok"
  else
    echo "failed (skipping)"
  fi
}
