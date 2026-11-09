#!/usr/bin/env bash
# Build cmdr and cmdrd into bin/staged/ (plus VERSION = git revision). Writes
# nothing else: installed binaries and ~/.local/bin links are untouched until
# the user runs `cmdr install` (`just install`).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STAGE="${CMDR_STAGE_DIR:-$REPO_DIR/bin/staged}"
mkdir -p "$STAGE"

cd "$REPO_DIR"
go build -o "$STAGE/cmdr" ./cmd/cmdr
go build -o "$STAGE/cmdrd" ./cmd/cmdrd

# macOS privacy grants (TCC) follow a binary's signature; Go's ad-hoc one changes
# every build. A stable identity ("Commander Dev" by default) keeps the grants.
# Signing happens here so the installed copy carries it.
SIGN="${COMMANDER_SIGN_IDENTITY:-Commander Dev}"
if [[ "$(uname)" == Darwin ]] && command -v codesign >/dev/null 2>&1 \
    && security find-identity -p codesigning 2>/dev/null | grep -q "\"$SIGN\""; then
    for bin in cmdr cmdrd; do
        codesign --force --sign "$SIGN" --identifier "commander.$bin" "$STAGE/$bin" 2>/dev/null \
            || echo "could not sign $bin as \"$SIGN\"" >&2
    done
fi

rev="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
if [[ -n "$(git status --porcelain --untracked-files=no 2>/dev/null)" ]]; then rev="$rev-dirty"; fi
echo "$rev" > "$STAGE/VERSION"
echo "staged $rev in $STAGE (install with: just install)"
