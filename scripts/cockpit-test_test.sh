#!/usr/bin/env bash
# Tests for scripts/cockpit-test.sh safety: it refuses real state and starts nothing real.
set -uo pipefail
unset TMUX
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d /tmp/ckt.XXXX) || { echo "FAIL: mktemp"; exit 1; }
[ -d "$tmp" ] || { echo "FAIL: no temp dir"; exit 1; }
tmp=$(cd "$tmp" && pwd -P)
export COCKPIT_TMUX_TMPDIR="$tmp/t"
# A FAKE tmux (file state next to the -S socket) stands in for the real one: no
# real tmux server is started, and nothing here can reach another socket.
mkdir "$tmp/fakebin"
cat > "$tmp/fakebin/tmux" <<'EOT'
#!/bin/bash
PATH=/usr/bin:/bin   # the isolated cockpit PATH has no grep/mv/rm
[ "${1:-}" = -S ] || { echo "fake tmux: bare call without -S: $*" >&2; exit 97; }
[ -z "${TMUX:-}" ] || { echo "fake tmux: TMUX set" >&2; exit 98; }
sock=$2; shift 2
echo "$sock $*" >> "$sock.log"
sub=${1:-}; shift || true
case "$sub" in
  new-session) : > "$sock"; sess=; while [ $# -gt 0 ]; do case $1 in -s) sess=$2; shift;; -d) ;; *) cmd=$1;; esac; shift; done
    echo "$sess" >> "$sock.sessions"; echo "$cmd" > "$sock.pane"
    echo "COCKPIT_SERVER_TOKEN=${COCKPIT_SERVER_TOKEN:-}" > "$sock.env" ;;
  has-session) [ -e "$sock" ] && grep -qx "${2:-}" "$sock.sessions" ;;
  kill-session) [ -e "$sock" ] || exit 1; grep -vx "$2" "$sock.sessions" > "$sock.s2"; mv "$sock.s2" "$sock.sessions"
    [ -s "$sock.sessions" ] || rm -f "$sock" ;;
  show-environment) [ -e "$sock" ] && [ ! -e "$sock.deaf" ] || exit 1
    case "${2:-}" in COCKPIT_SERVER_TOKEN) cat "$sock.env" 2>/dev/null;; esac ;;
  set-environment) [ "$1" = -gu ] && : > "$sock.env"; [ "$1" = -g ] && echo "$2=$3" > "$sock.env" ;;
  list-panes) echo "%0 bash 220x50" ;;
  display) echo "bash 220x50" ;;
  capture-pane) cat "$sock.pane" ;;
  *) ;;
esac
EOT
chmod +x "$tmp/fakebin/tmux"
export PATH="$tmp/fakebin:$PATH"
# Every tmux call of this test goes through tm, on a socket inside a dir the test made.
tm() { local d=$1; shift; env -u TMUX tmux -S "$d/cockpit.sock" "$@"; }
trap 'tm "$COCKPIT_TMUX_TMPDIR" kill-session -t commander >/dev/null 2>&1; rm -rf "$tmp"' EXIT
fail=0
check() { if [ "$1" != "$2" ]; then echo "FAIL: $3: want '$1' got '$2'"; fail=1; fi; }
cat > "$tmp/cmdr" <<'EOS'
#!/bin/sh
{ echo "CH=$COMMANDER_HOME"; echo "HOME=$HOME"; echo "RT=$COMMANDER_RUNTIME"; echo "ROLE=${COMMANDER_ROLE:-}";
  echo "CLAUDE=$(command -v claude)"; echo "OPENCODE=$(command -v opencode)"; echo "CODEX=$(command -v codex)";
  echo "OVERRIDE=${COMMANDER_CLAUDE_BIN:-}${CLAUDE_CONFIG_DIR:-}${CODEX_HOME:-}${ABS_AGENT:-}"; } > "$HOME/rec"
tmux new-session -d -s commander 'echo isolated-cockpit-sentinel; sleep 120'
EOS
chmod +x "$tmp/cmdr"
printf '#!/bin/sh\nexit 0\n' > "$tmp/fakeoc"; chmod +x "$tmp/fakeoc"
export COCKPIT_CMDR="$tmp/cmdr" COCKPIT_FAKE_OPENCODE="$tmp/fakeoc"
iso="${COCKPIT_TMUX_TMPDIR}.home"
REC="$iso/home/rec"

# 1. refuses the real commander home, a dir holding it, the caller's home, a foreign dir
realch="$tmp/realhome/.commander"; mkdir -p "$realch"
out=$(HOME="$tmp/realhome" COCKPIT_HOME="$realch" bash "$here/cockpit-test.sh" up 2>&1); check 3 $? "real home exit"
case "$out" in *refusing*) ;; *) echo "FAIL: no refusal text: $out"; fail=1;; esac
out=$(HOME="$tmp/realhome" COCKPIT_HOME="$tmp/realhome" bash "$here/cockpit-test.sh" up 2>&1); check 3 $? "contains real home"
mkdir -p "$tmp/other"; echo x > "$tmp/other/f"
out=$(COCKPIT_HOME="$tmp/other" bash "$here/cockpit-test.sh" up 2>&1); check 3 $? "foreign dir"
callerch="$tmp/caller"; mkdir -p "$callerch"
out=$(COMMANDER_HOME="$callerch" COCKPIT_HOME="$callerch" bash "$here/cockpit-test.sh" up 2>&1); check 3 $? "caller home"
[ -e "$REC" ] && { echo "FAIL: cmdr ran after a refusal"; fail=1; }

# 2. nothing real starts: isolated env even with a hostile caller environment
out=$(COMMANDER_HOME="$callerch" COMMANDER_ROLE=manager-x COMMANDER_RUNTIME=claude COMMANDER_CLAUDE_BIN=/absolute/claude CLAUDE_CONFIG_DIR=/real CODEX_HOME=/real ABS_AGENT=/absolute/codex HOME="$tmp/realhome" bash "$here/cockpit-test.sh" up 2>&1); check 0 $? "isolated up ($out)"
check "CH=$iso/ch" "$(sed -n 1p "$REC")" "commander home"
check "HOME=$iso/home" "$(sed -n 2p "$REC")" "home"
check "RT=opencode" "$(sed -n 3p "$REC")" "runtime"
check "ROLE=" "$(sed -n 4p "$REC")" "role dropped"
check "CLAUDE=$iso/bin/claude" "$(sed -n 5p "$REC")" "claude is the stub"
check "OPENCODE=$iso/bin/opencode" "$(sed -n 6p "$REC")" "opencode is the fake"
check "CODEX=$iso/bin/codex" "$(sed -n 7p "$REC")" "codex is the stub"
check "OVERRIDE=" "$(sed -n 8p "$REC")" "absolute agent overrides dropped"
"$iso/bin/claude" 2>/dev/null; check 1 $? "claude stub fails"
check 0 "$(find "$callerch" "$tmp/realhome/.commander" -type f | wc -l | awk '{print $1}')" "real dirs untouched"

# A second consecutive setup must replace tools/tmux.real even when it cannot be
# overwritten in place (a running server makes it busy; read-only stands in for it).
chmod 555 "$iso/tools/tmux.real"
out=$(COMMANDER_HOME="$callerch" HOME="$tmp/realhome" bash "$here/cockpit-test.sh" up 2>&1); check 0 $? "second up ($out)"

# Every subcommand validates descendants before writing or contacting a server.
for sub in up text keys down; do
  attack="$tmp/attack-$sub"; mkdir -p "$attack/bin"; touch "$attack/.cockpit-isolated"
  ln -s "$realch" "$attack/ch"
  out=$(COCKPIT_HOME="$attack" bash "$here/cockpit-test.sh" "$sub" 2>&1); check 3 $? "linked ch $sub"
done
attack="$tmp/attack-bin"; mkdir -p "$attack/bin"; touch "$attack/.cockpit-isolated"
printf 'untouched\n' > "$tmp/target"
ln -s "$tmp/target" "$attack/bin/claude"
out=$(COCKPIT_HOME="$attack" bash "$here/cockpit-test.sh" text 2>&1); check 3 $? "linked stub"
check untouched "$(cat "$tmp/target")" "external target unchanged"
out=$(COCKPIT_HOME="$tmp" COMMANDER_HOME="$realch" bash "$here/cockpit-test.sh" text 2>&1); check 3 $? "marked ancestor of protected dir"

# Cached fake binaries are never trusted, including on read-only subcommands.
printf '#!/bin/sh\necho cached-agent-must-not-run\n' > "$iso/bin/opencode"
out=$(bash "$here/cockpit-test.sh" text 2>&1); check 0 $? "live isolated text ($out)"
case "$out" in *isolated-cockpit-sentinel*) echo 'live cockpit text: isolated-cockpit-sentinel';; *) echo 'FAIL: missing live pane text'; fail=1;; esac
cmp -s "$tmp/fakeoc" "$iso/bin/opencode"; check 0 $? "fake recreated"

# Own a dummy foreign server solely for this regression. No foreign sessions
# are sent keys or terminated by cockpit-test.sh; cleanup is our responsibility.
foreign="$tmp/foreign"; mkdir -m 700 "$foreign"
tm "$foreign" new-session -d -s sentinel 'sleep 120'
for sub in up text keys down; do
  out=$(COCKPIT_TMUX_TMPDIR="$foreign" COCKPIT_HOME="$iso" bash "$here/cockpit-test.sh" "$sub" 2>&1); check 3 $? "foreign socket $sub"
done
tm "$foreign" has-session -t sentinel; check 0 $? "foreign session survived"
tm "$foreign" kill-session -t sentinel

# The removed shot subcommand is a usage error, not a way to reach a server.
out=$(bash "$here/cockpit-test.sh" shot 2>&1); check 2 $? "shot removed"
# Every tmux call used the -S socket (the fake refuses bare calls with 97).
grep -q . "$COCKPIT_TMUX_TMPDIR/cockpit.sock.log" || { echo "FAIL: no tmux calls logged"; fail=1; }

# Even a marked socket directory cannot adopt a server missing its token.
tm "$COCKPIT_TMUX_TMPDIR" set-environment -gu COCKPIT_SERVER_TOKEN
out=$(bash "$here/cockpit-test.sh" down 2>&1); check 3 $? "foreign server token"
tm "$COCKPIT_TMUX_TMPDIR" set-environment -g COCKPIT_SERVER_TOKEN "$(cat "$iso/.server-token")"
# A server that cannot be read is refused, not adopted.
: > "$COCKPIT_TMUX_TMPDIR/cockpit.sock.deaf"
out=$(bash "$here/cockpit-test.sh" text 2>&1); check 3 $? "unreadable server"
rm -f "$COCKPIT_TMUX_TMPDIR/cockpit.sock.deaf"
[ $fail = 0 ] && echo "cockpit-test_test: ok"
exit $fail
