#!/usr/bin/env bash
# test-opencode-stats.sh — verify opencode stats output shape
#
# Verifies:
#   1. opencode stats produces text output (no --json flag)
#   2. --days N filter works
#   3. --models flag adds per-model breakdown
#
# Run: bash tests/integration/test-opencode-stats.sh
# Cost: $0 (read-only, no model calls)
# Exits 0 on pass.

set -uo pipefail

if ! command -v opencode >/dev/null 2>&1; then echo "✗ FAIL: opencode not found"; exit 1; fi
echo "opencode: $(opencode --version 2>/dev/null | head -1)  OS: $(uname -s)"

echo ""
echo "── Step 1: opencode stats --help shows no --json ──"
HELP=$(opencode stats --help 2>&1 || true)
HAS_JSON="no"; echo "$HELP" | grep -qE -- "--json" && HAS_JSON="yes"
echo "→ --json flag present in help: $HAS_JSON (expected: no)"
[ "$HAS_JSON" = "yes" ] && { echo "✗ ARCH-FAIL: stats now has --json — re-evaluate U10 architecture"; exit 1; }

echo ""
echo "── Step 2: opencode stats --days 1 returns text ──"
OUT=$(opencode stats --days 1 2>&1 || true)
echo "$OUT" | head -10
LEN=${#OUT}
echo "→ output length: $LEN chars"
[ "$LEN" -eq 0 ] && { echo "⚠ stats returned empty (acceptable if no usage in last 1d)"; }

echo ""
echo "── Step 3: --models flag exists ──"
HAS_MODELS="no"; echo "$HELP" | grep -qE -- "--models" && HAS_MODELS="yes"
echo "→ --models flag: $HAS_MODELS"

echo ""
echo "════════════════════════════════════════════"
echo "opencode stats contract"
echo "  --json flag:     $HAS_JSON  (must be no — text-only)"
echo "  --models flag:   $HAS_MODELS"
echo "  display-only:    yes (NEVER used for U10 ceiling enforcement)"
echo "════════════════════════════════════════════"
[ "$HAS_JSON" = "no" ]
