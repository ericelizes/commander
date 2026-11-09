#!/usr/bin/env bash
# app-dev.sh is the desktop app's dev loop: make sure cmdrd is answering,
# then run `npm run dev` in app/ (Vite HMR for the renderer, Electron main
# restarts on change). See app/build/INTEGRATION.md.
#
#   scripts/app-dev.sh             # cmdrd + Electron dev window
#   scripts/app-dev.sh --web       # cmdrd + renderer only (Vite in a browser)
#   scripts/app-dev.sh --check     # just ensure cmdrd and print its URL
#
# On Linux with no DISPLAY/WAYLAND_DISPLAY (a VM over ssh) it switches to
# --web automatically: Vite listens on 127.0.0.1:5173, forward the port
# (ssh -L 5173:127.0.0.1:5173) and open the printed URL.
# Honors COMMANDER_HOME (temp homes for testing).
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOME_DIR="${COMMANDER_HOME:-$HOME/.commander}"
MODE=electron
case "${1:-}" in
  --web) MODE=web ;;
  --check) MODE=check ;;
  "") ;;
  *) echo "usage: $0 [--web|--check]" >&2; exit 2 ;;
esac
if [[ "$(uname -s)" == Linux && -z "${DISPLAY:-}" && -z "${WAYLAND_DISPLAY:-}" && $MODE == electron ]]; then
  echo "no display: running the renderer only (--web)"
  MODE=web
fi

api_url() { tr -d '[:space:]' < "$HOME_DIR/api-url" 2>/dev/null | sed 's:/*$::'; }
daemon_up() {
  local u
  u="$(api_url)"
  [[ -n "$u" ]] && curl -fsS -m 1 -o /dev/null "$u/"
}

if ! daemon_up; then
  echo "cmdrd is down; starting it"
  case "$(uname -s)" in
    Darwin)
      source "$REPO/install/legacy-launchd.sh"
      launchctl kickstart "gui/$(id -u)/org.commander" 2>/dev/null || kick_legacy_daemon 2>/dev/null || true
      ;;
    Linux) systemctl --user start commander.service 2>/dev/null || started=0 ;;
  esac
  if [[ "${started:-1}" == 0 ]]; then
    command -v cmdrd >/dev/null || { echo "cmdrd not on PATH; run just install" >&2; exit 1; }
    mkdir -p "$HOME_DIR/logs"
    nohup cmdrd --notifications=false >>"$HOME_DIR/logs/cmdrd-dev.log" 2>&1 &
    echo "started cmdrd (pid $!, log $HOME_DIR/logs/cmdrd-dev.log)"
  fi
  for _ in $(seq 40); do daemon_up && break; sleep 0.25; done
  daemon_up || { echo "cmdrd did not come up; see $HOME_DIR/logs" >&2; exit 1; }
fi
echo "cmdrd: $(api_url)"
[[ $MODE == check ]] && exit 0

cd "$REPO/app"
[[ -d node_modules ]] || npm ci
if [[ $MODE == web ]]; then
  echo "renderer: http://127.0.0.1:5173/#token=$(tr -d '[:space:]' < "$HOME_DIR/api-token")&api=$(api_url)"
  exec npm run dev:web
fi
exec npm run dev
