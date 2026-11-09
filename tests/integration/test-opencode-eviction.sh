#!/usr/bin/env bash
# test-opencode-eviction.sh — observational test for session eviction behavior
#
# Documents (does NOT assert pass/fail):
#   1. Establish session with secret context
#   2. Delete session storage from filesystem
#   3. Reuse same ID — does opencode error / create fresh / recover from server?
#
# Result drives U7 fallback chain design.
#
# Run: bash tests/integration/test-opencode-eviction.sh
# Cost: ~$0.03 (three minimal pings)

set -uo pipefail

if ! command -v opencode >/dev/null 2>&1; then echo "✗ FAIL: opencode not found"; exit 1; fi
if ! command -v jq       >/dev/null 2>&1; then echo "✗ FAIL: jq not found"; exit 1; fi
echo "opencode: $(opencode --version 2>/dev/null | head -1)  OS: $(uname -s)"

RAND=$(openssl rand -base64 16 2>/dev/null | tr -dc 'a-zA-Z0-9' | head -c22 || echo "AAAAAAAAAAAAAAAAAAAAAA")
SID="ses_evict${RAND}"
echo "→ session: $SID"

echo ""
echo "── Step 1: establish session ──"
R1=$(opencode run --session "$SID" --format json \
  "Remember secret: GREEN_OWL. Reply: noted." 2>&1 || true)
echo "$R1" | grep '^{' | jq -r 'select(.type=="text") | .part.text // empty' 2>/dev/null | head -3

echo ""
echo "── Step 2: locate + delete session files ──"
SESSION_FILES=$(find "$HOME" -path "*/opencode/sessions/*" -name "*${RAND}*" 2>/dev/null)
if [ -z "$SESSION_FILES" ]; then
  SESSION_FILES=$(find "$HOME" -path "*/opencode/*" -name "*${RAND}*" 2>/dev/null | head -5)
fi
echo "→ session files found:"
echo "$SESSION_FILES" | head -5
echo ""
echo "$SESSION_FILES" | xargs rm -f 2>/dev/null || true
echo "→ deleted"

echo ""
echo "── Step 3: reuse same SID after eviction ──"
R2=$(opencode run --session "$SID" --format json \
  "What was the secret? Reply with just the word." 2>&1 || true)
TEXT2=$(echo "$R2" | grep '^{' | jq -r 'select(.type=="text") | .part.text // empty' 2>/dev/null | tr -d '\n')
ERR2=$(echo "$R2"  | grep '^{' | jq 'select(.type=="error")' 2>/dev/null | head -1)
echo "→ response text: $TEXT2"
echo "→ error event:   $(echo "$ERR2" | head -1)"

echo ""
echo "── Determination ──"
BEHAVIOR="unknown"
if echo "$TEXT2" | grep -qi "GREEN_OWL"; then
  BEHAVIOR="cached-elsewhere — context survived FS deletion (cloud/server cache)"
elif [ -n "$ERR2" ]; then
  BEHAVIOR="error-on-evicted — opencode errors when session FS files missing"
elif [ -n "$TEXT2" ]; then
  BEHAVIOR="fresh-session-same-id — silent fresh context, no error"
fi
echo "→ observed: $BEHAVIOR"

echo ""
echo "════════════════════════════════════════════"
echo "opencode eviction semantics (observational)"
echo "  behavior: $BEHAVIOR"
echo ""
echo "U7 fallback chain implication:"
case "$BEHAVIOR" in
  cached-elsewhere*) echo "  → No fallback needed; opencode auto-recovers." ;;
  error-on-evicted*) echo "  → U7 must catch error, create new ses_ ID, redispatch via bd." ;;
  fresh-session-same-id*) echo "  → Silent context loss is the failure mode. U7 must verify session content via probe message before trusting it." ;;
  *) echo "  → Inconclusive; document behavior + retry." ;;
esac
echo "════════════════════════════════════════════"
