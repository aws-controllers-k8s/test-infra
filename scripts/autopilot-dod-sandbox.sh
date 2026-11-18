#!/usr/bin/env bash
# Runs one command ("$@") of ack-dod's build check (go build/vet/test/mod
# tidy on the PR head) as an unprivileged uid, for the Dispatcher's
# autopilot-dod job (ack-build-tools autopilot dod --build-sandbox).
#
# Why (Plan 9 review F1: PR code must not be able to write the verdict): the
# build check runs the PR's own code (tests, init functions, go:generate'd
# helpers). In the dod job that is the same pod that then writes
# $ARTIFACTS/verdict.json. Run as the same uid, a test could leave a detached
# process that rewrites the verdict after dod writes it. So every build step
# runs as uid/gid 65532 with no groups, no new privileges and a clean
# environment, cannot write anything the root-owned dod process owns, and
# every process of that uid is killed when the step exits.
#
# It must run as root, and it fails closed (exit 125, which dod records as
# BUILD_SANDBOX_FAILED) when it is not root, when setpriv, pkill or pgrep is
# missing, or when a sandbox process survives the kill.
#
# Environment:
#   AUTOPILOT_DOD_SANDBOX_DIR  root of the sandbox's HOME, GOPATH, GOMODCACHE,
#                              GOCACHE and TMPDIR (default /tmp/dod-sandbox)
#   GOFLAGS, GOPROXY, GONOSUMDB, GOPRIVATE  passed only if set
#   CGO_ENABLED                passed (default 0)
set -uo pipefail

SANDBOX_UID=65532
fail() { echo "autopilot-dod-sandbox: $*" >&2; exit 125; }

[ "$#" -gt 0 ] || fail "no command"
[ "$(id -u)" = "0" ] || fail "must run as root"
for tool in setpriv pkill pgrep; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is missing"
done

# The sandbox root is root-owned (0755) so the sandbox uid cannot replace it
# or its subdirectories (for example with a symlink root would then follow);
# only the subdirectories are the sandbox uid's.
SB="${AUTOPILOT_DOD_SANDBOX_DIR:-/tmp/dod-sandbox}"
case "$SB" in /*) ;; *) fail "AUTOPILOT_DOD_SANDBOX_DIR must be absolute" ;; esac
if [ -e "$SB" ] || [ -L "$SB" ]; then
  [ -d "$SB" ] && [ ! -L "$SB" ] && [ "$(stat -c %u "$SB")" = "0" ] || fail "$SB is not a root-owned directory"
else
  mkdir -m 0755 "$SB" || fail "could not create $SB"
fi
chmod 0755 "$SB" || fail "could not chmod $SB"
for d in home gopath gomodcache gocache tmp; do
  if [ -L "$SB/$d" ]; then fail "$SB/$d is a symlink"; fi
  mkdir -p "$SB/$d" || fail "could not create $SB/$d"
  chown -h "$SANDBOX_UID:$SANDBOX_UID" "$SB/$d" || fail "could not chown $SB/$d"
done

# The sandbox uid reads the checkout; everything stays owned by root.
chmod -R go+rX "$PWD" || fail "could not make $PWD readable"
d="$PWD"
while [ "$d" != "/" ]; do
  d="$(dirname "$d")"
  chmod o+x "$d" || fail "could not open $d"
done

envs=(PATH="$PATH"
  HOME="$SB/home" GOPATH="$SB/gopath" GOMODCACHE="$SB/gomodcache"
  GOCACHE="$SB/gocache" TMPDIR="$SB/tmp"
  CGO_ENABLED="${CGO_ENABLED:-0}")
for v in GOFLAGS GOPROXY GONOSUMDB GOPRIVATE; do
  if [ -n "${!v+x}" ]; then envs+=("$v=${!v}"); fi
done

setpriv --reuid=65532 --regid=65532 --clear-groups --no-new-privs \
  env -i "${envs[@]}" "$@"
code=$?

# Whatever the command's status: no process of the sandbox uid outlives it.
pkill -9 -U 65532
for _ in $(seq 1 20); do
  pgrep -U 65532 >/dev/null || exit "$code"
  sleep 0.1
done
fail "processes of uid $SANDBOX_UID survived the kill"
