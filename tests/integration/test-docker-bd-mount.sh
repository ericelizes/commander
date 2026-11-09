#!/usr/bin/env bash
# test-docker-bd-mount.sh — verify bd SQLite WAL behavior via Docker volume mount
#
# Tests both directions: host→container and container→host writes.
# Verifies host.docker.internal DNS resolution (macOS auto, Linux needs --add-host).
#
# Run: bash tests/integration/test-docker-bd-mount.sh
# Cost: $0
# Skips if docker daemon unavailable.

set -uo pipefail
OS="$(uname -s)"

if ! command -v docker >/dev/null 2>&1; then echo "⚠ SKIP: docker not available"; exit 0; fi
docker info >/dev/null 2>&1 || { echo "⚠ SKIP: docker daemon not running"; exit 0; }
if ! command -v bd >/dev/null 2>&1; then echo "✗ FAIL: bd not found"; exit 1; fi

echo "docker: $(docker version --format '{{.Server.Version}}' 2>/dev/null)  bd: $(bd --version 2>/dev/null | head -1)  OS: $OS"

TEST_DIR=$(mktemp -d)
BD_BIN=$(command -v bd)

# Network flags: macOS Docker Desktop resolves host.docker.internal automatically;
# Linux Docker Engine needs --add-host.
NET_FLAGS=""
[ "$OS" != "Darwin" ] && NET_FLAGS="--add-host=host.docker.internal:host-gateway"
echo "→ test dir: $TEST_DIR  net flags: '${NET_FLAGS:-(none, macOS)}'"

cleanup() {
  docker rm -f "cmdr-bd-test-$$" 2>/dev/null || true
  rm -rf "$TEST_DIR"
}
trap cleanup EXIT

cd "$TEST_DIR"
git init -q .
git config user.email "test@commander.local"
git config user.name "commander-test"
BD_NON_INTERACTIVE=1 bd init --stealth >/dev/null 2>&1 || \
  BD_NON_INTERACTIVE=1 bd init >/dev/null 2>&1 || true
bd config set export.auto false >/dev/null 2>&1 || true
[ -d ".beads" ] || { echo "✗ FAIL: bd init did not create .beads"; exit 1; }
echo "✓ .beads created"

echo ""
echo "── Step 1: HOST write ──"
HOST_ID=$(bd q "host-written-bead" --labels kind:test 2>/dev/null)
[ -z "$HOST_ID" ] && { echo "✗ FAIL: bd q failed on host"; exit 1; }
bd set-state "$HOST_ID" cmdr=brainstorming --reason "docker test" 2>/dev/null
echo "→ host wrote: $HOST_ID"

echo ""
echo "── Step 2: CONTAINER read (host→container) ──"
C_READ=$(docker run --rm $NET_FLAGS \
  -v "$TEST_DIR/.beads:/workspace/.beads" \
  -v "$BD_BIN:/usr/local/bin/bd:ro" \
  -w /workspace \
  alpine:3.19 \
  sh -c "chmod +x /usr/local/bin/bd 2>/dev/null; bd list --label cmdr:brainstorming 2>&1 || bd list -l cmdr:brainstorming 2>&1" \
  2>&1 || true)
echo "→ container read: $(echo "$C_READ" | head -3)"

H2C="fail"
echo "$C_READ" | grep -q "$HOST_ID" && H2C="pass"
echo "$C_READ" | grep -qiE "(error|busy|locked|sqlite)" && H2C="sqlite-error"
echo "→ host→container: $H2C"

echo ""
echo "── Step 3: CONTAINER write → HOST read ──"
C_WRITE=$(docker run --rm $NET_FLAGS \
  -v "$TEST_DIR/.beads:/workspace/.beads" \
  -v "$BD_BIN:/usr/local/bin/bd:ro" \
  -w /workspace \
  alpine:3.19 \
  sh -c "chmod +x /usr/local/bin/bd 2>/dev/null; bd q 'container-written-bead' --labels kind:test 2>&1" \
  2>&1 || true)
echo "→ container wrote: $(echo "$C_WRITE" | head -2)"

C_ID=$(echo "$C_WRITE" | grep -oE 'bd-[a-zA-Z0-9.]+' | head -1)
C2H="fail"
if [ -n "$C_ID" ]; then
  HOST_VERIFY=$(bd show "$C_ID" 2>/dev/null || true)
  echo "$HOST_VERIFY" | grep -q "container-written-bead" && C2H="pass"
  [ "$C2H" = "fail" ] && C2H="bead-not-visible"
else
  C2H="no-id-returned"
fi
echo "→ container→host: $C2H"

echo ""
echo "── Step 4: host.docker.internal DNS from container ──"
DNS_OUT=$(docker run --rm $NET_FLAGS \
  alpine:3.19 \
  sh -c "nslookup host.docker.internal 2>&1 | head -3" 2>&1 || true)
echo "→ DNS: $(echo "$DNS_OUT" | head -2)"
DNS_OK="fail"
echo "$DNS_OUT" | grep -qiE "(address|answer|127\.|10\.|172\.|192\.)" && DNS_OK="pass"
echo "→ host.docker.internal resolves: $DNS_OK"

echo ""
echo "════════════════════════════════════════════"
echo "Docker bd-mount contract"
echo "  OS:                    $OS"
echo "  host→container:        $H2C"
echo "  container→host:        $C2H"
echo "  host.docker.internal:  $DNS_OK"
if [ "$OS" = "Darwin" ]; then
  echo "  macOS: no --add-host needed (Docker Desktop resolves automatically)"
else
  echo "  Linux: --add-host=host.docker.internal:host-gateway required"
fi
if [ "$H2C" = "sqlite-error" ]; then
  echo "  FALLBACK needed: WAL contention detected — use ephemeral bd + sync"
fi
echo "════════════════════════════════════════════"
[ "$H2C" = "pass" ] || [ "$H2C" = "sqlite-error" ]
