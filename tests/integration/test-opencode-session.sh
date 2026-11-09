#!/usr/bin/env bash
# test-opencode-session.sh — verify opencode session resume contract
#
# Verifies:
#   1. ses_-prefixed --session value is treated as ID lookup
#   2. Second invocation with same session ID recalls prior context
#   3. nd-JSON stream emits step_finish events
#
# Run: bash tests/integration/test-opencode-session.sh
# Cost: ~$0.02 (two minimal pings)
# Exits 0 on pass.

set -uo pipefail

if ! command -v opencode >/dev/null 2>&1; then echo "✗ FAIL: opencode not found"; exit 1; fi
if ! command -v jq       >/dev/null 2>&1; then echo "✗ FAIL: jq not found"; exit 1; fi
echo "opencode: $(opencode --version 2>/dev/null | head -1)  OS: $(uname -s)"

RAND=$(openssl rand -base64 16 2>/dev/null | tr -dc 'a-zA-Z0-9' | head -c22 || echo "AAAAAAAAAAAAAAAAAAAAAA")
SID="ses_${RAND}"
echo "→ session: $SID"

cleanup() {
  find "$HOME" -path "*/opencode/sessions/*" -name "*${RAND}*" -type f 2>/dev/null | xargs rm -f 2>/dev/null || true
}
trap cleanup EXIT

echo ""
echo "── Step 1: first invocation establishes context ──"
R1=$(opencode run --session "$SID" --format json \
  "Remember the secret word: PURPLE_FOX. Reply with: noted." 2>&1 || true)
JSON_LINES=$(echo "$R1" | grep -c '^{' 2>/dev/null || echo 0)
echo "→ json lines: $JSON_LINES"
[ "$JSON_LINES" -eq 0 ] && { echo "✗ FAIL: no JSON output from first run"; echo "$R1" | head -10; exit 1; }
echo "✓ first run produced nd-JSON"

echo ""
echo "── Step 2: second invocation must recall context ──"
R2=$(opencode run --session "$SID" --format json \
  "What was the secret word I told you? Reply with just the word." 2>&1 || true)
TEXT2=$(echo "$R2" | grep '^{' | jq -r 'select(.type=="text") | .part.text // empty' 2>/dev/null | tr -d '\n')
echo "→ recall response: $TEXT2"

RECALL="fail"
echo "$TEXT2" | grep -qi "PURPLE_FOX" && RECALL="pass"
echo "→ context recalled: $RECALL"

echo ""
echo "── Step 3: step_finish event present ──"
SF=$(echo "$R2" | grep '^{' | jq 'select(.type=="step_finish")' 2>/dev/null | head -1)
SF_OK="no"; [ -n "$SF" ] && SF_OK="yes"
COST=$(echo "$SF" | jq -r '.part.cost // 0' 2>/dev/null)
TOKENS_IN=$(echo "$SF" | jq -r '.part.tokens.input // 0' 2>/dev/null)
echo "→ step_finish present: $SF_OK  cost=\$$COST  input_tokens=$TOKENS_IN"

echo ""
echo "════════════════════════════════════════════"
echo "opencode session contract"
echo "  ses_-prefixed ID:  used as ID lookup ($SID)"
echo "  context recall:    $RECALL"
echo "  nd-JSON stream:    $JSON_LINES lines on second invocation"
echo "  step_finish event: $SF_OK"
echo "════════════════════════════════════════════"
[ "$RECALL" = "pass" ] && [ "$SF_OK" = "yes" ]
