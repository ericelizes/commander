#!/usr/bin/env bash
# cockpit-test.sh — drive the commander cockpit for automated/agent testing.
#
# Model: the cockpit runs as a DETACHED tmux session ("commander"). Functional
# checks (text capture, key input) work on that session with NO GUI window and
# NO display at all — ideal for headless agents and CI.
#
# Usage:
#   scripts/cockpit-test.sh up           # create/refresh the detached cockpit session
#   scripts/cockpit-test.sh text         # capture-pane every cockpit pane as text
#   scripts/cockpit-test.sh keys "..."   # send-keys to the cockpit (e.g. "Down" or "hi" Enter)
#   scripts/cockpit-test.sh down         # end the cockpit session
#
# Env: COCKPIT_SESSION (default commander), COCKPIT_WS (default 99),
#      COCKPIT_TMUX_TMPDIR (default /tmp/cl.cockpit),
#      COCKPIT_HOME (default ${COCKPIT_TMUX_TMPDIR}.home, a sibling dir), COCKPIT_CMDR (cmdr binary
#      to run; default: built from this checkout into the isolated bin dir)
#
# SAFETY: this script isolates cooperative cmdr builds, not arbitrary code. It runs
# cmdr with a throwaway HOME and COMMANDER_HOME (marker .cockpit-isolated), every
# COMMANDER_* variable of the caller dropped (no role, no real session ids), the
# runtime pinned to opencode, mail pointed at a dead port, and a PATH where
# opencode is tests/evals/fakeopencode and claude/codex are stubs that fail. It
# refuses a COCKPIT_HOME that is (or holds) the real ~/.commander or the
# caller's COMMANDER_HOME. For other live checks use `cmdr board`,
# `cmdr app shot` or `just app-shot`.
#
# Isolation: cmdr hard-codes the "commander" session name, so the cockpit runs on
# its OWN tmux server: every tmux call (this script's, and cmdr's through a shim
# on PATH) runs with $TMUX unset and -S "$TMUX_TMPDIR/cockpit.sock", rejecting
# unowned directories and servers before session operations. Keep the dir
# short: socket paths over ~104 chars fail.
set -euo pipefail

unset TMUX
REAL_HOME="$HOME"
CALLER_CH="${COMMANDER_HOME:-}"
export TMUX_TMPDIR="${COCKPIT_TMUX_TMPDIR:-/tmp/cl.cockpit}"
SOCK="$TMUX_TMPDIR/cockpit.sock"
TMUX_REAL="$(command -v tmux || true)"
# The one tmux entry point of the setup phase (the driver has its own below).
tm() { env -u TMUX "$TMUX_REAL" -S "$SOCK" "$@"; }

die() { echo "cockpit-test.sh: refusing: $*" >&2; exit 3; }
canon() {
  if [ -d "$1" ]; then (cd "$1" && pwd -P)
  else printf '%s/%s\n' "$(canon "$(dirname "$1")")" "$(basename "$1")"; fi
}

# ---- isolation: decided before anything runs -------------------------------
ISO="${COCKPIT_HOME:-${TMUX_TMPDIR}.home}"
[ ! -L "$ISO" ] || die "linked isolation root"
ISO="$(canon "$ISO")"
for real in "$REAL_HOME/.commander" "$CALLER_CH" "${COMMANDER_HOME_REAL:-}"; do
  [ -n "$real" ] || continue
  r="$(canon "$real")"
  socket_root="$(canon "$TMUX_TMPDIR")"
  case "$socket_root/" in "$r/"*) die "socket directory is inside real commander home";; esac
  case "$r/" in "$socket_root/"*) die "socket directory contains real commander home";; esac
  case "$ISO/" in "$r/"*) die "COCKPIT_HOME $ISO is inside the real commander home $r. Use a temp dir (default ${TMUX_TMPDIR}.home).";; esac
  case "$r/" in "$ISO/"*) die "COCKPIT_HOME $ISO contains the real commander home $r. Use a temp dir.";; esac
done
case "$ISO" in "$REAL_HOME"|"$REAL_HOME/.commander"|"$REAL_HOME/.config"*|/) die "COCKPIT_HOME $ISO is a real directory.";; esac
mkdir -p "$ISO"
# Validate the entire reused tree before any writes, including dangling links,
# multiply-linked files and protected paths nested under a marked root.
[ -z "$(find "$ISO" -type l -print -quit)" ] || die "linked isolation descendant"
[ -z "$(find "$ISO" -type f -links +1 -print -quit)" ] || die "hard-linked isolation file"
[ -z "$(find "$ISO" ! -user "$(id -un)" -print -quit)" ] || die "foreign-owned isolation descendant"
if [ ! -e "$ISO/.cockpit-isolated" ]; then
  [ -z "$(ls -A "$ISO")" ] || die "COCKPIT_HOME $ISO is not empty and was not made by this script."
  : > "$ISO/.cockpit-isolated"
fi
ISO_HOME="$ISO/home"; ISO_CH="$ISO/ch"; ISO_BIN="$ISO/bin"
mkdir -p "$ISO_HOME" "$ISO_CH" "$ISO_BIN"

# Socket directories must be created here, never adopted from a caller/server.
[ ! -L "$TMUX_TMPDIR" ] || die "linked socket directory"
if [ ! -e "$TMUX_TMPDIR/.cockpit-owner" ]; then
  if [ -d "$TMUX_TMPDIR" ]; then
    [ -z "$(ls -A "$TMUX_TMPDIR")" ] || die "foreign socket directory"
    rmdir "$TMUX_TMPDIR"
  fi
  mkdir -m 700 "$TMUX_TMPDIR" || die "cannot create socket directory"
  printf '%s\n' "$ISO" > "$TMUX_TMPDIR/.cockpit-owner"
fi
[ -O "$TMUX_TMPDIR" ] && [ ! -L "$TMUX_TMPDIR/.cockpit-owner" ] || die "foreign socket owner"
[ "$(cat "$TMUX_TMPDIR/.cockpit-owner")" = "$ISO" ] || die "socket directory belongs to another home"
[ -z "$(find "$TMUX_TMPDIR" -type l -print -quit)" ] || die "linked socket descendant"
# A unique server token distinguishes a server started with our environment.
if [ ! -e "$ISO/.server-token" ]; then
  token=$(mktemp "$ISO/token.XXXXXX")
  printf '%s\n' "$token" > "$ISO/.server-token"
  rm "$token"
fi
export COCKPIT_SERVER_TOKEN="$(cat "$ISO/.server-token")"
[ -n "$TMUX_REAL" ] || die "tmux not found"
if [ -e "$SOCK" ]; then
  # A server is (or was) on our socket: it must answer and carry our token.
  tok="$(tm show-environment -g COCKPIT_SERVER_TOKEN 2>/dev/null)" || tok="UNREADABLE"
  [ "$tok" = "COCKPIT_SERVER_TOKEN=$COCKPIT_SERVER_TOKEN" ] || die "foreign or unreadable tmux server on $SOCK (if its server is gone, remove this stale socket file by hand)"
fi
repo="$(cd "$(dirname "$0")/.." && pwd -P)"
rm -f "$ISO_BIN/opencode" "$ISO_BIN/claude" "$ISO_BIN/codex"
if [ -z "${COCKPIT_FAKE_OPENCODE:-}" ]; then
  (cd "$repo" && go build -o "$ISO_BIN/opencode" ./tests/evals/fakeopencode) || die "could not build the fake opencode (tests/evals/fakeopencode)"
elif [ -n "${COCKPIT_FAKE_OPENCODE:-}" ]; then
  cp "$COCKPIT_FAKE_OPENCODE" "$ISO_BIN/opencode"
fi
for real_agent in claude codex; do
  printf '#!/bin/sh\necho "cockpit-test.sh: real %s is disabled in the isolated cockpit" >&2\nexit 1\n' "$real_agent" > "$ISO_BIN/$real_agent"
  chmod +x "$ISO_BIN/$real_agent"
done
if [ -n "${COCKPIT_CMDR:-}" ]; then
  rm -f "$ISO_BIN/cmdr"
  cp "$COCKPIT_CMDR" "$ISO_BIN/cmdr"
else
  rm -f "$ISO_BIN/cmdr"
  (cd "$repo" && go build -o "$ISO_BIN/cmdr" ./cmd/cmdr) || die "could not build cmdr"
fi

# The command body contains no setup bypass entry point.
sed -n '/^SESSION=/,$p' "$repo/scripts/cockpit-test.sh" > "$ISO/driver.sh"
# Everything below runs with the isolated environment; no COMMANDER_* leaks in.
for v in $(env | sed -n 's/^\(COMMANDER_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done
unset CLAUDE_CONFIG_DIR CODEX_HOME CLAUDE_SESSION_ID
export HOME="$ISO_HOME" COMMANDER_HOME="$ISO_CH" COMMANDER_RUNTIME=opencode
export COMMANDER_CODEX_OPERATOR_SHARE=0 COMMANDER_CODEX_MANAGER_SHARE=0
export AM_HTTP_URL=http://127.0.0.1:1
# Only explicitly selected tools are reachable through PATH. Never carry a
# caller's agent executable directories into a pane or a tmux server.
mkdir -p "$ISO/tools"
for tool in sh bash sed sleep env ps cat mkdir dirname stty tput clear date tail head awk; do
  source_tool=$(command -v "$tool" || true)
  if [ -n "$source_tool" ]; then
    rm -f "$ISO/tools/$tool"
    if [ "$(uname -s)" = Darwin ]; then
      # macOS rejects relocated platform-signed binaries with SIGKILL. Use
      # exact-path wrappers for this explicit tool allowlist, never caller PATH.
      printf '#!/bin/bash\nexec %q "$@"\n' "$source_tool" > "$ISO/tools/$tool"
      chmod +x "$ISO/tools/$tool"
    else
      cp "$source_tool" "$ISO/tools/$tool"
    fi
  fi
done
# rm first: a running server executes this file (cp onto it is ETXTBSY).
rm -f "$ISO/tools/tmux.real"
cp "$TMUX_REAL" "$ISO/tools/tmux.real"
# `tmux` on PATH (what cmdr runs) is this shim: same socket, TMUX unset.
printf '#!/bin/sh\nunset TMUX\nexec "%s" -S "%s" "$@"\n' "$ISO/tools/tmux.real" "$SOCK" > "$ISO/tools/tmux"
chmod +x "$ISO/tools/tmux"
export PATH="$ISO_BIN:$ISO/tools"
# Drop all inherited configuration, executable overrides, credentials and shell
# startup variables before invoking cmdr or tmux. Re-enter only after setup.
exec env -i HOME="$HOME" COMMANDER_HOME="$COMMANDER_HOME" \
  COMMANDER_RUNTIME=opencode COMMANDER_CODEX_OPERATOR_SHARE=0 COMMANDER_CODEX_MANAGER_SHARE=0 \
  AM_HTTP_URL="$AM_HTTP_URL" PATH="$PATH" TMUX_TMPDIR="$TMUX_TMPDIR" \
  COCKPIT_SERVER_TOKEN="$COCKPIT_SERVER_TOKEN" COCKPIT_TM="$ISO/tools/tmux" TERM="${TERM:-xterm-256color}" \
  COCKPIT_SESSION="${COCKPIT_SESSION:-commander}" COCKPIT_WS="${COCKPIT_WS:-99}" \
  COCKPIT_COLS="${COCKPIT_COLS:-220}" COCKPIT_ROWS="${COCKPIT_ROWS:-50}" \
  bash "$ISO/driver.sh" "$@"

SESSION="${COCKPIT_SESSION:-commander}"
WS="${COCKPIT_WS:-99}"
COLS="${COCKPIT_COLS:-220}"
ROWS="${COCKPIT_ROWS:-50}"

# The only tmux entry point here: the shim made by the setup phase.
tm() { "$COCKPIT_TM" "$@"; }

ensure_session() {
  if ! tm has-session -t "$SESSION" 2>/dev/null; then
    # Creates the detached session; attach fails w/o a TTY (harmless). cmdr refuses
    # with no terminal, so name the one waiver; this script has already dropped any role.
    COMMANDER_COCKPIT_NO_TERMINAL=1 cmdr >/dev/null 2>&1 || true
  fi
  tm has-session -t "$SESSION" 2>/dev/null || { echo "failed to create '$SESSION' session" >&2; exit 1; }
  tm set-option -t "$SESSION" window-size manual 2>/dev/null || true
  tm resize-window -t "$SESSION" -x "$COLS" -y "$ROWS" 2>/dev/null || true
}

cmd_up() {
  ensure_session
  echo "cockpit '$SESSION' up (detached, ${COLS}x${ROWS}):"
  tm list-panes -t "$SESSION" -F '  #{pane_id} #{pane_current_command} #{pane_width}x#{pane_height}'
}

cmd_text() {
  tm has-session -t "$SESSION" 2>/dev/null || { echo "no '$SESSION' session; run 'up' first" >&2; exit 1; }
  for p in $(tm list-panes -t "$SESSION" -F '#{pane_id}'); do
    echo "===== pane $p [$(tm display -p -t "$p" '#{pane_current_command} #{pane_width}x#{pane_height}')] ====="
    tm capture-pane -t "$p" -p | sed 's/[[:space:]]*$//'
  done
}

cmd_keys() {
  tm has-session -t "$SESSION" 2>/dev/null || { echo "no '$SESSION' session; run 'up' first" >&2; exit 1; }
  tm send-keys -t "$SESSION" "$@"
}

cmd_down() {
  tm kill-session -t "$SESSION" 2>/dev/null || true
  echo "torn down"
}

case "${1:-}" in
  up) cmd_up ;;
  text) cmd_text ;;
  keys) shift; cmd_keys "$@" ;;
  down) cmd_down ;;
  *) echo "usage: $0 {up|text|keys \"...\"|down}" >&2; exit 2 ;;
esac
