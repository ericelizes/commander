#!/usr/bin/env bash
# app-install.sh builds the Commander desktop app and installs it, by
# running `cmdr app install` from a freshly built cmdr (so it works before
# `just install`). All flags pass through:
#
#   scripts/app-install.sh                 # npm ci && npm run dist, then install
#   scripts/app-install.sh --skip-build    # install the last build in app/release
#   scripts/app-install.sh --from <path>   # install a given .app / .AppImage
#
# macOS: /Applications/Commander.app (previous one kept in
# ~/.commander/app-backup). Linux: ~/Applications/Commander.AppImage,
# ~/.local/bin/commander-app and a .desktop entry.
# CMDR_APP_INSTALL_DIR / COMMANDER_HOME redirect both (used for dry runs).
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PATH="$HOME/.local/go/bin:$PATH"
export COMMANDER_REPO="$REPO"

BIN_DIR="$(mktemp -d)"
trap 'rm -rf "$BIN_DIR"' EXIT
(cd "$REPO" && go build -o "$BIN_DIR/cmdr" ./cmd/cmdr)

exec "$BIN_DIR/cmdr" app install "$@"
