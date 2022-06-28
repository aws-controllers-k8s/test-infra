#!/usr/bin/env bash

# check_is_installed checks to see if the supplied executable is installed and
# exits if not. An optional second argument is an extra message to display when
# the supplied executable is not installed.
#
# Usage:
#
#   check_is_installed PROGRAM [ MSG ]
#
# Example:
#
#   check_is_installed kind "You can install kind with the helper scripts/install-kind.sh"
check_is_installed() {
    local __name="$1"
    local __extra_msg="$2"
    if ! is_installed "$__name"; then
        echo "FATAL: Missing requirement '$__name'"
        echo "Please install $__name before running this script."
        if [[ -n $__extra_msg ]]; then
            echo ""
            echo "$__extra_msg"
            echo ""
        fi
        exit 1
    fi
}

is_installed() {
    local __name="$1"
    if $(which $__name >/dev/null 2>&1); then
        return 0
    else
        return 1
    fi
}

# filenoext returns just the name of the supplied filename without the
# extension
filenoext() {
    local __name="$1"
    local __filename=$( basename "$__name" )
    # How much do I despise Bash?!
    echo "${__filename%.*}"
}

perform_helm_login() {
  #ecr-public only exists in us-east-1 so use that region specifically
  echo "$__pw" | helm registry login -u AWS --password-stdin public.ecr.aws
}

ensure_binaries() {
    check_is_installed "helm"
}

ensure_binaries