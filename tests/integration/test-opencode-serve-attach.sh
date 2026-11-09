#!/usr/bin/env bash
# test-opencode-serve-attach.sh — verify serve+attach multi-client cross-talk
#
# Verifies:
#   1. opencode serve --port starts cleanly
#   2. opencode run --attach <url> --session <ses_id> works
#   3. Two simultaneous clients with different ses_ IDs do not cross-talk
#
# Result drives U7 architecture: serve+attach (1-to-N) vs spawn-per-operator.
#
# Run: bash tests/integration/test-opencode-serve-attach.sh
# Cost: ~$0.05 (4 minimal pings)

set -uo pipefail
OS="$(uname -s)"
PORT=9997
SERVE_PID=""

if ! command -v opencode >/dev/null 2>&1; then echo "✗ FAIL: opencode not found"; exit 1; fi
echo "opencode: $(opencode --version 2>/dev/null | head -1)  OS: $OS"

cleanup() {
  [ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null || true
  lsof -ti tcp:$PORT 2>/dev/null | xargs kill -9 2>/dev/null || true
  rm -f /tmp/oc-serve-$$.log /tmp/oc-rb-$$.txt
}
trap cleanup EXIT

echo "── Step 1: opencode serve --port $PORT ──"
opencode serve --port $PORT >/tmp/oc-serve-$$.log 2>&1 &
SERVE_PID=$!
READY=0
for i in $(seq 1 20); do
  sleep 0.5
  nc -z localhost $PORT 2>/dev/null && { READY=1; echo "✓ ready after ${i}×0.5s"; break; }
done
if [ "$READY" -eq 0 ]; then echo "✗ FAIL: serve not ready"; cat /tmp/oc-serve-$$.log; exit 1; fi

echo "── Step 2: single client via opencode run --attach ──"
R1=$(openssl rand -base64 12 2>/dev/null | tr -dc 'a-zA-Z0-9' | head -c14 || echo "A12345678")
SID_A="ses_srvA${R1}"
RA=$(opencode run --attach "http://localhost:$PORT" --session "$SID_A" --format json \
  "Say only: CLIENT_A_OK" 2>&1 || true)
echo "$RA" | head -5
SINGLE="fail"; echo "$RA" | grep -q "CLIENT_A_OK" && SINGLE="pass"
[ "$SINGLE" = "fail" ] && { echo "✗ FAIL: single-client attach broken"; exit 1; }
echo "✓ single client: pass"

echo "── Step 3: two simultaneous clients (cross-talk test) ──"
R2=$(openssl rand -base64 12 2>/dev/null | tr -dc 'a-zA-Z0-9' | head -c14 || echo "B12345678")
SID_B="ses_srvB${R2}"
opencode run --attach "http://localhost:$PORT" --session "$SID_B" --format json \
  "Say only: CLIENT_B_OK" >/tmp/oc-rb-$$.txt 2>&1 &
PID_B=$!
RA2=$(opencode run --attach "http://localhost:$PORT" --session "$SID_A" --format json \
  "Say only: CLIENT_A_SECOND" 2>&1 || true)
wait "$PID_B" 2>/dev/null || true
RB=$(cat /tmp/oc-rb-$$.txt 2>/dev/null || echo "")
echo "→ A: $(echo "$RA2" | head -2)"
echo "→ B: $(echo "$RB"  | head -2)"

A_OK="no"; B_OK="no"; CROSSTALK="none"
echo "$RA2" | grep -q "CLIENT_A_SECOND" && A_OK="yes"
echo "$RB"  | grep -q "CLIENT_B_OK"     && B_OK="yes"
{ echo "$RA2" | grep -q "CLIENT_B" || echo "$RB" | grep -q "CLIENT_A_SECOND"; } && CROSSTALK="detected"

MULTI="fail"; [ "$A_OK" = "yes" ] && [ "$B_OK" = "yes" ] && [ "$CROSSTALK" = "none" ] && MULTI="pass"
VIABLE="no"; [ "$SINGLE" = "pass" ] && [ "$MULTI" = "pass" ] && VIABLE="yes"

echo ""
echo "════════════════════════════════════════════"
echo "opencode serve+attach contract (U7 decision)"
echo "  single client: $SINGLE  multi-client: $MULTI  cross-talk: $CROSSTALK"
echo "  serve viable:  $VIABLE"
[ "$VIABLE" = "yes" ] && echo "U7: use opencode serve + opencode run --attach" || \
  echo "U7: spawn-per-operator (serve+attach failed)"
echo "════════════════════════════════════════════"
[ "$VIABLE" = "yes" ] || [ "$SINGLE" = "pass" ]
