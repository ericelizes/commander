# Verify Codex MCP launch policy without launching MCPs

All fixtures below use temporary files and a recording shell stub. They start
no real Codex, MCP, browser, or chat, and never change installed configuration.

Run from a task worktree:

```sh
GOMAXPROCS=2 go test -p 1 ./internal/chat -run 'TestCodex(MCP|BridgeChildNotifications)' -count=1
GOMAXPROCS=2 go test -p 1 ./tests/evals -run TestCodexMCP -count=1
cmdr internal-slot run -- env -u TMUX GOMAXPROCS=2 GOFLAGS=-p=1 go test ./...
```

`TestCodexMCPEffectiveTrustDiscovery` checks a project-only browser under trust
supplied only by the system file or only by the managed file. Ordinary launches
must emit `mcp_servers.project-browser.enabled=false`; explicit selection leaves
that server available. User trust overrides system trust, then managed trust wins.
Unknown and untrusted project layers contribute no MCPs. Malformed active config
fails without echoing its secret-bearing content. Missing files are allowed.

`TestCodexMCPProjectDiscoveryBoundaries` checks directory-specific distrust,
custom project markers, ancestor boundaries, and malformed trusted versus
untrusted project configuration. `TestCodexMCPManagedWorktreeTrust` verifies the
main repository trust fallback and rejects a mismatched worktree registration.
`TestCodexMCPLaunchPolicy` verifies default zero browsers, explicit one browser,
unchanged other MCPs, and managed forced-enable refusal.

The executable eval uses `scenarios/codex-mcp/effective-trust.json`; the existing
`launch.json` checks aliases and multiple configured browsers. The stub records
argv only. Tests verify that configuration remains unchanged.

For cockpit smoke, choose a unique private socket directory and capture detached
panes (the script creates fake agents and an isolated home):

```sh
export COCKPIT_TMUX_TMPDIR="$(mktemp -d /tmp/cl.mcp.XXXXXX)"
cmdr internal-slot run -- env GOMAXPROCS=2 GOFLAGS=-p=1 scripts/cockpit-test.sh up
cmdr internal-slot run -- env GOMAXPROCS=2 GOFLAGS=-p=1 scripts/cockpit-test.sh text
scripts/cockpit-test.sh down
```

The cockpit smoke confirms the detached UI starts; it does not prove a real MCP
was disabled. The synthetic unit and executable eval provide that policy proof.
Do not install or restart anything to run this recipe.

Discovery follows the file-backed system/user/managed ordering in the pinned
[Codex 0.164 loader](https://github.com/openai/codex/blob/rust-v0.164.0/codex-rs/config/src/loader/mod.rs)
and its
[managed discovery merge](https://github.com/openai/codex/blob/rust-v0.164.0/codex-rs/config/src/loader/project_discovery.rs).
The bridge does not expose profiles, cloud config, MDM config injection, or session
trust overrides; this policy inventories the default file-backed launch path.
Managed `enabled=true` cannot be overridden by session flags: the launch fails
with the server name and an instruction to ask the administrator to remove that
forced setting. This intentionally refuses that Codex launch rather than silently
starting an agent with browser access it was not authorized to have.

Symlink regression coverage (`TestCodexMCPSymlinkTrust`) keeps raw cwd ancestors,
with canonical then raw trust keys for each directory, project root, and main
repository fallback. Conflicting canonical distrust wins over raw trust; a
specific directory denial wins over project trust. Fixtures include a symlink
whose physical ancestors differ, malformed configs outside both project-root
boundaries, system/managed-only raw and canonical keys, and reciprocal linked
worktree registration through aliases. Tests intentionally retain unresolved
temporary paths, including macOS `/var` spellings.

Repeat targeted tests and recording evals with `TMPDIR` pointing to a symlink to
an existing scratch directory; use an absolute path. Do not canonicalize fixture
trust keys to make them pass. The child-completion eval links its throwaway cmdr
binary to synthetic system/managed paths. Chat test helpers clear those paths
before dispatch, so even re-executed bridge helpers cannot read `/etc/codex`.

Codex 0.164 `config/src/overrides.rs` splits override keys on literal dots
without decoding quoted segments. Every discovered browser alias must use only
ASCII letters, digits, underscores or hyphens. Unsupported aliases fail before
app-server launch, including explicit selection and disabled entries; the error
names the alias and asks for a rename. Nonbrowser aliases remain unchanged.
`TestCodexMCPUnsafeBrowserAlias` covers the policy and config preservation;
the unsafe-alias recording eval checks the actual bridge refuses before its
fake app-server starts.

Project-root discovery mirrors the pinned loader's stat checks: a .git directory
requires a stat-able HEAD, while a .git file or other configured marker qualifies
directly. TestCodexMCPHeadlessNestedMarker covers nested HEAD-less directories,
valid HEAD, gitfiles, symlinked directories and custom directory markers. Every
effective-trust recording scenario includes nested HEAD-less metadata.

Browser opt-in is intentionally bridge-only and defaults off; no public cross-layer
browser flag is exposed. Existing opt-in environment behavior is unchanged.
