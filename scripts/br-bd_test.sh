#!/usr/bin/env bash
# Tests for scripts/br-bd: a fake bd (and fake real br) record the argv they get.
set -euo pipefail
unset COMMANDER_ROLE # this script may itself run under an Operator; runas sets it
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d /tmp/brbd.XXXX)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/proj/.beads/dolt" "$tmp/old/.beads" "$tmp/elsewhere"
printf '#!/bin/sh\necho "bd $@"\n' > "$tmp/bin/bd"
printf '#!/bin/sh\necho "br $@"\n' > "$tmp/bin/realbr"
chmod +x "$tmp/bin/bd" "$tmp/bin/realbr"
proj=$(cd "$tmp/proj" && pwd -P)
old=$(cd "$tmp/old" && pwd -P)
cd "$tmp/elsewhere"
run() { PATH="$tmp/bin:$PATH" COMMANDER_REAL_BR="$tmp/bin/realbr" bash "$here/br-bd" "$@"; }
unset BEADS_DIR BEADS_DB
fail=0
check() { # want, got
	if [ "$1" != "$2" ]; then echo "FAIL: want '$1' got '$2'"; fail=1; fi
}
# deps direction (cwd = a dolt project)
cd "$proj"
check "bd -C $proj create T --deps blocked-by:x-1,discovered-from:x-2" "$(run create T --deps blocks:x-1,discovered-from:x-2)"
check "bd -C $proj create T --deps=blocked-by:x-1 --labels a" "$(run create T --deps=blocks:x-1 --labels a)"
check "bd -C $proj create T --deps blocked-by:x-1" "$(run create T --deps blocked-by:x-1)"
check "bd -C $proj create T --labels blocks:z" "$(run create T --labels blocks:z)"
# routing from another cwd
cd "$tmp/elsewhere"
check "bd -C $proj show x-1" "$(run --db "$proj/.beads/beads.db" show x-1)"
check "bd -C $proj show x-1" "$(run --db="$proj/.beads/beads.db" show x-1)"
check "bd -C $proj show x-1" "$(run -C "$proj" show x-1)"
check "bd -C $proj show x-1" "$(run --directory "$proj" show x-1)"
check "bd -C $proj show x-1" "$(BEADS_DIR="$proj/.beads" run show x-1)"
check "bd -C $proj show x-1" "$(BEADS_DB="$proj/.beads/beads.db" run show x-1)"
check "bd -C $proj create T --deps blocked-by:a" "$(run --db "$proj/.beads/beads.db" create T --deps blocks:a)"
# a project without .beads/dolt, or no project at all, goes to the real br
check "br --db $old/.beads/beads.db show x-1" "$(run --db "$old/.beads/beads.db" show x-1)"
check "br -C $old show x-1" "$(run -C "$old" show x-1)"
check "br show x-1" "$(run show x-1)"
# agent write guard: an agent role cannot write with br; reads, the user, cmdr's own children and the switch pass
cd "$proj"
export COMMANDER_HOME="$tmp/chome"
runas() { local role=$1; shift; COMMANDER_ROLE=$role run "$@" 2>/dev/null || echo "REFUSED($?)"; }
for role in operator-x manager-y; do
	for w in "create T" "update x-1 --title t" "close x-1" "comments add x-1 hi" "--db $proj/.beads/beads.db comments add x-1 hi" "label add x-1 a" "dep add a b" "dep rm a b" "defer x-1" "set-state x-1 a=b" "sync"; do
		# shellcheck disable=SC2086
		check "REFUSED(2)" "$(runas $role $w)"
	done
	for r in "show x-1" "list --json" "comments x-1" "dep list x-1" "label list" "sync --flush-only"; do
		# shellcheck disable=SC2086
		case "$(runas $role $r)" in REFUSED*) echo "FAIL: read refused for $role: $r"; fail=1 ;; esac
	done
done
check "bd -C $proj close x-1 --force" "$(run close x-1)"
check "bd -C $proj close x-1 --force" "$(COMMANDER_ROLE=commander run close x-1)"
check "bd -C $proj close x-1 --force" "$(COMMANDER_ROLE=operator-x COMMANDER_BR_HELD=1 run close x-1)"
check "bd -C $proj close x-1 --force" "$(COMMANDER_ROLE=operator-x COMMANDER_AGENT_WRITE_GUARD=off run close x-1)"
grep -q '"verb":"close"' "$tmp/chome/logs/agent-write-refusals.jsonl" || { echo "FAIL: refusal not logged"; fail=1; }
[ "$fail" = 0 ] && echo ok
exit "$fail"
