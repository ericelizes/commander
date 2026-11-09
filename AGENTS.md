# AGENTS.md

The single source of truth for working in this repo is **[CLAUDE.md](./CLAUDE.md)**.

Read it before building, running, or testing. Key rule: every feature ships with
**both** an automated test (`go test ./...` + a `tests/evals` scenario) **and** a
live cockpit verification (`scripts/cockpit-test.sh text`). Don't disturb the
user's monitor — drive the cockpit via the detached tmux session, never on their
visible workspace.

Setup: `just install` (cmdr), then `just setup-mail` (Agent Mail server —
required for mail and for adding projects).
