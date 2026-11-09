#!/usr/bin/env bash
# Proves term-inventory.sh --check fails on planted terms and passes on a clean repo.
# Usage: scripts/term-inventory_test.sh
set -euo pipefail

src=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
git init -q .
mkdir scripts
cp "$src/term-inventory.sh" "$src/forbidden-terms.txt" "$src/term-allowlist.txt" scripts/
git config user.email t@example.com
git config user.name t
echo 'package main' >main.go
# Private terms are never in the repo, so test-only ones stand in for them. The tracked list's own
# patterns stay in force, so the path cases below test what CI really runs.
cat >>scripts/forbidden-terms.txt <<'TERMS'
plantedterm
plantedhost.example
plantedorg
cs:PlantedCase
sub:plantedsub
re:planted[_-]regex
TERMS
git add . && git commit -qm init

check() { scripts/term-inventory.sh --check 2>&1; }
fail() { echo "FAIL: $*" >&2; exit 1; }

check >/dev/null || fail "clean repo should pass: $(check)"

expect_hit() { # label, then a command that plants the term
  local label=$1; shift
  "$@"
  git add -A
  if out=$(check); then fail "$label: planted term not caught"; fi
  echo "$out" | grep -q "$label" || fail "$label: offender not listed: $out"
  git rm -q -f --cached -r . >/dev/null; git reset -q --hard
  git clean -ffdq
}

expect_hit 'a.txt' sh -c 'echo "mail plantedterm@example.org" >a.txt'
expect_hit 'b.txt' sh -c 'echo "me@plantedhost.example" >b.txt'
expect_hit 'c.txt' sh -c 'echo "github.com/plantedorg/x" >c.txt'
expect_hit 'd.txt' sh -c 'printf "see /Users/%s/code\n" someone >d.txt'
expect_hit 'plantedterm-notes.md' sh -c 'echo hi >plantedterm-notes.md'

expect_hit 'e.txt' sh -c 'printf "see /home/%s/code\n" someone >e.txt'
# A repository-wide exception on the line must not hide another term beside it.
expect_hit 'f.txt' sh -c 'echo "github.com/example-user/commander plantedhost.example" >f.txt'
expect_hit 'g.txt' sh -c 'echo "github.com/example-user/commander /Users/someone" >g.txt'
expect_hit 'h.bin' sh -c 'printf "\000\000me@plantedhost.example\000" >h.bin'
expect_hit 'i.txt' sh -c 'printf "plantedhost.example\r\n" >i.txt'
expect_hit 'plantedhost.example-notes.md' sh -c 'echo hi >plantedhost.example-notes.md'
expect_hit 'j.txt' sh -c 'echo "PlantedCase" >j.txt'

# Regressions: far-along term on a huge line, newline in name, symlink target, underscore ids, odd paths
expect_hit 'big.js' sh -c 'head -c 6000 /dev/zero | tr "\0" x >big.js; echo " me@plantedhost.example" >>big.js'
expect_hit 'nl' sh -c 'printf "x" >"$(printf "plantedterm\nfoo")nl"'
expect_hit 'lnk' ln -s 'plantedterm-target' lnk
expect_hit 'u.txt' sh -c 'echo "plantedsub_x" >u.txt'
expect_hit 'v.txt' sh -c 'echo "planted_regex" >v.txt'
expect_hit 'w.txt' sh -c 'echo "/Users/someone/x" >w.txt'
expect_hit 'x.txt' sh -c 'printf "%s\n" "C:\Users\someone\\x" >x.txt'
expect_hit 'y.txt' sh -c 'printf "%s\n" "C:\Users\someone" >y.txt'
expect_hit 'submodules' sh -c 'printf "[submodule \"s\"]\n\tpath = s\n\turl = ../s\n" >.gitmodules'
expect_hit 'submodules' sh -c 'git init -q s && git -C s -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m s'

# Whole-word and case-sensitive terms must not fire inside other words or in another case.
echo 'xplantedtermx plantedcase' >near.txt
# Generic example path, allowlisted module path, and a path:term entry with a reason
echo 'cd /tmp/example/code' >ok.txt
echo 'import "github.com/example-user/commander/x"' >mod.go
echo 'plantedterm' >legacy.go
echo 'legacy.go:plantedterm # test reason' >>scripts/term-allowlist.txt
git add -A
check >/dev/null || fail "allowlisted terms should pass: $(check)"

echo 'bare.go:plantedterm' >>scripts/term-allowlist.txt
if check >/dev/null; then fail "allowlist entry without a reason should fail"; fi

echo "term-inventory_test: ok"
