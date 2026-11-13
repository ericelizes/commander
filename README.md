# commander

A desktop app for running a team of AI agents across your projects. You talk
to **Commander**; it starts a **Manager** per piece of work (a workstream), and
each Manager runs **Operators** that do the steps in their own git worktrees.
The sidebar shows every workstream and what needs you; the right pane shows a
workstream's plan, its chat and any web pages you pin.

Two parts: `cmdrd`, the background service that runs the agents (on your Mac
or on a Linux box/VM), and the Electron app that shows it.

## Set up

1. **Install the prerequisites**: Go ≥1.22, Node ≥20 (for the app), `bd`
   (`brew install beads`), `git`, `jq`, `tmux`, and the Claude Code CLI
   (`npm i -g @anthropic-ai/claude-code`). `just doctor` lists what is missing.
2. **Run the installer** from this checkout:

   ```bash
   bash install/install.sh
   ```

   It builds and installs `cmdr`/`cmdrd`, creates `~/.commander`, and sets up
   **Agent Mail** (the server agents use to message each other). Safe to re-run.
3. **Sign in to Claude** once: run `claude` and log in.
4. **Open the app**:

   ```bash
   cmdr app install   # build the desktop app into /Applications (macOS)
   cmdr app           # open it; starts cmdrd if it is not running
   ```

   On a Linux box with no display, `cmdr app --web` prints a link instead.
5. **Start working**: type to Commander in the chat, e.g. "set up a workstream
   to add CSV export to billing". Commander picks the project under `~/code`
   (or `COMMANDER_WORKSPACE_ROOT`).

**Using a fork?** In `bd` 1.3.4, the default `routing.mode=auto` treats the
checkout as a contributor's and routes every write to `~/.beads-planning`,
outside the project database. Commander's reads keep that embedded store
locked, so every `cmdr` write fails with `embeddeddolt: store is read-only`.
Run `bd config set routing.mode maintainer` in the project, then remove the
`repos.additional` key and its list item from `.beads/config.yaml` by hand.
Avoid `bd config unset repos.additional`: it comments out the key but leaves
the list item, producing invalid YAML.

### Optional: connect things

All optional; nothing needs them.

- **Linear**: Settings → Connections → Linear API key (or `cmdr linear
  set-key`). Adds the My sprint card and ticket chips on workstreams.
- **Signed-in web pages** in the right pane: `cmdr browser login` signs the
  pane's browser in to GitHub, Linear, Notion, Slack or Datadog.

### Updating

The download icon in the sidebar's bottom bar shows when `main` has new
commits; click it and Update. From a terminal: `git pull && just build &&
just install --yes --restart-cmdrd`. Chats keep running through a restart.

Builds go to `bin/staged/` only (`just build`); nothing running changes until
`just install` (`cmdr install`) copies them to `~/.commander/bin/` (the
previous build is kept: `cmdr install --rollback`).

Two CLI entry points (both point at the same binary):

- `cmdr` — primary, 3-char. Bare `cmdr` launches or attaches the tmux session.
- `commander` — full-name alias.

Commander opens as a cross-project top-tier session rooted at `~/code` by
default. Override that with `COMMANDER_WORKSPACE_ROOT=/abs/path` if your
multi-project workspace lives elsewhere.

## Choosing the agent runtime (opencode or Claude Code)

Every tier runs on opencode by default. To run any tier on Claude Code
instead, add a `[runtime]` table to `~/.commander/config.toml`:

```toml
[runtime]
default   = "claude"     # "opencode" (default) or "claude"
# commander = "opencode" # optional per-tier overrides: commander, lead, operator
```

`COMMANDER_RUNTIME=claude cmdr` overrides every tier for one launch. Running
`cmdr` after a change rebuilds the Commander window on the new runtime;
`cmdr refresh` does the same for open Manager/Operator windows. Each runtime
keeps its own session ids (`*.session-id` vs `*.claude.session-id`), so
switching back resumes where you left off.

Claude panes load the role prompt with `--append-system-prompt-file` from
`~/.commander/claude-agents/`; nothing is installed into `~/.claude/agents`.
Models come from `[models]`: family names map to Claude aliases
(`anthropic/claude-opus-4-7` → `opus`). Code-type operators in Docker need the
`commander/operator` image to contain `claude`, plus `CLAUDE_CODE_OAUTH_TOKEN`
(from `claude setup-token`) or `ANTHROPIC_API_KEY` in the environment, because
the macOS Keychain login is not visible inside a container.

## Daily commands

| Command | Does |
|---------|------|
| `cmdr` | Launch or attach the `commander` tmux session. |
| `cmdr --project commander` | Create or focus the Lead window for a project. |
| `cmdr --task fro-001` | Create or focus the Operator window for a task. |
| `cmdr refresh` | Recreate currently open Commander-owned windows so layout/code changes land during development. |
| `cmdr restart` | Kill the current Commander session and start a fresh Commander brain. |
| `cmdr --version` | Print the version (currently `0.0.1`). |
| `cmdr --help` | Print the command surface. |

## Requirements

See `share/contracts.md` for the full pinned-version table and verified CLI
shapes. Headline pins:

| Tool       | Min version | Notes                                       |
|------------|-------------|---------------------------------------------|
| `go`       | 1.22        | Build toolchain.                            |
| `opencode` | 1.14.0      | Agent runtime. Install via `npm i -g opencode-ai`. |
| `bd`       | 1.0.0       | Beads task tracker. `brew install beads` (or `npm i -g @beads/bd`). |
| `am`       | 0.2.40      | Agent Mail.                                 |
| `docker`   | 20.10       | Code-type operator sandboxing.              |
| `tmux`     | 3.3         | Knowledge-type operator panes.              |
| `sqlite3`  | 3.35        | Bundled with macOS / present on most Linux. |
| `jq`       | 1.6         | Stream parsing.                             |

`just doctor` checks all of these and prints status.

## Layout

```
commander/
├── cmd/cmdr/            # CLI entry point + session capture/activation helpers
├── internal/layout/     # tmux session/window/pane orchestration
├── internal/cli/        # help + version
├── install/             # install.sh + commander.toml apps fragment
├── share/contracts.md   # verified external CLI shapes (single source of truth)
├── docs/                # plans, requirements
├── tests/integration/   # contract verification scripts
├── tests/evals/         # tmux-backed Commander interaction eval harness
├── Justfile             # task runner
└── go.mod
```

## Uninstall

```bash
just uninstall
```

Removes `~/.local/bin/{cmdr,commander}` and `~/.commander/`. Does not touch
your dotfiles or shell config.
