#!/usr/bin/env bash
# Read-only prerequisite check; setup and doctor warn without failing.
# --required is for a review preflight, where missing skills must stop work.
set -uo pipefail

required=0
case "${1:-}" in
    --required) required=1 ;;
    '') ;;
    *) echo "usage: bash install/check-review-plugin.sh [--required]" >&2; exit 2 ;;
esac

registry="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/plugins/installed_plugins.json"
found=0
if command -v jq >/dev/null 2>&1 && [[ -r "$registry" ]] && paths=$(jq -r '
    .plugins | to_entries[] |
    select(.key | split("@")[0] == "compound-engineering") |
    .value | if type == "array" then .[] else . end |
    .installPath | select(type == "string")
' "$registry" 2>/dev/null); then
    while IFS= read -r path; do
        if [[ -n "$path" && -r "$path/skills/ce-code-review/SKILL.md" ]]; then
            found=1
            break
        fi
    done <<< "$paths"
fi

if [[ "$found" -eq 1 ]]; then
    echo "  ✓ review skill compound-engineering:ce-code-review installed"
    exit 0
fi

cat >&2 <<EOF
  ! Review prerequisite missing: compound-engineering:ce-code-review.
    Could not verify its skill file via $registry (jq is required to check).
    Run in your terminal:
      claude plugin marketplace add EveryInc/compound-engineering-plugin && claude plugin install compound-engineering
    Then restart the review session so it can load the skill. If already installed,
    check that the plugin is enabled and available in the review runtime.
EOF
exit "$required"
