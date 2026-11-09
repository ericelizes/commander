#!/usr/bin/env bash
# make-icons.sh regenerates every app icon from build/icon.svg, the source of
# truth (macOS only: needs the chrome-headless-shell cmdr installs under
# ~/.commander/browsers, sips and iconutil). The outputs are committed, so
# Linux builds never run this.
#
#   app/build/icon.png          1024x1024 render of icon.svg (electron-builder default)
#   app/build/icon.icns         macOS bundle icon
#   app/build/icons/NxN.png     Linux icon set (16..512)
#   docs/screens/app-icon.png   preview for docs
set -euo pipefail

BUILD="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO="$(cd "$BUILD/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Render icon.svg at 1024 on a transparent page (Quick Look would fill the
# corners white).
CHROME="$(find "$HOME/.commander/browsers" -name chrome-headless-shell -type f | head -1)"
printf '<!doctype html><body style="margin:0"><img src="%s" width="1024" height="1024" style="display:block">' "file://$BUILD/icon.svg" >"$TMP/icon.html"
"$CHROME" --headless --disable-gpu --hide-scrollbars --allow-file-access-from-files \
  --default-background-color=00000000 --window-size=1024,1024 --force-device-scale-factor=1 \
  --screenshot="$BUILD/icon.png" "file://$TMP/icon.html" 2>/dev/null

mkdir -p "$BUILD/icons"
for n in 16 32 48 64 128 256 512; do
  sips -z "$n" "$n" "$BUILD/icon.png" --out "$BUILD/icons/${n}x${n}.png" >/dev/null
done

SET="$TMP/icon.iconset"
mkdir -p "$SET"
for n in 16 32 128 256 512; do
  sips -z "$n" "$n" "$BUILD/icon.png" --out "$SET/icon_${n}x${n}.png" >/dev/null
  d=$((n * 2))
  sips -z "$d" "$d" "$BUILD/icon.png" --out "$SET/icon_${n}x${n}@2x.png" >/dev/null
done
iconutil -c icns "$SET" -o "$BUILD/icon.icns"

mkdir -p "$REPO/docs/screens"
cp "$BUILD/icon.png" "$REPO/docs/screens/app-icon.png"
echo "icons written under $BUILD"
