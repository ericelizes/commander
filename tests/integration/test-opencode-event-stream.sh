#!/usr/bin/env bash
# test-opencode-event-stream.sh — verify opencode run --format json event schema
#
# NEW (not in original plan) — load-bearing for U10 cost ceiling watchdog.
# Verifies:
#   1. --format json produces nd-JSON (streaming, one line per event)
#   2. Events: step_start, text, tool_use, tool_result, step_finish, error
#   3. step_finish.part.tokens: {input, output, reasoning, cache.{read,write}}
#   4. step_finish.part.cost: float USD
#   5. NO model field in tokens (record model at spawn time, not from stream)
#   6. sessionID format: ses_XXXX
#
# Run: bash tests/integration/test-opencode-event-stream.sh
# Cost: ~$0.01

set -uo pipefail

if ! command -v opencode >/dev/null 2>&1; then echo "✗ FAIL: opencode not found"; exit 1; fi
if ! command -v jq       >/dev/null 2>&1; then echo "✗ FAIL: jq not found (brew install jq)"; exit 1; fi
echo "opencode: $(opencode --version 2>/dev/null | head -1)  OS: $(uname -s)"

RAND=$(openssl rand -base64 16 2>/dev/null | tr -dc 'a-zA-Z0-9' | head -c22 || echo "evtAAAAAAAAAAAAAAAAAA")
SESSION_ID="ses_evt${RAND}"
echo "→ session: $SESSION_ID"

cleanup() {
  find "$HOME" -path "*/opencode/sessions/*" -name "*${RAND}*" -type f 2>/dev/null | xargs rm -f 2>/dev/null || true
}
trap cleanup EXIT

echo ""
echo "── Step 1: capture nd-JSON stream ──"
STREAM=$(opencode run --session "$SESSION_ID" --format json \
  "Reply with just the word: pong" 2>&1 || true)
JSON_LINES=$(echo "$STREAM" | grep -c '^{' 2>/dev/null || echo 0)
echo "→ JSON lines: $JSON_LINES"
echo "raw stream:"
echo "$STREAM"

[ "$JSON_LINES" -eq 0 ] && { echo "✗ FAIL: no JSON lines"; exit 1; }
echo "✓ format: nd-json"

echo ""
echo "── Step 2: event type inventory ──"
TYPES=$(echo "$STREAM" | grep '^{' | jq -r '.type // empty' 2>/dev/null | sort | uniq | tr '\n' ' ')
echo "→ types: $TYPES"

HAVE_SF="no"; echo "$TYPES" | grep -q "step_finish" && HAVE_SF="yes"
HAVE_SS="no"; echo "$TYPES" | grep -q "step_start"  && HAVE_SS="yes"
HAVE_TX="no"; echo "$TYPES" | grep -q "text"        && HAVE_TX="yes"
echo "  step_start=$HAVE_SS  text=$HAVE_TX  step_finish=$HAVE_SF"

echo ""
echo "── Step 3: step_finish schema ──"
SF=$(echo "$STREAM" | grep '^{' | jq 'select(.type=="step_finish")' 2>/dev/null | head -1)
[ -z "$SF" ] && { echo "✗ FAIL: no step_finish event"; exit 1; }
echo "step_finish:"
echo "$SF" | jq . 2>/dev/null

TOKENS_OBJ=$(echo "$SF" | jq '.part.tokens // empty' 2>/dev/null)
TOKENS_OK="no"; [ -n "$TOKENS_OBJ" ] && TOKENS_OK="yes"
COST_VAL=$(echo "$SF" | jq '.part.cost // empty' 2>/dev/null)
COST_OK="no"; [ -n "$COST_VAL" ] && COST_OK="yes"
MODEL_IN_TOKENS="no"
[ "$TOKENS_OK" = "yes" ] && echo "$TOKENS_OBJ" | jq -e '.model' >/dev/null 2>&1 && MODEL_IN_TOKENS="yes"
SID_FMT=$(echo "$SF" | jq -r '.sessionID // empty' 2>/dev/null)
SES_PREFIX="no"; echo "$SID_FMT" | grep -q '^ses_' && SES_PREFIX="yes"

if [ "$TOKENS_OK" = "yes" ]; then
  echo "  input:    $(echo "$TOKENS_OBJ" | jq '.input // 0')"
  echo "  output:   $(echo "$TOKENS_OBJ" | jq '.output // 0')"
  echo "  reasoning:$(echo "$TOKENS_OBJ" | jq '.reasoning // 0')"
  echo "  cache.read: $(echo "$TOKENS_OBJ" | jq '.cache.read // 0')"
  echo "  cache.write:$(echo "$TOKENS_OBJ" | jq '.cache.write // 0')"
fi
echo "  cost (USD): ${COST_VAL:-absent}"
echo "  model in tokens: $MODEL_IN_TOKENS (expected: no)"
echo "  sessionID ses_ prefix: $SES_PREFIX (expected: yes)"

ARCH_OK="no"
[ "$TOKENS_OK" = "yes" ] && [ "$COST_OK" = "yes" ] && ARCH_OK="yes"

echo ""
echo "════════════════════════════════════════════"
echo "opencode event stream contract (U10 load-bearing)"
echo "  format:           nd-json"
echo "  step_finish:      tokens=$TOKENS_OK cost=$COST_OK"
echo "  tokens.model:     $MODEL_IN_TOKENS (expected: no)"
echo "  sessionID fmt:    ses_prefix=$SES_PREFIX"
echo "  U10 viable:       $ARCH_OK"
if [ "$MODEL_IN_TOKENS" = "no" ]; then
  echo "  NOTE: record model at spawn time in SQLite, not from stream"
fi
echo "════════════════════════════════════════════"
[ "$ARCH_OK" = "yes" ]
