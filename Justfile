# commander Justfile — `just --list` to see all recipes
#
# Install: `brew install just` (or `cargo install just`).
# Run `just install` once to set up commander; everything else is dev/ops.

# Default recipe: list available commands
default:
    @just --list --unsorted

# ────────────────────────────────────────────────────────────────────────────
# Setup
# ────────────────────────────────────────────────────────────────────────────

# Install what `just build` staged: copy bin/staged into place and relink ~/.local/bin
# (explicit; add --restart-cmdrd to also restart cmdrd). First install on a machine: bash install/install.sh
install *ARGS:
    @test -x bin/staged/cmdr || bash scripts/stage.sh
    @staged="$(cat bin/staged/VERSION 2>/dev/null)"; head="$(git rev-parse HEAD)"; \
      case "$staged" in "$head"|"$head"-dirty) ;; *) echo "WARNING: bin/staged is $staged but HEAD is $head (run: just build)" >&2 ;; esac
    @bin/staged/cmdr install {{ARGS}}

# Install Agent Mail (am) + start its server; needed for mail and adding projects
setup-mail:
    @bash install/setup-agent-mail.sh

# Re-run install (idempotent; alias for the dotfiles wizard's update_cmd)
update:
    @git pull --ff-only && bash install/install.sh --update

# Remove commander from system (does NOT touch your dotfiles or shell config)
uninstall:
    #!/usr/bin/env bash
    set -e
    rm -f "$HOME/.local/bin/cmdr" "$HOME/.local/bin/commander" "$HOME/.local/bin/cmdrd"
    rm -rf "$HOME/.commander"
    echo "✓ Removed ~/.local/bin/{cmdr,commander,cmdrd}"
    echo "✓ Removed ~/.commander/"

# ────────────────────────────────────────────────────────────────────────────
# Build
# ────────────────────────────────────────────────────────────────────────────

# Build cmdr + cmdrd into bin/staged/ only; nothing installed or running changes
build:
    @bash scripts/stage.sh

# Screenshot the real Commander window as the user sees it (macOS) -> ~/.commander/shots/app.png
app-shot *ARGS:
    @bash scripts/app-shot.sh {{ARGS}}

# Screenshot the desktop app in every theme (headless) -> app/out/shots/<theme>.png
shots *ARGS:
    @cd app && node scripts/shots.mjs {{ARGS}}

# ────────────────────────────────────────────────────────────────────────────
# Diagnose
# ────────────────────────────────────────────────────────────────────────────

# Print commander version + verify all prereqs are reachable
doctor:
    #!/usr/bin/env bash
    set -uo pipefail
    echo "── commander doctor ────────────────────────────────────────────"
    if command -v cmdr >/dev/null 2>&1; then
        cmdr --version
        echo "✓ cmdr on PATH at $(command -v cmdr)"
    else
        echo "✗ cmdr NOT on PATH (try: just install)"
    fi
    echo
    echo "External tools (see share/contracts.md for pinned versions):"
    for t in go opencode bd am docker sqlite3 tmux jq; do
        if command -v "$t" >/dev/null 2>&1; then
            printf "  ✓ %-10s %s\n" "$t" "$(command -v "$t")"
        else
            printf "  ✗ %-10s MISSING\n" "$t"
        fi
    done
    echo
    echo "Layout:"
    for p in "$HOME/.commander" \
             "$HOME/.commander/config.toml" \
             "$HOME/.commander/objectives" \
             "$HOME/.commander/logs" \
             "$HOME/.local/bin/cmdr" \
             "$HOME/.local/bin/commander"; do
        if [[ -e "$p" || -L "$p" ]]; then
            echo "  ✓ $p"
        else
            echo "  ✗ $p (run: just install)"
        fi
    done

# ────────────────────────────────────────────────────────────────────────────
# Tests
# ────────────────────────────────────────────────────────────────────────────

# Run all tests (Go unit + integration scripts)
test: test-go test-integration

# Register an explicitly selected Rust backend for all Go/eval/ship entrypoints.
setup-eval-backend backend:
    @python3 scripts/setup-eval-backend.py {{quote(backend)}}

# Run the tmux-backed Commander eval scenarios only
test-evals:
    @go test ./tests/evals

# Run Go unit tests
test-go:
    @go test ./...

# Run real-CLI round-trip integration tests (am, bd, opencode, docker)
test-integration:
    #!/usr/bin/env bash
    set -e
    shopt -s nullglob
    failed=0
    for f in tests/integration/test-*.sh; do
        echo "→ $f"
        if ! bash "$f"; then
            echo "  ✗ FAILED: $f"
            failed=1
            break
        fi
    done
    exit $failed

# ────────────────────────────────────────────────────────────────────────────
# Dev
# ────────────────────────────────────────────────────────────────────────────

# Lint Go code (vet + golangci-lint if installed)
lint:
    #!/usr/bin/env bash
    set -e
    go vet ./...
    if command -v golangci-lint >/dev/null 2>&1; then
        golangci-lint run
    else
        echo "(golangci-lint not installed — skipping)"
    fi

# Watch for changes and refresh the running cmdr cockpit (requires entr; falls back to one-shot)
dev:
    #!/usr/bin/env bash
    set -e
    if command -v entr >/dev/null 2>&1; then
        echo "Watching Go sources (entr)..."
        find . -name '*.go' -not -path './vendor/*' | entr -r go run ./cmd/cmdr refresh
    else
        echo "(entr not installed — refreshing once. brew install entr for watch mode.)"
        go run ./cmd/cmdr refresh
    fi

# Tail commander logs
log:
    @ls "$HOME/.commander/logs"/*.log >/dev/null 2>&1 && tail -f "$HOME/.commander/logs"/*.log || echo "(no logs yet at ~/.commander/logs/)"

# Show the verified CLI contracts commander relies on
contracts:
    @less share/contracts.md
