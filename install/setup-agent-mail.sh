#!/usr/bin/env bash
# setup-agent-mail.sh — install Agent Mail (`am`) and get its server running,
# which commander needs for Commander↔Manager↔Operator mail and for adding
# projects (see docs/agent-mail-routing.md).
#
# Idempotent. The upstream installer installs am to ~/.local/bin, writes
# HTTP_BEARER_TOKEN to ~/.config/mcp-agent-mail/config.env, and runs the
# server as a background service (launchd com.agent-mail on macOS, a systemd
# user unit on Linux). It also registers mcp-agent-mail with detected MCP
# clients (Claude Code user scope, Codex); commander's prompts deny those raw
# tools and talk to the server over HTTP instead.
set -euo pipefail

INSTALLER_URL="https://raw.githubusercontent.com/Dicklesworthstone/mcp_agent_mail_rust/main/install.sh"
HEALTH_URL="http://127.0.0.1:8765/health/liveness"
CONFIG_ENV="${XDG_CONFIG_HOME:-$HOME/.config}/mcp-agent-mail/config.env"
export PATH="$HOME/.local/bin:$PATH"

ok() { echo "  ✓ $*"; }
die() { echo "  ✗ $*" >&2; exit 1; }

echo "agent-mail setup"

# The installer refuses to run without minisign (release signature check).
if ! command -v minisign >/dev/null 2>&1; then
  if [[ "$OSTYPE" == darwin* ]] && command -v brew >/dev/null 2>&1; then
    brew install minisign >/dev/null
  else
    die "minisign missing — install it (apt install minisign) and re-run"
  fi
fi
ok "minisign"

if command -v am >/dev/null 2>&1; then
  ok "am already installed ($(am --version 2>/dev/null | head -1))"
else
  # Run from an empty dir: the installer writes project-local MCP configs
  # (cursor.mcp.json, opencode.json — with the bearer token) and edits
  # .gitignore in its working directory, which would land in this repo.
  (cd "$(mktemp -d)" && curl -fsSL "$INSTALLER_URL" | bash -s -- --yes)
  command -v am >/dev/null 2>&1 || die "am not on PATH after install (expected ~/.local/bin/am)"
  ok "installed $(am --version 2>/dev/null | head -1)"
fi

grep -q '^HTTP_BEARER_TOKEN=.' "$CONFIG_ENV" 2>/dev/null \
  || die "no HTTP_BEARER_TOKEN in $CONFIG_ENV — re-run with: curl -fsSL $INSTALLER_URL | bash -s -- --yes --force"
ok "token in $CONFIG_ENV"

for _ in $(seq 1 20); do
  curl -fsS "$HEALTH_URL" >/dev/null 2>&1 && { ok "server up at 127.0.0.1:8765"; exit 0; }
  if [[ "$OSTYPE" == darwin* ]]; then
    launchctl kickstart "gui/$(id -u)/com.agent-mail" >/dev/null 2>&1 || true
  else
    systemctl --user start agent-mail.service >/dev/null 2>&1 || true
  fi
  sleep 1
done
die "server not answering at $HEALTH_URL — check the agent-mail service logs"
