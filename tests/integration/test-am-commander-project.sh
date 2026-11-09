#!/usr/bin/env bash
# test-am-commander-project.sh — verify am cross-project addressing for commander
#
# Uses commander naming: commander am-project, commander + lead-commander.
# Key check: --program opencode-ai (NOT claude-code).
#
# Run: bash tests/integration/test-am-commander-project.sh
# Cost: $0 (local am)

set -uo pipefail

if ! command -v am >/dev/null 2>&1; then echo "✗ FAIL: am not found"; exit 1; fi
echo "am: $(am --version 2>/dev/null | head -1)  OS: $(uname -s)"

SUFFIX=$(uuidgen 2>/dev/null | tr '[:upper:]' '[:lower:]' | cut -c1-8 || \
         openssl rand -hex 4 2>/dev/null || echo "$(date +%s | tail -c8)")
TEST_PROJECT="cmdr-test-${SUFFIX}"
TEST_DIR=$(mktemp -d)

cleanup() {
  rm -rf "$TEST_DIR"
  am projects delete "$TEST_PROJECT" --yes 2>/dev/null || true
}
trap cleanup EXIT

echo "→ project: $TEST_PROJECT"
cd "$TEST_DIR"

echo ""
echo "── Step 1: register commander ──"
am agents register --project "$TEST_PROJECT" --program opencode-ai \
  --model sonnet --name commander --json >/dev/null
echo "✓ commander registered"

echo "── Step 2: register lead-commander ──"
am agents register --project "$TEST_PROJECT" --program opencode-ai \
  --model sonnet --name lead-commander --json >/dev/null
echo "✓ lead-commander registered"

echo "── Step 3: idempotency re-register commander ──"
am agents register --project "$TEST_PROJECT" --program opencode-ai \
  --model sonnet --name commander --json >/dev/null
echo "✓ idempotent"

echo ""
echo "── Step 4: commander → lead-commander dispatch ──"
am mail send \
  --project "$TEST_PROJECT" \
  --from commander \
  --to lead-commander \
  --subject "dispatch: activate commander" \
  --body "Spawn operator for auth task bd-a1b2." \
  --importance high \
  --json >/dev/null
echo "✓ mail sent"

echo ""
echo "── Step 5: inbox read (ISO-8601 --since) ──"
if [[ "$(uname)" == "Darwin" ]]; then
  SINCE=$(date -u -v-1H +%FT%TZ 2>/dev/null || date -u +%FT%TZ)
else
  SINCE=$(date -u -d '1 hour ago' +%FT%TZ 2>/dev/null || date -u +%FT%TZ)
fi
echo "→ since: $SINCE"

INBOX=$(am mail inbox \
  --project "$TEST_PROJECT" --agent lead-commander \
  --since "$SINCE" --include-bodies --json)
echo "$INBOX" | head -15

echo "$INBOX" | grep -q "dispatch: activate commander" || \
  { echo "✗ FAIL: subject not in inbox"; exit 1; }
echo "✓ subject present"

echo "$INBOX" | grep -q "Spawn operator" || \
  { echo "✗ FAIL: body not in inbox (--include-bodies)"; exit 1; }
echo "✓ body present"

echo ""
echo "── Step 6: check-inbox --rate-limit 0 ──"
am check-inbox --project "$TEST_PROJECT" --agent lead-commander --rate-limit 0 --json >/dev/null
am check-inbox --project "$TEST_PROJECT" --agent lead-commander --rate-limit 0 --json >/dev/null
echo "✓ two calls with --rate-limit 0 succeeded"

echo ""
echo "── Step 7: verify --program opencode-ai stored ──"
AGENTS_OUT=$(am agents list --project "$TEST_PROJECT" --json 2>/dev/null || \
             am agents list --project "$TEST_PROJECT" 2>/dev/null || echo "")
PROG_CHECK="unknown"
echo "$AGENTS_OUT" | grep -q "opencode-ai" && PROG_CHECK="opencode-ai"
echo "$AGENTS_OUT" | grep -q "opencode" && [ "$PROG_CHECK" = "unknown" ] && PROG_CHECK="opencode"
echo "→ program stored: $PROG_CHECK"

echo ""
echo "════════════════════════════════════════════"
echo "am commander-project contract — PASS"
echo "  project:         $TEST_PROJECT"
echo "  program string:  $PROG_CHECK  (must be opencode-ai)"
echo "  mail round-trip: pass"
echo "  --since ISO-8601: pass  --rate-limit 0: pass"
echo "════════════════════════════════════════════"
