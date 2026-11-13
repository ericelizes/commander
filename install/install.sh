#!/usr/bin/env bash
# commander installer — stages a build, installs it (cmdr install), sets up ~/.commander/.
#
# Idempotent: safe to run repeatedly. Re-running on a configured machine is a
# no-op for layout and updates the binary in-place. Detects OS (darwin* | linux*)
# and warns about any missing prereqs without failing.
#
# Usage:
#   install/install.sh            # fresh install or re-apply
#   install/install.sh --update   # same as above; alias for the wizard's
#                                 # update_cmd contract in apps.toml
#   install/install.sh --quiet    # suppress per-step output (keep warnings)
#   install/install.sh --restart-cmdrd  # also reload the cmdrd unit onto the new binary
#
# Builds go to bin/staged; `cmdr install` copies them into place. A running
# cmdrd is left alone unless --restart-cmdrd is given.
#
# Exit status:
#   0  install applied or already-applied
#   1  unrecoverable error (e.g. cannot create ~/.commander, build failure)

set -uo pipefail

# ── Args ────────────────────────────────────────────────────────────────────

QUIET=0
UPDATE=0
RESTART_CMDRD=0
for arg in "$@"; do
    case "$arg" in
        --quiet) QUIET=1 ;;
        --update) UPDATE=1 ;;
        --restart-cmdrd) RESTART_CMDRD=1 ;;
        -h|--help)
            sed -n '2,22p' "$0" | sed 's/^# //; s/^#//'
            exit 0
            ;;
        *)
            echo "install.sh: unknown flag '$arg'" >&2
            exit 1
            ;;
    esac
done

say() { [[ $QUIET -eq 1 ]] || echo "$@"; }
warn() { printf '  ! %s\n' "$*" >&2; }
ok() { say "  ✓ $*"; }

# ── Paths ───────────────────────────────────────────────────────────────────

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMMANDER_HOME="${COMMANDER_HOME:-$HOME/.commander}"
LOCAL_BIN="$HOME/.local/bin"

# ── OS detection ────────────────────────────────────────────────────────────

case "$OSTYPE" in
    darwin*) OS=macos ;;
    linux*)
        # WSL identifies as linux; surface it for diagnostic clarity.
        if grep -qi microsoft /proc/version 2>/dev/null; then
            OS=wsl
        else
            OS=linux
        fi
        ;;
    *)
        warn "unsupported OS '$OSTYPE' — commander targets macOS and Linux/WSL"
        exit 1
        ;;
esac

say "commander installer (os=$OS, repo=$REPO_DIR)"

# ── Prereq check (warn-only; doctor re-checks) ──────────────────────────────

check_cmd() {
    local cmd="$1" hint="${2:-}"
    if command -v "$cmd" >/dev/null 2>&1; then
        ok "prereq $cmd"
    else
        warn "prereq $cmd missing${hint:+ — $hint}"
    fi
}

say "checking prereqs (see share/contracts.md for pinned versions)"
check_cmd go "brew install go (≥1.22)"
check_cmd opencode "npm install -g opencode-ai"
check_cmd bd "brew install beads (or npm i -g @beads/bd)"
check_cmd docker "Docker Desktop or Docker Engine ≥20.10"
check_cmd sqlite3
check_cmd tmux "brew/apt install tmux (≥3.3)"
check_cmd jq
check_cmd fzf
check_cmd uuidgen
check_cmd git

# ── Agent Mail: agents message each other through it, so set it up here ─────

if command -v am >/dev/null 2>&1 && curl -fsS --max-time 3 http://127.0.0.1:8765/health/liveness >/dev/null 2>&1; then
    ok "agent mail running"
else
    say "setting up Agent Mail"
    if bash "$REPO_DIR/install/setup-agent-mail.sh"; then
        ok "agent mail running"
    else
        warn "Agent Mail setup failed — re-run: just setup-mail"
    fi
fi

# ── Stage the build, then install it (explicit: refused for agents) ────────
#
# Builds only write bin/staged; `cmdr install` copies them into place.

say "staging build"
if [[ -n "${COMMANDER_ROLE:-}" ]]; then
    warn "refusing to install from agent role $COMMANDER_ROLE; installing needs the user's OK"
    exit 1
fi
if ! command -v go >/dev/null 2>&1; then
    warn "go not on PATH — cannot build cmdr binary"
    warn "install Go ≥1.22 then re-run: bash install/install.sh"
    exit 1
fi
if bash "$REPO_DIR/scripts/stage.sh"; then
    ok "staged $REPO_DIR/bin/staged"
else
    warn "build failed — see output above"
    exit 1
fi
INSTALL_ARGS=(--yes)
[[ $RESTART_CMDRD -eq 1 ]] && INSTALL_ARGS+=(--restart-cmdrd)
if ! COMMANDER_REPO="$REPO_DIR" "$REPO_DIR/bin/staged/cmdr" install "${INSTALL_ARGS[@]}"; then
    warn "cmdr install failed"
    exit 1
fi

# ── Layout: ~/.commander/{config.toml,objectives,logs} ──────────────────────

say "linking $COMMANDER_HOME"

mkdir -p "$COMMANDER_HOME" "$COMMANDER_HOME/objectives" "$COMMANDER_HOME/logs" || {
    warn "could not create $COMMANDER_HOME"
    exit 1
}
ok "dir $COMMANDER_HOME"
ok "dir $COMMANDER_HOME/objectives"
ok "dir $COMMANDER_HOME/logs"

# Seed an empty config.toml only if absent. U4 owns the schema.
if [[ ! -e "$COMMANDER_HOME/config.toml" ]]; then
    cat > "$COMMANDER_HOME/config.toml" <<'EOF'
# commander config — populated by U4. Edit at your own risk pre-U4.
EOF
    ok "seed $COMMANDER_HOME/config.toml"
else
    ok "config $COMMANDER_HOME/config.toml (already present)"
fi

# state.db is created by U3 on first run that touches the store.

# ── PATH: ~/.local/bin links are made by `cmdr install` ────────────────────
say "wiring PATH"
mkdir -p "$LOCAL_BIN" || {
    warn "could not create $LOCAL_BIN"
    exit 1
}

# Install the per-OS unit so cmdrd starts at login (and restarts on crash).
# Best-effort: a missing launchctl / systemctl is a warning, not a failure.
say "installing cmdrd unit (os=$OS)"
case "$OS" in
    macos)
        source "$REPO_DIR/install/legacy-launchd.sh"
        # tmux keeper first: new Operator sessions go to its socket, so it must
        # be up before cmdrd restarts. Never booted out when already loaded:
        # that would end the Operators running under it.
        KEEPER_SRC="$REPO_DIR/install/launchd/org.commander-tmux.plist"
        KEEPER_DST="$HOME/Library/LaunchAgents/org.commander-tmux.plist"
        if [[ -f "$KEEPER_SRC" ]]; then
            mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.commander/logs"
            sed "s|__HOME__|$HOME|g" "$KEEPER_SRC" > "$KEEPER_DST"
            ok "wrote $KEEPER_DST"
            if command -v launchctl >/dev/null 2>&1; then
                if launchctl print "gui/$(id -u)/org.commander-tmux" >/dev/null 2>&1; then
                    ok "tmux keeper already loaded (left running)"
                elif legacy_keeper_loaded; then
                    retire_legacy_keeper_plist
                    ok "legacy tmux keeper left running until logout"
                elif launchctl bootstrap "gui/$(id -u)" "$KEEPER_DST" 2>/dev/null; then
                    ok "loaded tmux keeper (tmux -L commander-ops)"
                else
                    warn "launchctl bootstrap of the tmux keeper failed — run manually:"
                    warn "  launchctl bootstrap gui/\$(id -u) $KEEPER_DST"
                fi
            fi
        fi
        PLIST_SRC="$REPO_DIR/install/launchd/org.commander.plist"
        PLIST_DST="$HOME/Library/LaunchAgents/org.commander.plist"
        if [[ -f "$PLIST_SRC" ]]; then
            mkdir -p "$HOME/Library/LaunchAgents"
            # Substitute __HOME__ token so the StandardOut/Err paths resolve
            # under the actual user's home dir.
            sed "s|__HOME__|$HOME|g" "$PLIST_SRC" > "$PLIST_DST"
            ok "wrote $PLIST_DST"
            if command -v launchctl >/dev/null 2>&1; then
                # bootout is idempotent (no-op if not loaded). bootstrap loads
                # the plist into the user's gui domain.
                cleanup_legacy_daemon
                if launchctl print "gui/$(id -u)/org.commander" >/dev/null 2>&1 && [[ $RESTART_CMDRD -eq 0 ]]; then
                    ok "cmdrd already loaded (left running; pass --restart-cmdrd to reload it)"
                    PLIST_SKIP=1
                fi
                [[ ${PLIST_SKIP:-0} -eq 1 ]] || launchctl bootout "gui/$(id -u)" "$PLIST_DST" 2>/dev/null || true
                if [[ ${PLIST_SKIP:-0} -eq 1 ]]; then
                    :
                elif launchctl bootstrap "gui/$(id -u)" "$PLIST_DST" 2>/dev/null; then
                    ok "loaded launchd unit"
                else
                    warn "launchctl bootstrap failed — run manually:"
                    warn "  launchctl bootstrap gui/\$(id -u) $PLIST_DST"
                fi
            else
                warn "launchctl not on PATH — daemon won't auto-start"
            fi
        fi
        ;;
    linux|wsl)
        KEEPER_UNIT_SRC="$REPO_DIR/install/systemd/commander-tmux.service"
        if [[ -f "$KEEPER_UNIT_SRC" ]] && command -v systemctl >/dev/null 2>&1; then
            # Before cmdrd; enable --now is a no-op for a running keeper.
            mkdir -p "$HOME/.config/systemd/user"
            cp "$KEEPER_UNIT_SRC" "$HOME/.config/systemd/user/commander-tmux.service"
            systemctl --user daemon-reload 2>/dev/null || true
            systemctl --user enable --now commander-tmux.service 2>/dev/null \
                && ok "enabled commander-tmux.service (tmux -L commander-ops)" \
                || warn "could not enable commander-tmux.service"
        fi
        UNIT_SRC="$REPO_DIR/install/systemd/commander.service"
        UNIT_DST="$HOME/.config/systemd/user/commander.service"
        if [[ -f "$UNIT_SRC" ]]; then
            mkdir -p "$HOME/.config/systemd/user"
            cp "$UNIT_SRC" "$UNIT_DST"
            ok "wrote $UNIT_DST"
            if command -v systemctl >/dev/null 2>&1; then
                systemctl --user daemon-reload 2>/dev/null || warn "systemctl daemon-reload failed"
                if systemctl --user enable --now commander.service 2>/dev/null; then
                    ok "enabled commander.service (user)"
                else
                    warn "systemctl --user enable failed — run manually:"
                    warn "  systemctl --user enable --now commander.service"
                fi
            else
                warn "systemctl not on PATH — daemon won't auto-start"
            fi
        fi
        ;;
esac

# Surface PATH hint only if ~/.local/bin isn't on PATH already.
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$LOCAL_BIN"; then
    warn "'$LOCAL_BIN' is not on your PATH"
    warn "  add this line to your ~/.zshrc (or equivalent):"
    warn "    export PATH=\"\$HOME/.local/bin:\$PATH\""
    warn "  then: source ~/.zshrc  (or open a new shell)"
fi

say ""
if [[ $UPDATE -eq 1 ]]; then
    say "commander updated. cmdrd was not restarted (update/reload no longer restart it);"
    say "  to run the new build: cmdr install --restart-only"
else
    say "commander installed. next steps:"
    if echo "$PATH" | tr ':' '\n' | grep -qx "$LOCAL_BIN"; then
        say "  1. run:  cmdr --version"
        say "  2. run:  cmdr          (or: cmdr launch)"
    else
        say "  1. add ~/.local/bin to PATH (see warning above)"
        say "  2. run:  cmdr --version"
    fi
fi

exit 0
