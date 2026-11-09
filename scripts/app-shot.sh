#!/usr/bin/env bash
# app-shot.sh: a PNG of the real Commander desktop window, exactly as the user
# sees it (macOS). Captures only that window, without raising or focusing it.
#   scripts/app-shot.sh [out.png]     default: ~/.commander/shots/app.png
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
out="${1:-$HOME/.commander/shots/app.png}"
mkdir -p "$(dirname "$out")"
id="$(swift "$here/app-window-id.swift" Commander)"
if [ -z "$id" ]; then
  echo "app-shot: the Commander window is not on screen" >&2
  exit 1
fi
screencapture -x -o -l "$id" "$out"
echo "$out"
