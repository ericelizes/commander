#!/bin/sh
# Install this wrapper as the former bd entry point, after preserving that
# executable as bd.pre-postgres. The selector is authoritative even if invalid.
set -eu
ch=${COMMANDER_HOME:-"$HOME/.commander"}
if [ -e "$ch/state.postgres" ] || [ -L "$ch/state.postgres" ]; then
  exec "$HOME/.commander/bin/cmdr" beads-compat "$@"
fi
case "$0" in */*) legacy="${0%/*}/bd.pre-postgres";; *) echo 'Beads fence requires an absolute installed entry point' >&2; exit 1;; esac
if [ -e "$ch/storage-cutover" ]; then
  # Before selection, all legacy invocations are read-only, including callers
  # that omit --readonly. The upstream client enforces the operation restriction.
  exec "$legacy" --readonly "$@"
fi
exec "$legacy" "$@"
