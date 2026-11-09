#!/usr/bin/env bash
# Read-only inventory of forbidden terms (scripts/forbidden-terms.txt) in tracked files.
# Usage: scripts/term-inventory.sh [--json] [--check] [--terms FILE]
# A hit is a matching line, or a tracked file name. Exit status is 0, except --check: exit 1 listing
# every non-allowlisted hit (the CI gate, .github/workflows/term-gate.yml).
set -euo pipefail

json=0
check=0
root=$(git rev-parse --show-toplevel)
terms_file="$root/scripts/forbidden-terms.txt"
while [ $# -gt 0 ]; do
  case "$1" in
    --json) json=1 ;;
    --check) check=1 ;;
    --terms) shift; terms_file=$1 ;;
    -h|--help) sed -n '2,4p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

kind_of() {
  case "$1" in
    .beads/*) echo beads ;;
    spikes/*) echo spikes ;;
    prompts/*) echo prompts ;;
    */fixtures/*|*/testdata/*|fixtures/*|testdata/*) echo fixtures ;;
    tests/*|*/test/*|*_test.go|*.test.ts|*.test.tsx) echo tests ;;
    docs/*|*.md) echo docs ;;
    *) echo code ;;
  esac
}

cd "$root"
rows=$(mktemp)
trap 'rm -f "$rows"' EXIT

excludes=(':(exclude)scripts/forbidden-terms.txt' ':(exclude)scripts/term-inventory.sh' ':(exclude)scripts/term-allowlist.txt' ':(exclude)scripts/term-inventory_test.sh')
# "*:text" allowlist entries also exempt any content line containing text (the module path).
global_exceptions=$(awk '{ sub(/[[:space:]]*#.*/, ""); sub(/[[:space:]]+$/, "") } /^\*:/ { print substr($0, 3) }' "$root/scripts/term-allowlist.txt" 2>/dev/null || true)

# rows: kind <TAB> file <TAB> term <TAB> count
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) continue ;; esac
  flags=(-i -w -F)
  term=$line
  case "$line" in
    cs:*) flags=(-w -F); term=${line#cs:} ;;
    sub:*) flags=(-i -F); term=${line#sub:} ;;
    re:*) flags=(-i -E); term=${line#re:} ;;
  esac
  {
    # content: file <TAB> count. Binary files are scanned as text (-a). Lines holding a repository-wide "*:"
    # exception are re-tested with only that substring removed, so other terms on the same line still count.
    git grep -a -z -n "${flags[@]}" -e "$term" -- . "${excludes[@]}" 2>/dev/null | tr '\0' '\037' |
      while IFS=$'\037' read -r file _ content || [ -n "$file" ]; do
        # No line truncation: a term far along a minified line must count. Only a line holding an exception is re-tested.
        stripped=0
        while IFS= read -r ex; do
          if [ -n "$ex" ] && [[ $content == *"$ex"* ]]; then content=${content//"$ex"/}; stripped=1; fi
        done <<<"$global_exceptions"
        if [ "$stripped" -eq 0 ] || printf '%s\n' "$content" | grep -q "${flags[@]}" -e "$term"; then printf '%s\n' "$file"; fi
      done | sort | uniq -c | awk '{ c = $1; $1 = ""; sub(/^ /, ""); print $0 "\t" c }'
    # symlinks: git grep skips them, so test each tracked link's target text (mode 120000)
    git ls-files -s -z -- . "${excludes[@]}" | while IFS= read -r -d '' ent; do
      [[ $ent == 120000* ]] || continue
      name=${ent#*$'\t'}
      if git cat-file blob ":$name" | { grep -a -q "${flags[@]}" -e "$term" 2>/dev/null; }; then printf '%s\t1\n' "${name//$'\n'/\\n}"; fi
    done
    # names: tracked file names count once
    # (NUL-separated so a name holding a newline is one name; shown with the newline as \n)
    git ls-files -z -- . "${excludes[@]}" | { grep -z "${flags[@]}" -e "$term" 2>/dev/null || true; } |
      while IFS= read -r -d '' name; do printf '%s\t1\n' "${name//$'\n'/\\n}"; done
  } |
    while IFS=$'\t' read -r file count; do
      # Explicit path:term exceptions; a comment documents why each is needed.
      if [ -f "$root/scripts/term-allowlist.txt" ] && path="$file" term="$term" awk '
        BEGIN { path = ENVIRON["path"]; term = ENVIRON["term"] }
        { sub(/[[:space:]]*#.*/, ""); sub(/[[:space:]]+$/, "") }
        $0 == path ":" term || $0 == "*:" term { found=1 }
        END { exit !found }
      ' "$root/scripts/term-allowlist.txt"; then continue; fi
      printf '%s\t%s\t%s\t%s\n' "$(kind_of "$file")" "$file" "$term" "$count"
    done >>"$rows" || true
done <"$terms_file"

if [ "$check" -eq 1 ]; then
  # git grep does not recurse into submodules, so their content would go unscanned.
  if [ -f .gitmodules ] || git ls-files -s | grep -q '^160000 '; then
    echo "submodules are not scanned by the term gate; remove them (.gitmodules / gitlink entries)" >&2; exit 1
  fi
  if [ -f scripts/term-allowlist.txt ] && noreason=$(grep -v -E '^[[:space:]]*(#|$)' scripts/term-allowlist.txt | grep -v -E '#[[:space:]]*[^[:space:]]'); then
    echo "allowlist entries need a reason after '#':" >&2; echo "$noreason" | sed 's/^/  /' >&2; exit 1
  fi
  if [ -s "$rows" ]; then
    echo "forbidden terms found (fix them, or allowlist with a reason in scripts/term-allowlist.txt):" >&2
    sort "$rows" | awk -F'\t' '{ printf "  %s: %s (%d)\n", $2, $3, $4 }' >&2
    exit 1
  fi
  echo "term gate: clean"
  exit 0
fi

if [ "$json" -eq 1 ]; then
  jq -R -s '
    split("\n") | map(select(length > 0) | split("\t") | {kind: .[0], file: .[1], term: .[2], count: (.[3] | tonumber)}) as $r
    | {
        total: ($r | map(.count) | add // 0),
        files: ($r | map(.file) | unique | length),
        by_kind: ($r | group_by(.kind) | map({key: .[0].kind, value: {files: (map(.file) | unique | length), hits: (map(.count) | add)}}) | from_entries),
        by_term: ($r | group_by(.term) | map({key: .[0].term, value: (map(.count) | add)}) | from_entries),
        hits: ($r | group_by(.file) | map({file: .[0].file, kind: .[0].kind, total: (map(.count) | add), terms: (map({key: .term, value: .count}) | from_entries)}) | sort_by(.kind, -.total, .file))
      }' <"$rows"
  exit 0
fi

sort -t$'\t' -k1,1 -k2,2 -k3,3 "$rows" | awk -F'\t' '
  { kt[$1] += $4; kf[$1 SUBSEP $2] += $4; if (!(($1 SUBSEP $2) in seen)) { seen[$1 SUBSEP $2] = 1; kfiles[$1]++ }
    tt[$3] += $4; total += $4; detail[$1 SUBSEP $2] = detail[$1 SUBSEP $2] sprintf(" %s=%d", $3, $4)
    if (!($1 in order)) { order[$1] = 1; kinds[++nk] = $1 }
    if (!(($1 SUBSEP $2) in listed)) { listed[$1 SUBSEP $2] = 1; files[$1, ++nf[$1]] = $2 } }
  END {
    for (i = 1; i <= nk; i++) {
      k = kinds[i]
      printf "== %s: %d hits in %d files\n", k, kt[k], kfiles[k]
      for (j = 1; j <= nf[k]; j++) { f = files[k, j]; printf "  %5d  %s  [%s ]\n", kf[k SUBSEP f], f, detail[k SUBSEP f] }
    }
    print "== per term"
    for (t in tt) printf "  %5d  %s\n", tt[t], t | "sort -rn"
    close("sort -rn")
    printf "== total: %d hits\n", total
  }'
