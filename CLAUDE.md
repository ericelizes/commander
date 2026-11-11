# CLAUDE.md — working in the `commander` repo

Single source of truth for how an agent builds, runs, and **tests** commander.
Read `README.md` for the product story and `docs/` for plans. This file is about
*how you work here*. Keep it current (see "Updating this file" at the bottom).

`commander` is a tmux-orchestrated three-level agent cockpit (Commander → Lead →
Operator). `cmdr` lays out native `opencode` chat panes plus commander-rendered
chrome columns (objectives, fleet, tasks). Because it's a TUI, you can drive and
inspect it as **text** — no GUI, no screenshots required.

## Eval backend prerequisite

Before `go test ./...`, `just test-go`, `just test-evals`, or ship verification,
run `python3 scripts/setup-eval-backend.py /absolute/path/to/trusted/beads_rust`
(or `just setup-eval-backend /absolute/path/to/trusted/beads_rust`). Python 3
and beads_rust **0.7.6** are required. This copies the explicitly chosen binary
into the user's private cache and records its version and SHA-256. All these
entrypoints resolve and hash-check the same record automatically, before test
HOME isolation; no ambient `COMMANDER_EVAL_BR_BIN` is required. Missing or changed
dependencies fail closed with setup instructions. Never pass the host routing
wrapper as the source. See [eval setup](tests/evals/README.md) for cache locations
and CI provisioning. This command only provisions test tooling; it does not
install Commander or modify the host `br` wrapper.

## The golden rule: two tests per feature, always

A feature is **not done** until you have BOTH:

1. **An automated test** — `go test ./...` (unit), and a `tests/evals/scenarios/*.json`
   scenario when you add or change a Commander interaction flow.
2. **A live verification** — you actually saw it work. `scripts/cockpit-test.sh`
   isolates cooperative cmdr builds: throwaway HOME/COMMANDER_HOME, recreated
   fake opencode, failing claude/codex stubs, minimal PATH and clean environment.
   It rejects linked/hardlinked descendants, protected homes and foreign socket
   directories/servers. This is not an OS sandbox: arbitrary code or supplied
   COCKPIT_CMDR/COCKPIT_FAKE_OPENCODE overrides can execute absolute binaries or
   access external files. Use only trusted test overrides, never real agents;
   concurrent hostile filesystem mutation is outside the guarantee. Test with
   `scripts/cockpit-test_test.sh`. Never
   start a real cockpit, Commander or Manager session to verify (it can hold
   a Manager's session for minutes and block its mail). For the real system use read-only
   checks: `cmdr board`, `cmdr app shot`, `just app-shot`.

For every change, answer these three in your commit/PR body:
- **Unit:** which `go test` covers the logic?
- **End-to-end:** which eval scenario exercises the flow? (add one if missing)
- **Live:** what did `cockpit-test.sh text` (isolated), `cmdr board` or `cmdr app shot` (grep the lines you need from its text file) show? (paste it)

If you can't answer all three, the feature isn't finished. "Tests pass" alone is
not enough — this is an interactive cockpit; prove it works *in the cockpit*.

## Environment (this machine)

- **Go** is Homebrew's (`/opt/homebrew/bin/go`, on PATH); `~/.local/go/bin` no
  longer exists on this machine. `go.mod` requires **Go 1.25+**.
- **External CLIs:** `opencode` (≥1.14) and/or `claude`, `br` (beads_rust,
  `cargo install beads_rust`), `am` (agent-mail), `tmux`, `docker`, `jq`.
  SQLite is **embedded** (`modernc.org/sqlite`) — the `sqlite3` CLI is NOT
  required.
- **Agent Mail:** `just setup-mail` installs `am`, starts its server on
  `127.0.0.1:8765` (launchd `com.agent-mail` / systemd `agent-mail.service`)
  and checks the token in `~/.config/mcp-agent-mail/config.env`. Without it,
  mail fails with "no am bearer token" and adding a project fails.
- **Mail load:** am has no batched inbox read, so readers skip quiet inboxes.
  `rolemail.Send` touches `$COMMANDER_HOME/mail-signals/<role>`
  (`mailstate.Signal`); the Waker reads an inbox at first sight, when signalled,
  while mail waits undelivered, and once per `wake.SweepEvery` (2 min); finished
  Operators are never read. The mail hook reads only when signalled since its
  last full read (`mailstate.NeedsCheck`, `<role>.checked`). Snapshot unread
  counts come from the Waker's last read (`Waker.Unread`), not the server.
  `fetch_inbox` always passes `mark_read:false` (the server default writes).
  `am robot metrics --json` gives live per-tool call counts.
- **Build vs install:** `just build` (`scripts/stage.sh`) writes `bin/staged/{cmdr,cmdrd,VERSION}`
  (VERSION = git revision, `-dirty` if modified) and nothing else; no build touches what runs. `just install` =
  `cmdr install` (`internal/binstage`, `cmd/cmdr/install_cmd.go`): copies staged binaries atomically as REAL files
  to `$CMDR_INSTALL_DIR` (default `$COMMANDER_HOME/bin`, else `~/.commander/bin`), keeps `<name>.prev` +
  `VERSION.prev`, writes `REPO` (the checkout; `binstage.RepoHint` is how installed binaries find it, since they no
  longer sit under `<repo>/bin`), relinks `~/.local/bin/{cmdr,commander,cmdrd}` (a non-symlink is left alone),
  prints `revision: old -> new`. Install copies all files to `.new` first, hardlinks `X`->`X.prev`, then renames over `X`
  (dest never absent, mixed cmdr/cmdrd/VERSION undone on failure); an already-installed revision copies nothing and keeps
  `.prev`; `REPO` must be a dir with go.mod and not a git worktree, and `RepoHint` ignores a `REPO` that no longer
  exists. Refused when `COMMANDER_ROLE` is set; asks to confirm unless `--yes`; cmdrd restarts
  only with `--restart-cmdrd` (after the install) or alone with `cmdr install --restart-only` (kickstart/systemctl, no
  copy). Both restarts then SIGTERM any cmdrd daemon (not `__chat-host`/`__codex-bridge`) that was running before and survived, e.g. one the app started (found via `cmdrd.lock` pid + `ps`; 15s, then SIGKILL with a message; `cmd/cmdr/restart_cmdrd.go`, tests `RestartOnly*`). The very first install has no `.prev`: roll back by re-pointing the `~/.local/bin` links at the old
  `<repo>/bin/{cmdr,cmdrd}`. `just install` warns when `bin/staged/VERSION` is not HEAD. Linux caveat: after rename-over a
  running old cmdrd's `os.Executable()` reads `cmdrd.prev`; restart cmdrd after installing; `cmdr install --rollback` swaps `.prev` back. `install/install.sh` stages, installs and
  leaves a loaded cmdrd alone unless `--restart-cmdrd`. Tests use `HOME`/`CMDR_INSTALL_DIR` temp dirs
  (`binstage_test.go`, `install_cmd_test.go`); never run install yourself.
- opencode config lives at `~/.opencode/opencode.json` (schema: top-level `mcp`
  key, not `mcpServers`). A broken opencode config makes `cmdr` abort at launch.
- **Runtime:** each tier runs on opencode or Claude Code, picked by
  `internal/agentrt` (`$COMMANDER_RUNTIME`, then `[runtime]` in
  `~/.commander/config.toml`, then opencode). Claude session ids are minted
  UUIDs in `*.claude.session-id` files; role prompts go to
  `~/.commander/claude-agents/`. Test packages pin `COMMANDER_RUNTIME=opencode`
  in `TestMain`; Claude tests opt in with `t.Setenv`.
- **Native chat engine** (`internal/chat`, served by cmdrd at `/api/chat`): owns
  headless `claude -p` stream-json processes per role; one owner per session id
  (flock in `$COMMANDER_HOME/chat-locks` + a `ps` scan), logs in
  `$COMMANDER_HOME/chat/<role>.jsonl`. Unit tests use the fake claude in
  `internal/chat/chattest` (the test binary re-execs itself; call
  `chattest.MaybeRunFake()` first in `TestMain`). `COMMANDER_CHAT_REAL=1 go test
  -run TestRealClaude ./internal/chat/` drives the real CLI on a throwaway
  session (costs plan usage; haiku). Handover/pop-out of real chats is off
  unless cmdrd runs with `COMMANDER_CHAT_HANDOVER=1`. Never Start/handover a real
  Commander/Manager role from a test. Design: `docs/plans/2026-10-20-native-chat-engine.md`.
- **cmdrd restarts:** the API listener uses SO_REUSEPORT (`api/listen.go`), so a
  new cmdrd takes `:8787` while the old one drains (no drift to 8788). Each
  running chat is a detached **chat host** (`cmdrd __chat-host <cfg>`, setsid;
  `internal/chat/host.go`) that owns the claude child, its stdin and the session
  flock, journals claude's raw stdout and serves it on
  `$COMMANDER_HOME/chat-hosts/<role>.sock` (long paths fall back to a hashed
  `/tmp/cmdr-chat-*.sock`). The Engine normalizes/persists/publishes as before and
  acks what it published; `Engine.Close` only flushes acks and disconnects, so a
  restart changes no claude pid and interrupts no turn. `Start`/`ResumeRunning`
  adopt a surviving host (history up to the host's ack is replayed silently to
  rebuild normalizer/pending state), else spawn one; `Stop` ends claude and the
  host; a host whose role is no longer marked running is ended at resume. Tests:
  call `MaybeRunHost()` first in `TestMain`; kill stray `__chat-host` processes a
  failed run leaves. Hosts retire their listener before publishing child exit, so
  immediate runtime/account switches cannot adopt an exiting host; deferred cleanup
  closes that same listener without removing a successor socket. Pre-host chats (children of an old cmdrd) still end at the
  restart that installs this: do that restart while chats are idle. The plist has
  `AbandonProcessGroup` (needs `cmdr install`/`just install` + a manual reload).
- **Chat cost:** the Engine hands each live `turn_result` to `Options.Costs`
  (`chat.StoreCosts` -> `chat_costs`, migration 0011; unique on session id +
  cumulative `total_usd`, so replay/adoption/backfill never double count).
  `Engine.BackfillCosts` reads `chat/*.jsonl` once per cmdrd start. Snapshot
  `fleet.chat_cost_usd_24h` and `fleet.roles_cost[]` (24h/session/total, est.:
  plan-equivalent); `cost_usd_24h`/`7d` and the series include chats. The app shows
  it only as the spend line's hover and the chat header tooltip.
- **Terminal-held history:** a role whose claude runs in a tmux TUI has no
  native log; `Engine.Status` (the app polls it) publishes its Claude transcript
  into the chat log (`syncTranscript`, events counted per session, only the new
  tail on later polls; skipped for sessions the engine itself ran), so the app's
  terminal mode shows real history, not "no messages yet".
- **Restart recovery:** `ResumeRunning` sends `chat.ResumeMessage` (mail origin)
  to a role whose log ended mid-turn and whose claude it had to spawn anew (an
  adopted host keeps its own turn). Operators found dead in cmdrd's first reap
  sweep (`reapHostRuns`) are reopened and respawned with `Resume` (same
  worktree, `--resume` the session file's id), capped at 2 by `auto-respawn:<n>`
  bead labels (`daemon/settle.go`). **Crash restart:** an Operator that dies while cmdrd watches (session gone, or
  exited without a final report) is reopened and restarted ONCE on the same bead/branch/worktree (`Resume` with
  `ResumeNote` asking for the report; label `crash-respawn:<unix>`; a `cmdrd: ...` comment on the bead records the
  event and `finalReport` skips such notes; no mail). Caps: one per bead, 3 per step and 20 fleet-wide per hour
  (`restartBudget`, in memory). A spawn that fails is retried once at the next sweep (`partialErr`). Dying again, a
  refused restart or a second spawn failure hands the step back (`stopped`): `operator-died` label, the Manager mailed
  the facts ("Not restarted: ...", last pane output via `spawn.OutputTail`). A worktree on a detached HEAD (`spawn.DetachedHEAD`) has no branch to
  restart on: handed back at once, and `internal-operator-provider` refuses before it closes anything (tests `TestSettleDiedOnDetachedHEADHandsBack`, `TestOperatorProviderRefusesADetachedWorktree`). `internal-spawn-operator` retries a busy
  or timed-out failure once after `spawnRetryAfter` (20s) before it reports and mails. Tests: `CrashRestart*`.
- **Codex runtime (per role):** `<home>/chat/<role>.runtime` = `codex` (missing = claude) makes the
  chat host run `cmdrd __codex-bridge` (`internal/chat/codexbridge.go`): Claude stream-json on stdio,
  `codex app-server` JSON-RPC underneath; thread id in `<role>.codex-thread`. Switch with
  `cmdr runtime <role> [claude|codex]` or `POST /api/chat/<role>/runtime {"runtime":..}` (app: chat
  menu Run on Codex/Claude): restarts a running chat, refused mid-turn (409). Each switch hands over:
  a fresh Codex thread reads `<role>.jsonl` on its first turn; Claude back from Codex gets the same
  note once (`<role>.handoff`). Init reports model `codex:<m>`, version `codex-<v>`. **Hooks:** the
  bridge (`--hooks-role R`) passes Claude's hooks to `codex app-server` as `-c hooks.<Event>=[...]`
  overrides (`agentrt.CodexHookArgs`; never written to any CODEX_HOME) and starts/resumes the thread with
  `config:{bypass_hook_trust:true}` (Codex skips untrusted session-flag hooks otherwise; the app-server
  has no `--dangerously-bypass-hook-trust`, and it also lets the user's own untrusted hooks run). Codex
  0.163 sends Claude's hook wire format: shell = `tool_name` Bash with string `command`, edits =
  `apply_patch` with the patch in `command` (matchers `Edit|Write` also hit it). So mail arrives
  mid-turn as on Claude (UserPromptSubmit context, Stop block; the wake loop's `DeliverMail` stays
  the fallback), and the Manager guard (matcher `Bash|apply_patch|Edit|Write`: Codex has no
  `--disallowedTools`, so the guard refuses its edits) and Operator guard run. Guards read tool input
  through `hookInput.shellCommand`/`isEditTool` (`cmd/cmdr/internal_mail_hook.go`: Bash/shell/exec_command,
  `command` string or argv). Tokens: the result line carries `usage` (uncached input, cache reads,
  output, summed over the turn's `thread/tokenUsage/updated` `last`); `ClaudeEventMapper` counts it when
  assistant lines had none, so `max_tokens_per_operator` caps Codex runs ($0 cost on a ChatGPT plan).
  Agent-mail MCP servers configured in `$CODEX_HOME/config.toml` are disabled with `-c
  mcp_servers.<name>.enabled=false`. The bridge also reads system and trusted-project
  file config plus `/etc/codex/managed_config.toml`. Project discovery merges system,
  user, then managed trust (Codex 0.164); malformed trust fails with a redacted error.
  Managed forced-enable conflicts require an administrator to remove the conflicting
  setting; the bridge reports the server name before starting Codex. Chrome-devtools MCPs are disabled
  by default (including aliases identified by command/args). An explicitly browser-required
  launch may select ONE configured server with bridge `--browser-server NAME` or a
  launch-scoped `COMMANDER_CODEX_BROWSER_SERVER=NAME`; every other browser MCP stays
  disabled. Unknown/disabled selections and managed `enabled=true` conflicts refuse
  before starting Codex. Overrides never rewrite config or credentials. This affects
  new launches only; it does not stop existing MCPs. Tests: `TestCodexMCPLaunchPolicy`,
  eval `TestCodexMCPLaunchScenario` (`scenarios/codex-mcp/launch.json`).
  Trust discovery retains raw cwd ancestors and tries canonical then raw keys for
  directory/project/repository trust (Codex 0.164). Keep unresolved fixture paths:
  run targeted MCP tests/evals with normal and symlinked TMPDIR. Chat test helpers
  inject empty system/managed paths; child eval binaries link synthetic paths.
  See `docs/testing/codex-mcp-launch-policy.md` for coverage and residual alias
  override parsing limitations.
  Host Operators on Codex run `cmdr __codex-bridge --prompt ...
  --hooks-role R --guard-only` one-shot (approval never, danger-full-access; build-slot guard, no mail).
  **Claude by default:** honor the user's explicit runtime choice for Managers and Operators.
  Agents may carry out that choice with `cmdr runtime <role> codex`,
  `internal-spawn-operator --runtime codex`, `internal-operator-provider --runtime codex`,
  or an authorized rebalance; these commands do not refuse agent callers.
  Commander uses a preferred account of either provider and automatically falls back to any connected account with usage left (supersedes d38).
  The share env overrides count only in a process with no agent role (`agentrt.share`), so an agent cannot draw
  Codex by prefixing its own command; `testguard.Isolate` unsets both role variables so a suite an Operator runs
  is nobody's agent.
  **Runtime shares** (`internal/chat/runtime_share.go`): `[runtime] codex_operator_share` and
  `codex_manager_share` (0-1, default 0 each, so a missing key draws nobody; existing values still apply; env
  `COMMANDER_CODEX_OPERATOR_SHARE` / `COMMANDER_CODEX_MANAGER_SHARE`; test mains pin them to 0). Draws are
  `chat.CodexByShare` (FNV of the id < share) and go to Codex only while `chat.CodexHooksReady`
  (`codex features list` shows hooks on).
  **Pins:** Operators require an explicit <role>.runtime-pin containing the chosen runtime. The .runtime file always records the effective run; a missing-hooks fallback records Claude with handoff and reason claude-hooks-unavailable, retaining Codex intent for recovery. Legacy markerless Operator files are ambiguous and treated as automatic. For each role whose explicit choice the user knows, reassert it with `cmdr runtime <role> codex` (or claude) while idle; never bulk-migrate inferred choices or overwrite real roles automatically. Tests: TestOperatorLegacyRuntimeIsNotExplicit, TestOperatorFallbackHandoffAndRecovery, TestMailPinnedCodexFallbackUsesClaudeClassification. Manager/chat compatibility: a runtime file without `<role>.runtime-auto` is a person's choice (`WriteRuntime`, used by
  `cmdr runtime <role>` and the menu, drops the marker; the Managers switched by hand before shares had
  none, so they are pinned); `WriteAutoRuntime` writes a share's choice. A drawn (auto) Codex Manager goes
  back to Claude at its next start while the Manager share is 0 (`dropDrawnCodex`, in `Engine.Start` only when
  no surviving host is adopted, with the handoff; an adopted Codex host keeps running as Codex); a pin stays, and never passes to the Manager's Operators. Operators
  (`spawn.resolveOperatorRuntime`): explicit `--runtime` (including the hooks readiness check), else an account-only request selects effective Claude without changing intent, else its explicit pin on respawn (Codex requires ready hooks; a temporary hooks failure runs
  Claude while retaining the pin for a later restart), else Claude on a respawn (a drawn Codex run
  moves with a `cmdrd:` comment on the bead, `Spawner.operatorRuntime`), else Claude when the task
  title/brief (`SpawnRequest.TaskText`, set by `internal-spawn-operator`) names a LOCAL background service
  (`needsBackgroundService`: whole words podman, docker, vfkit, gvproxy, colima, lima, `machine start`,
  `dev server`, `docker compose`, case-insensitive; never daemon/VM/server/service; a Codex tool call tears
  down what it forked), else the draw on the task bead (new runs only), else Claude. The
  `operator-runtime` event's reason is then `claude-service-keyword`; `cmdr runtime report` counts those
  runs and `claude-hooks-unavailable` fallbacks under Claude in a "forced to claude" column. A never-started Manager draws on its workstream id
  in the chat engine's start (below). **Manager starts steer by usage (d82)** (`internal/chat/managerstart.go`): every
  engine start of a `manager-*` role that spawns a process (EnsureNative, Open/`internal-open-workstream`, mail via
  `EnsureRole`, stand-in parents, the app's Start, restarts; not an adopted host, not a terminal pane) runs
  `planManagerStart` then `steerManager`: a fresh Manager with no runtime file draws Codex only by share with hooks
  and Codex usage left; a Claude pin stays; a Codex pin runs on Claude while Codex is out or has no hooks, the pin kept in `<role>.runtime-fallback` (`ManagerPin`), and goes back once Codex can run it; an unpinned Codex Manager whose Codex is out runs on Claude, and one a share drew at share 0 leaves Codex only for a Claude account with usage (the steer replaces `dropDrawnCodex` for Managers, so an outage never flips it and drops its thread); a person's account choice (`WriteAccountRuntime`, app and `cmdr account use`) leaves an unpinned Manager unpinned; on
  Claude the account intent (`<role>.account-intent`) comes back once it has usage, else the current account, else
  `NextAccountWith` (intent kept, so a person's choice is never overwritten by a move); every Claude account out
  sends an unpinned Manager to Codex (usage + hooks), else the start fails with `chat.ErrNoUsage` naming why. A move
  publishes a `usage_limit_switch` notice and `Options.OnUsageMove` (cmdrd: `wsmgr.NoteOnWorkstream`, a `cmdrd:`
  comment on the workstream bead). A Manager at a usage limit (Claude or unpinned Codex) always moves
  (`moveManager`: restart through that steer, then a `[switch]` message telling it to re-read its handover and
  `cmdr internal-inbox`); `AutoSwitch` is true for Managers. Tests: `TestPlanManagerStart*`, `TestManagerStart*`,
  `TestManagerMovesAtUsageLimitAndReportsIt`, `TestManagerLeavesAnOutCodex`, `TestAccountChoiceKeepsTheManagerUnpinned`, `TestManagerAccountChoiceStillFallsBackToCodex`, `TestPlanManagerStartDrawnCodexStaysWhileClaudeIsOut`, `TestNoteOnWorkstreamCommentsTheBead`. `cmdr runtime rebalance [--dry-run]` moves
  idle, unpinned, running Managers through cmdrd (`POST /api/chat/<role>/runtime`, which refuses a
  turn in flight) until round(share x live Managers) run on Codex, never the Commander, and lists moves.
  Each host run writes an `operator-runtime` cost event (reason claude|codex) at its spawn time;
  `internal-ship` writes `operator-shipped` for the Operator whose worktree has the branch.
  `cmdr runtime report [--days N]` prints both shares, the live split (running Manager chats from cmdrd,
  running Operator runs) and a table per runtime: started, finished, died, respawned, median finish,
  shipped, tokens (watchdog totals, else counted from the latest run's `.fifo` output; legacy real fifos
  are skipped, opening one blocks).
  Native child agents share app-server notifications: the bridge filters explicit
  `threadId` values to its own thread and explicit `turnId` values to the active
  turn. Completion requires the active nonempty `turn.id` (duplicates ignored);
  a second turn cannot replace one in flight. Legacy item events without either
  identity remain accepted; unidentifiable child item text cannot be distinguished.
  Synthetic missing-thread/wrong-turn fixtures exercise the boundary, not recovered
  incident wire. Unit: `TestCodexBridgeChildNotificationsDoNotFinishParent`; eval
  `TestCodexChildCompletionScenario` runs the real `cmdr __codex-bridge --prompt` against
  `tests/evals/fakecodex` (a scripted app-server) replaying `scenarios/codex-child/*.json`
  and requires exactly one result, the parent's text and tokens, none of the child's.
  Hooks take effect on a chat's next (re)start with an installed cmdr. Tests fake Codex with the test
  binary (`COMMANDER_CODEX_BIN`, `maybeRunFakeCodex`); `codex_hook_input_test.go` has captured payloads.
- **Claude accounts (per role):** an account is a `CLAUDE_CONFIG_DIR` (`internal/claudeacct`): `default` = ~/.claude
  (no override), others `$COMMANDER_HOME/claude-accounts/<name>`. Claude Code keys OAuth by that dir (Keychain
  `Claude Code-credentials-<sha256(dir)[:8]>`, account $USER, or `<dir>/.credentials.json`); account dirs link
  settings/CLAUDE.md/commands/skills/agents/plugins/hooks from ~/.claude and copy `mcpServers` into their own
  `.claude.json`, never the login. `cmdr account list|add|use|auto`; `<home>/chat/<role>.account` (missing =
  default); `POST /api/chat/<role>/account {"account","continue"}` restarts a chat there (409 mid-turn). Every
  Claude start copies the session's newest transcript (any account) into the account's projects dir before
  `--resume` (`claudeacct.CarryTranscript`). Usage limit = rejected `rate_limit_event` or the CLI's `rate_limit`/
  `billing_error` assistant message: `Status.usage_limit` + notice `usage_limit`; `<role>.autoswitch` (always on
  for Managers, see "Manager starts steer by usage") moves to the next logged-in account with usage left. Per-account 5h/7d usage from rate_limit events (Codex
  `account/rateLimits/updated` via the bridge) in `chat/usage.json`; never poll Anthropic's usage endpoint.
  Operators: `internal-spawn-operator --account`, `internal-operator-provider <op> --account|--runtime`
  (respawn on the same bead/branch/worktree; a Manager, by `COMMANDER_ROLE`, only its own). App: chat ⋯
  "Switch to", the usage-limit notice, Settings (gear, bottom-left) > Agents / Usage / runtime split.
  **Spawns never start on an out provider** (`internal/spawn/usage.go`): every host spawn (`internal-spawn-operator`, `internal-operator-provider`,
  cmdrd crash/auto restarts) runs `resolveProvider` = runtime, account, then `steerByUsage` against `chat/usage.json` (`chat.ReadUsage`, what
  `cmdr account list` shows; no report = usage left). A request that NAMED the out runtime/account (`--runtime codex`, `--account X`) is refused
  naming a way out (`--runtime claude`, `--account <next>`, `--runtime codex` only with Codex hooks ready and no runtime named); both verbs check
  that first (`spawn.RequestedUsage`, no store needed), then `spawn.CheckUsage`: the runtime and account the spawn will really resolve to (role pin
  and account on a respawn, the Manager's account, a hooks fallback; `pickOperatorRuntime`/`pickOperatorAccount`, nothing written; a share draw not
  yet made passes when either side has usage), before a task bead is made or the old run is closed. Anything else moves: Codex out -> Claude (reason `claude-codex-out`, a "forced to claude" run in
  `cmdr runtime report`; a pin stays for later, like the hooks fallback), a Claude account out (or not logged in after that move) -> the next
  with usage by `chat.NextAccountWith` (= `Engine.NextAccount`'s rules, never a profile sharing an out login; else default when it has usage);
  `resolveProvider` only picks (`pickOperatorRuntime`/`pickOperatorAccount`, nothing written); `applyProvider` does every write in one step in both host spawns, once the spawn's own refusals (lock, branch, stop) are behind it and before the launcher: role runtime/pin, Codex thread removal, the "was on Codex" note, the account, then the move (`applyUsageMove`), so a refused spawn or crash restart leaves runtime, account and intent as they were:
  a `cmdrd:` comment on the task bead and `moved` (+ `runtime`, `account`) in the verbs' result JSON; an account move keeps the account it was
  headed for in `chat/<role>.account-intent` (`chat.WriteRunAccount`), which the next respawn tries first; any `WriteAccount` (a person's
  choice) removes it. Nothing with usage = refused
  (a crash restart then hands back after its retry, the error in the Manager's mail). Tests: `TestSteerByUsage*`, `TestResolveProvider*`,
  `TestRequestedUsage`, `TestSpawnCodeHost_CodexOutRunsClaude`, `TestOperatorProviderRefusesAnOutOfUsageProvider`,
  `TestSpawnOperator_UsageRefusalAndMove`, `TestOperatorProviderChecksTheAccountItResolvesTo`, `TestSpawnOperator_AllOutRefusedBeforeTheTask`,
  `TestCheckUsage`, `TestUsageMoveKeepsTheChosenAccount`, `TestRefusedSpawnRecordsNoUsageMove`, `TestSpawnRecordsTheUsageMove` (code + knowledge through `Spawn`), `TestRefusedRestartWritesNothing`, eval `TestSpawnUsageScenario` (built cmdr refusals) and `TestSpawnUsageMoveScenario` (production `Spawn`, real store/git, mocked tmux: moves recorded, `chat.ProviderOf` = result; both `scenarios/spawn-usage.json`). Takes effect after an install.
  **Default is always ~/.claude:** `default` means ~/.claude + ~/.claude.json whatever CLAUDE_CONFIG_DIR the process inherited (a cmdrd or tmux
  server started from a named account's shell has one). Every child clears it unless its account is named, and every reader agrees with the
  child: chat (`childEnv`, `ClaudeProjectsDir`), Operators (launcher), legacy knowledge run, inspector digest, recap, verify, login.
  **Terminal panes run on default:** `agentrt.ClaudeInteractiveCmd` (panes, pop-out) ignores the role's `.account`; the resume choice reads ~/.claude and `syncForeign` the same dir. `.account` is
  honoured by Engine starts and Operators only: a switch of a terminal-held role records the choice and moves nothing (`cmdr account use` says "did not move"; the status has `owned_by`; Settings lists Managers only while the engine runs them).
  **Carry at a real launch:** `claudeacct.CarryTranscript` copies the newest copy of the session (default, every account, this process's CLAUDE_CONFIG_DIR, passed dirs) into the target before a launch: Engine start, Operator
  respawn, and for panes (target ~/.claude) SoftRestart's restart branch, new-window paths (`Director.claudeLaunchCmd`, also passes the tmux server's value, `claudeacct.TmuxConfigDir`) and PopOut. Never inside `ClaudeInteractiveCmd`, which SoftRestart builds for panes it leaves running.
  A carry that fails leaves SoftRestart's pane running and fails a new window or PopOut. Limits: what a mid-turn pane writes between the carry and `respawn-pane -k` stays behind; an Engine start searches cmdrd's own value, not the tmux server's.
  `TestEveryClaudeLauncherIsAccounted` parses cmd/ and internal/ and fails on an unlisted launcher (a `"claude"` argument, `ClaudeBinary`, a builder, exec of a `*.Binary` field) or a Getenv/LookupEnv of the variable outside `configDirReaders` (the carry and the fake); `TestChatHostRunsUnderChildEnv` pins that the host's `h.cfg.Binary` runs under the Engine's `childEnv`. A name held in a plain variable and an `os.Environ` scan are out of its reach. The Docker Operator's env is an allow-list. `daemon.transcriptActiveAt` looks only under ~/.claude. Tests: `TestSoftRestart*`, `TestCarryTranscript*`, `TestPopOutOfANamedAccount*`, eval `TestAccountUseOnATerminalHeldRoleScenario`.
  **Same login:** logged-in profiles sharing an account UUID (email fallback) say "Same identity as another connected Claude profile" (no meters) and add no
  usage: `NextAccount` skips from, anything out, and every profile sharing a login with either. **Settings > Agents > Usage** is one Accounts list
  (`components/UsageTab.tsx`): each card is an identity (email, else account id; Codex's from its `auth.json` id_token email / `account_id`,
  `chat.codexIdentity`, display only) and its provider (Claude/Codex), never a profile name or an empty slot. A signed-out profile stays listed with
  Reconnect when cmdrd still knows its identity, agents are bound to it, or it was signed out, added or named here this session (so the Disconnect
  note stays). "Add or reconnect a Claude account" with an existing name lists that profile for sign-in and never calls add; "Connect Codex" lists the
  signed-out Codex account. `codex` is reserved (`claudeacct.Reserved`: `Create` refuses it, the sign-in routes are keyed by name); an older Claude
  profile named codex is shown without Connect/Disconnect. Tests `app/test/settings-accounts.test.tsx`, eval `TestAccountProviderIdentityScenario`.
  The sign-in hint: the route refuses a hint with a control
  character or leading dash (`ValidLoginEmail`), and `LoginCommand` quotes by allow-list. The hint does not enforce the resulting identity: a changed polled identity
  confirms connection, an unchanged one means check the shown email. `cmdr account verify <name>` runs read-only `claude auth status` and compares
  public identity with profile metadata: every field both sides know must agree, and at least one must be known (matching UUIDs with no email match).
  Tests fake the Keychain (`claudeacct.KeychainHas`); never log a real account in or out.
- **Opening the cockpit needs a person:** bare `cmdr`/`commander`, `--project`/`--task`, `root`/`projects`, `restart` and `reload` exit 2 with usage naming `cmdr --help`, before any tmux call or target lookup, when `COMMANDER_ROLE` or `COMMANDER_MANAGER_ROLE` is set or stdin or stdout is not a terminal (`refuseCockpit`, `cmd/cmdr/main.go`; an Operator's `cmdr 2>&1 | grep` once built a whole cockpit holding every stopped Manager). `COMMANDER_COCKPIT_NO_TERMINAL=1` waives only the terminal check, never a role; only `scripts/cockpit-test.sh` sets it. Commander's prompt uses `internal-workstreams` then `internal-open-workstream --focus`, tells the user to run `cmdr --task` themselves, and says what a `native:true` answer means (the chat runs, nothing moved on screen). `TestPromptsTeachNoRefusedCockpitCommand` runs each prompt line through `managerguard.OpensCockpit` (a line naming the user's `own terminal` is exempt); eval `TestCommanderShowMeScenario` runs the commands the prompt's table names, as Commander. `show-me-commander.json` only pins the refusal: its fake provider ignores the prompt. The Manager and Operator guards refuse the same commands first (`managerguard.OpensCockpit`; `go run ./cmd/cmdr` or a script gets past those, not past cmdr). `refresh` stays open: it never builds a missing session. Takes effect after an install. Tests: `TestCockpit*`, `TestOpensCockpit`, eval `TestBareCmdrScenario` (`scenarios/bare-cmdr/`, real cmdr, private sockets, real pty), `TestBareCmdrCockpitTestStillWorks`.
- **Start a Manager without tmux:** `cmdr internal-open-workstream --project P --bead B` (talks to cmdrd's chat API; no tmux session needed) or `POST /api/chat/<manager-role>/start`. Mail to a Manager that is not running (slept or exited, any history, not just a fresh one) starts it through `chatDeliverer.Deliver` -> `EnsureRole`; a failure now reads "native start of ... failed (terminal fallback: ...)" / "... could not be started natively", never only the tmux error. Tests: `ChatDeliverer*`, `TestOpen_NativeFailure*`.
- **Workstream Managers are native:** cmdrd (`Daemon.ensureManagers`, every 30s
  after `WaitResumed`) runs `wsmgr.EnsureNative`: Prepare + `Engine.Start` for each
  open workstream whose chat has no history (`Engine.NeverStarted`; a stopped one is
  not restarted behind the user's back). Mail to a non-running `manager-*` role starts it
  first (`chatDeliverer.managers`). `internal-open-workstream` asks cmdrd to start the
  chat (`cmd/cmdr/native_chats.go`) and makes no tmux window (`OpenResult.native`);
  only when cmdrd is down or a terminal holds the session does it open the window.
- **Nested Managers (per-project switch):** with `<project>/.commander/nested-managers` present (a file, not a state.db column, so every site that only has the project dir reads it), a bead's owner is the nearest open `kind:workstream` bead of its real lineage, self first (`beads.Owner`/`NearestOwner`, actions `wsmgr.StrictOwner`, authority `wsmgr.OwnerChain`); legacy `kind:track`, closed, parked and superseded ones own nothing, and nothing under one of those or under any closed bead owns either (`beads.ChainOwners`, the one rule that `Owners`/`NestedOwners` registration and livedata `Owns` read too, so every routed-to Manager can start; lists ordered by depth then bead id); switch off = the root, as before. Route to the nearest owner, authorize the nearest or any owner above it (+ Commander/user). Followed by: wsmgr (validate, `Owners`, Prepare preamble naming parent/children + a fresh mail cursor via `mailstate.StampFresh`, EnsureNative skips a parent whose single nested child stands in, Open, EnsureRole, OwnerOfRole/RoutedMail, `wsmgr.Owner` replaces `wsmgr.Root`), place/unregister, create/set/track `--workstream` (starts the new nested Manager, parent mailed), `spawn.ManagerRole`, spawn refuses a parent Manager under a nested workstream (`--as-parent`, recorded once the spawn ran) and any Manager outside the step's owner chain (switch on or off: a sibling's, or a nested one after `--undo`), the cap per owner (any owner's `max-operators`, the root's included, bounds its subtree; with no label the default bounds the root's subtree, so nesting never multiplies a tree's bound), operator-provider, `internal-mail` (an Operator's mail to a Manager of another bead of its task's lineage goes to the task's current owner, both ways, either spelling; the old one cc'd once per run after the main send, marker `mail-reroutes/<run>-<role>` written only once the copy went), settle/waits/live gates/proposals, livedata (`ObjectiveRow.Owns`, `Data.OwnerOf`/`ManagerOf`: watch, invariant, pitch, reported gap, Manager waits, mail roles), health `plan:<nested role>`, approval custody, gate publish/unlink/retire-legacy, publication repair, restore-approved-context, bind-writers, addenda, move-card, `--max-operators`; snapshot steps and board tracks carry `manager_role`, a stand-in row `scope_role`; `cmdr board --role manager-<nested>` shows that subtree; the app selects a nested Manager's own chat (`managerOfRole`). Track status lines are unchanged. `TestEveryRootResolverIsAccounted` fails on any new `beads.Root`/`wsmgr.StrictRoot` call not listed with why it means the top. Cutover: `cmdr internal-nest-managers --project P` (dry run: plan per owner) / `--apply` (register + cursor BEFORE the flip under `beads.AssumeNestedManagers`, flip, start with a handover mail, tell the root; no inbox moved or marked read) / `--apply --undo` (flips back, mails each root only its own nested Managers' unread mail and each nested Manager to stop dispatching; registrations kept); a rerun of either after it finished mails nobody again; the switch file is written atomically and an unreadable one reads as off (logged); Commander/user only. Recorded addenda stay attributable after undo (`recordedOwnerRoles`); gate unlink and publication repair also hold a nested Manager to its own subtree. Not built yet: the app's agents-tier indent and deep link, nested holds quieting only their subtree. Design `docs/plans/2026-11-09-nested-managers.md`. Tests: `TestOwner_*`, `TestStrictOwner_*`, wsmgr `nested_test.go`, livedata `nested_managers_test.go`, golden `testdata/board/nested-managers.json`, `app/test/nested-managers.test.tsx`, evals `TestNestedManagerMailScenario`, `TestNestedManagersCutoverScenario` (run with `-count=20`: the owner order once flaked).
- **A sidebar move follows ownership** (`cmdr internal-place`, `settleOwnership`): a workstream reaching the top is registered and started (no hands-off check: `hold` only stops shipping); one going under another is unregistered, session kept. Mail cannot start a role with no registration (`EnsureRole` -> `ErrInvalidRoute`, kept unread, `mail:<role>` health), so its old Manager is sent the notice only when its chat runs, a terminal holds it or cmdrd cannot say (`formerManagerListens`, read before the row goes, 5s; a failed read, an answer not naming the role, or `Status.owner_unknown`: the role did not resolve or the process scan failed; a Manager with no registration or session file is stopped, a cmdrd older than this sends no `owner_unknown`); the new owner always is. Limits: sent is not delivered. Delivery checks `EnsureRole` first (`chatDeliverer.Deliver`), so a running idle or terminal-held former Manager is refused too: the notice stays unread with the `mail:<role>` alert until its next turn's hook shows it (from the code, not run; the same before this rule). A Manager that stops between that read and delivery holds the notice, as does one cmdrd could not read. Clear with `cmdr internal-inbox --agent R --mark-read`. Tests: `TestSettleOwnership_*`, eval `TestMovedBackManagerScenario`.
- **Host Operators carry `COMMANDER_ROLE`:** `spawn.hostOperatorCommand` sets it to the AgentName (dotted, as stored) on both runtimes and every path through the launcher (fresh, knowledge, crash restart, whatever the spawner's own role), beside `COMMANDER_MANAGER_ROLE`; the launcher's explicit value wins over an inherited one. Every role check therefore applies to them: refused are install, worktrees gc, proposals accept/overrule, `--push-reset`, decisions add, park/unpark, hold, `--card-check`; the door checks and `checkAskNotSettled`/standing decisions judge their writes; `--no-notify` no longer skips a card. Not refused: internal-comment/dep/label/update/create `--operational`, `internal-ship` and an Operator closing its own task or a step under it (`closeDoorRole`: the `owner:` label on the bead or an ancestor; knowledge Operators close by design). Another bead's close goes through the close door. **Inherited role:** `testguard.IsolateTmux` drops `COMMANDER_ROLE` and `COMMANDER_MANAGER_ROLE` from test binaries and `ship.runCommand` drops `COMMANDER_ROLE` from its checks: in an invented inherited-role exercise, an Operator's `go test` would otherwise judge each test call as that agent. Takes effect for Operators spawned after an install; running ones keep their environment. Speed bump as ever: `env -u COMMANDER_ROLE` gets round it. Tests: `TestSpawnHost_OperatorEnvironmentCarriesItsRole` (real launcher, private tmux), `TestOperatorRole_*`, `TestCloseDoor_Operator*`.
- **Mail to host Operators needs no tmux:** `wake.TmuxDeliverer` returns `wake.ErrHostOperator` (wraps `ErrDeferred`) for a running Operator whose run row has a session name (`TmuxPaneID`): no cockpit window is opened, nothing is typed, no "commander tmux session not running". **Claude host Operators take mail by hook:** `agentrt.ClaudeHostOperatorSettings` adds `cmdr internal-mail-hook --mail` on PostToolUse (no matcher: every tool) and Stop, beside the unchanged guard: mail only (no board, no agent state), as context after a tool, as a block at Stop, so a Manager's direction arrives at the next tool call (mid long tool call: when it returns). Idle hooks avoid a mail-server read (`NeedsCheck`); an invented no-mail exercise should make no inbox request. A role matches its run by `AgentName` in either spelling (dots as dashes, as `receives` reads it), or by flattened task id for old rows; in an invented task-id-only matching regression, a running Operator reads as finished and the Waker marks its mail read before the hook sees it (`TestPoll_RealShapedRunning*`, eval `TestOperatorMailHookAndWakerScenario`: real Waker + real hook binary + fake am server). An Operator's mail does not age out of `mailstate` during its run, or a tool call past `FirstSightWindow` would hide it. **Bound on a respawn:** a fresh spawn stamps `mail-cursors/<role>.run-start` (`MarkRunStart`, before the launcher runs); a same-name Operator with no cursor counts mail older than that stamp less `FirstSightWindow` as its earlier runs' (`historyCutoff`; answers excepted): the hook gives one `HistoryNote` line (even with nothing new; an Operator's names `cmdr internal-inbox`, taught in `prompts/operator-code.md`, and does not say "count as read") and its cursor passes them, the Waker marks them seen with no report. A `Resume` spawn (crash restart, account or runtime respawn) keeps the stamp of the run it continues, so its own unseen mail reads in full and stays reportable (`TestSpawnHost_ResumeKeepsTheRunStart*`). No stamp (a run started before this) = no bound. A failed hook read stamps `<role>.read-failed` and `NeedsCheck` stays false for `ReadBackoff` (1 min), leaving `.checked` alone. **Mail text:** the hook quotes every body line `> ` (all line-break spellings, so a body cannot imitate a header; a blank body line is `>`, so an empty line ends a body and the app's `mail.ts` drops the trailing agent text) and flattens the sender onto the header line, and states under the banner that mail itself grants no authority, which comes from the brief, an answered card or a closed gate on the bead; a `[decision]` mail reports such an answer and the bead confirms it (`mailAuthorityLine`, for every role; never exempted by subject, which the sender chooses; `AUTHORITY` in `mail.ts` follows it; `prompts/lead.md` and `commander.md` teach it); `prompts/operator-code.md` says how to treat mid-run mail. `mail.ts` shows a `HistoryNote` as no mail. Invented verification exercise: use a fake provider and fake am to check the mail hook; this example is not execution evidence. Takes effect on an Operator's next (re)start after an install; running ones keep their settings. In an invented batch, two running Operators receive mail mid-tool; one has no cursor. Both must see it through the hook. Codex one-shots stay guard-only: unreachable, as `internal-mail` says. **The Waker on `ErrHostOperator`:** `pending` cleared, so only the 2-minute sweep reads; no signal unless `NeedsCheck` is false (mail with no live signal: an outside sender, or a cursor write that failed after the hook's read), then one, with `fetchedAt` moved past it so it does not make the role due; mail unread past `wake.HostMailReportAfter` (20 min) is reported through `OnDeliver` (health `mail:<role>`, also a Codex Operator's or a pre-hook one's) until a cursor passes it or the run ends (`forget` clears it). A retry window ends early once the cursor shows the failed batch read. Limits: the health board is memory, so a cmdrd restart can raise one more alert for mail still unread (a Codex Operator's, as the merged `mail-history` count); after a restart a cursorless Claude Operator's mail older than `replayWindow` reads "nobody was shown" until its hook takes it. Tests: `TestTmuxDeliverer_HostOperatorMailDefersToTheMailHook`, `TestPoll_HostOperatorMailOnly*`, `TestHostOperatorHookDeliversMailOnly`, `TestHostOperatorSettingsCarryTheMailHook`, `TestSpawnHost_LauncherCarriesTheMailHook` (real launcher, private tmux).
- **Commander interrupts are labelled:** Claude records every interrupt as the user's (the cut tool call's result "The user doesn't want to proceed with this tool use ... STOP ... wait for the user", then "[Request interrupted by user for tool use]"); Managers once read that as the user saying stop and held. `internal-mail --interrupt` posts `{"by":<sender>}` to `/api/chat/<role>/interrupt`; with `by` the API calls `Engine.InterruptForMail`: only mid-turn, it interrupts and at once sends `chat.InterruptNote` (origin mail, one line, sender reduced to name characters) saying who it was, that it is not the user and not a hold, and to read the mail and do what it directs (resume only if it does not redirect). No `by` (the app's stop button: the user) stays a plain interrupt with no note. When the turn ended by the SIGINT fallback (no ack; `Running` stays true until the exit is seen) the dying process gets nothing: the note is kept in `chat/<role>.interrupt-note` (written before the interrupt, so a start racing the exit finds it) and sent as the first message of the role's next spawned process (`sendPendingInterruptNote`, once, taken by rename, kept on a failed send, dropped after 1h; an adopted host gets none). That fallback is an expected stop (`process.noteStop`), not a crash: a role that restarts on a crash (commander, manager-*) is started again at once by `restartInterrupted` (no crash counted, no `crash_restart` notice); others stay stopped until their next start. A start that sent a note reports it (`start`'s `noted`), so `restartCrashed` and `resumeRole` send no `[resume]` message after it. The prompt rule covers only an interrupt (the "doesn't want to proceed" result): guard/hook refusals and disallowed tools are never the user's Stop. `prompts/lead.md` and `commander.md` key the rule on the note: an interruption followed by `[commander interrupt]` is not a hold, one with no note is the user's own Stop and is a hold (never "no user text is not a hold": the user's Stop has no text either). Tests: `TestInterruptForMailIsLabelledNeverTheUser`, `TestUserInterruptSendsNoNote`, `TestInterruptForMailOnTheSIGINTFallbackNotesTheNextProcess`, `TestStaleKeptInterruptNoteIsDropped`, `TestMailInterruptSIGINTRestartSendsOnlyTheNote`, `TestCrashRestartWithAKeptNoteSendsNoResumeMessage`, `TestChatAPIInterruptLabelsOnlyAnAgentsInterrupt`, `TestInterruptRolesPostsToChatInterrupt`, `TestToolRejectionIsNotAHoldInPrompts`. Takes effect after an install + cmdrd restart, and for prompts on a role's next (re)start.
- **Stopping a Codex Operator:** a one-shot host Operator on Codex (`__codex-bridge --prompt ... --guard-only`) has no mail hook and takes no message after its prompt, and Codex hooks fire only at a turn's start and end, so mail never reaches it. `internal-mail` says so (`warning:` on stderr, `unreachable` in the result JSON). `--interrupt` ends the run instead (`splitCodexOperators`, `spawn.StopHostRun`; never from an `operator-*` sender): a marker `$COMMANDER_HOME/operator-stops/<session>` naming who, then the pane is killed (exact targets `tmuxctl.ExactSession`/`ExactPane` = `=name`/`=name:`: a bare name prefix-matches a sibling such as `op-x-11` once `op-x-1` is gone; test `TestStopHostRun_EndsOnlyTheExactSession` on a private `-S` server via `tmuxctl.NewOnSocket`). The Operator ban (`callerIsOperator`): `COMMANDER_ROLE` or `--from` says operator, or `COMMANDER_MANAGER_ROLE` is set with no `COMMANDER_ROLE` (an Operator started before its launcher set `COMMANDER_ROLE`; a Manager's chat always has a role and may inherit the other through a cmdrd started from an Operator's shell; `/api/cmd` strips both from the cmdr child). Speed bump: an Operator can set the variables itself. Its hangup makes the bridge send `turn/interrupt`, wait up to 3s (`codexStopGrace`) and SIGKILL Codex's process group (Codex runs with `Setpgid`, so the tool it runs goes too). The result JSON has `stopped`, or `unreachable` plus a `NOT stopped` warning. cmdrd's next sweep reads the marker before the exit marker: `agent_runs` status `stopped`, `HostReapResult.Stopped`, `runSettler.stoppedOnRequest` = no crash restart, bead open with no owner, `operator-died`, Manager mailed. The mail stays unread, so a respawn needs the direction in its brief. A spawn on the stopped run's branch or under its name is refused only until the sweep closes its row (`refuseUnsweptStop`: marker + running row; under the old name both rows would share a session, under a new one a Codex tool may still be writing); a marker whose run was closed another way refuses nothing. **The freeze checks for an owner, no hold:** `spawn.FreezeWorktree` (every settle's freeze) locks a closed run's worktree only under a flock claim (`$COMMANDER_HOME/worktree-claims/<hash of the real path>.lock`) and only when no other running run owns it, by `agent_runs.worktree_path` (migration 0020; NULL = `.worktrees/<its name>`, `spawn.RunWorktree`); a claim it cannot get (a spawn is taking over) or a read it cannot make freezes nothing. A spawn holds the claim from before it clears the lock until its row is written (`claimWorktree`, waits up to 2 min for a freeze in progress), so neither order leaves the lock on. A stop's settle also hands nothing back when another running run holds the bead (same owner label under a same-name respawn). So a dropped or crashed settle refuses nothing; it only leaves the worktree unfrozen. A retried `operator-died` label cannot land on a same-name respawn on a code project (a follow-up gets a new bead and name); the one route left is a scheduler auto-spawn on a knowledge project with `auto_spawn=true` while a label write fails, and the worst is a stray label and a false "open again" mail for up to 30 minutes. **Only real Operator worktrees freeze:** the lock is a recursive chmod, so `FreezeWorktree` refuses any path that is not a direct child of `<project>/.worktrees` or `.worktrees.noindex`, compared as real paths (`isOperatorWorktree`), and a host spawn refuses a `--branch` checked out outside those folders (the main checkout's branch included) instead of taking that checkout over. `internal-operator-provider` refuses a stop no sweep has closed (`spawn.StopPending`), since closing the row itself would lose the settle. Not covered: Claude host Operators (`--interrupt` only warns, as before), and a message typed in the app (reads "delivered by mail"). Tests: `TestCodexBridge{Hangup,PaneKill}EndsCodexAndItsTool`, `TestStopHostRun_*`, `TestStoppedRunIsNotRestarted`, `TestMail*Codex*`, `TestMailInterrupt*`, `TestSpawnAfterStop*`, `TestStopThenSpawnThenSettleFreezesNothing`, `TestFreeze*`, `TestSpawnOnABranchCheckedOutOutside*`, `TestWorktreeClaim*`, `TestStoppedSettle*`, `TestHostOperatorEnvironment`.
- **Operator role spelling:** an Operator registers in am under its stored (dotted) AgentName; `internal-spawn-operator` prints it as `mail_role` (+ dashed `mail_role_alias`). `rolemail.Registered`/`Resolve` match any spelling of an `operator-*` role to the registered one (lists am_agents on an exact miss), never auto-register a second agent for it, and `Send` signals the registered spelling. Tests: `OperatorRole*`.
  **Existing external AM identity:** `internal-mail --to-existing-agent Name` is exclusive with `--to`/`--interrupt`; Managers, Commander and the user only, an agent only as itself (`existingAgentCaller`, before `mailDeps`; a speed bump, like every role check); it requires an already bound sender, exact active server `whois` identities in the fleet project with matching positive project IDs, and no recipient collision with local roles/AM names. No registration, binding writes or wake/process actions; `SendOptions.NoAutoContact` sends `auto_contact_if_blocked:false`, so the server cannot approve a contact (role mail omits it). `sent:true` is API success, not receipt. See `docs/agent-mail-routing.md`; unit `TestMailExistingAgent`, eval `TestExistingAgentMailScenario`.
  **Registration = am server under the repo path:** every Spawner (fresh `internal-spawn-operator`, `internal-operator-provider`,
  and cmdrd's: crash restarts in `settle.go`, scheduler, handoff) registers through `agentmail.ServerRegistrar` (`Key` = absolute
  commander repo path): a role already in `am_agents` is reused (a respawn keeps its registration), else EnsureProject +
  RegisterAgent under the path. The `am` CLI's bare project name (e.g. "commander") is no am project, so registering through it
  fails at every cmdrd restart; always pass the absolute path. cmdrd falls back to the CLI only without an am token (`daemon.spawnerMail`). Tests:
  `ServerRegistrar*`, `SpawnerMail*`.
- **Host pane environment:** command panes and respawns use `env -u TMUX -u TMUX_PANE`
  before the pane shell starts; host launchers unset both again and strip them from
  the agent command. tmux synthesizes these variables even when session environment
  entries are removed. Launcher cleanup pins the selected Operator socket explicitly.
  This protects new Operators after install; existing processes retain their environment.
  Regression: `TestPaneEnvironmentPrivateSocket` uses only an explicit private `-S`
  socket, poisons the session environment, and checks the respawned shell environment.
- **`pane_start_command` is quoted on tmux 3.2+:** `args_escape` wraps a command with a space (or `#';${}`) in double quotes with vis(3) escapes; 3.1 and older return it raw (checked in the source of 2.9-3.6). Ubuntu's 3.4 prints `$NAME` as `\\$NAME`, the source says `\$NAME`. Compare a start command only after `layout.plainStartCommand` (`listSessionPanes` applies it, so `SoftRestart`'s change check and its native-placeholder check see the launched string); plain `strings.Contains` readers (`relayout.go`, `layout.go`, `chat/handover.go`) do not need it. Not handled: tmux replaces non-ASCII with `_` for a client without a UTF-8 locale, so such a command reads as changed. Tests: `TestPlainStartCommand*`, `TestSoftRestart_*Quoted*`, `TestSoftRestart_AgainstRealTmuxStartCommands` (private socket, only `list-panes` reaches tmux).
**Operator -> Manager mail:** the host Operator's bootstrap prompt and env carry
  the exact Manager role (`spawn.ManagerRole`: `manager-<workstream root of the
  task bead>`, env `COMMANDER_MANAGER_ROLE`). Safety net in `internal-mail`
  (`rerouteOperatorMail`): mail from `operator-*` to `commander` is rerouted to
  that Manager (looked up via agent_runs); only `--importance urgent` also copies
  Commander.
- **Workstream ownership = real parents:** which workstream (and Manager) owns a
  bead, and which ancestors' gates hold it, is `beads.Lineage`/`beads.Root`: the
  parent-child edges up to the first bead with none, never the dotted id (a step
  promoted to a workstream, or moved under another bead, keeps its old id).
  Loaded data (`livedata.Data.Parents`, the snapshot's watch) costs no br call; a
  client read is one batched `br show` for the bead and its dotted prefixes (+1
  per ancestor outside them). `beads.ParentOf` is only the dotted-id fallback
  when parents cannot be read. Tests: `beads.TestLineage_*`.
- **Live state.db is migration-safe** (`internal/store/guard.go`): a db older than the
  build migrates only from an installed binary (the exe resolves to a
  `~/.local/bin/{cmdr,cmdrd,commander}` link) or with `COMMANDER_ALLOW_MIGRATE=1`;
  any other build gets "refusing to migrate the live db from a non-installed build".
  The guard covers only the real `~/.commander/state.db`; temp/private
  `COMMANDER_HOME` dbs migrate freely, and test binaries can never open the live db.
  Test packages that touch the store call `testguard.Isolate()` in `TestMain`
  (unset `COMMANDER_HOME` becomes a temp dir; the real home exits).
  **Degraded state:** when cmdrd cannot read state (`Source.LastLoadErr`, or a store
  error in the watch), the Commander check-in is not sent as "all on track": cmdrd
  mails Commander urgent via the am HTTP API using `$COMMANDER_HOME/mail-route.json`
  (cached am names, no db), writes `degraded.json`, and the snapshot carries
  `degraded:{reason}` (red banner atop the app navigator). Exception: `noLoadFor`, no load
  finishing (see Spent load), raises the mail and `degraded.json` but no banner, since no
  snapshot is published meanwhile.
- **No process outlives its Operator:** a host Operator's pane output goes through
  `tmux pipe-pane` (`exec cat >> <operators>/<flat>.fifo`) into a REGULAR file kept
  under the `.fifo` name (`spawn.createPaneOutput`). Never make it a real fifo again:
  in an invented pipe exercise, no reader opens it, so each writer would block in open()
  and remain under launchd (tmux never kills a pipe writer; it only ends on EOF when the pane closes).
  Safety net: cmdrd's minute loop runs `spawn.ReapPipeWriters` (writers of a
  registered project with no running agent_runs row, older than 2 min). Tests:
  `tmuxctl.TestPipePaneWriterEndsWithPane`, `spawn.TestSpawnHost_LeavesNoProcessWhenOperatorExits`,
  `spawn.TestReapPipeWriters_*`. **Tests that start processes** call
  `testguard.NoLeakedProcesses(t, <temp dir>)` first (fails and kills whatever still
  has that path in its command line after cleanup). `testguard.Isolate`/`IsolateTmux`
  also stop a test binary started by another one (in an invented recursive-child exercise, code under test exec'ing
  `os.Executable()` would start another suite at each level) and bound processes
  (Linux: a verified cgroup v2 pids.max <= 256, else re-exec under `systemd-run --user --scope`, RLIMIT_NPROC unchanged;
  macOS: lower RLIMIT_NPROC to leave 1000 process slots free). That re-exec exits, so call them from `TestMain` before `m.Run`, never inside a test
  (`os.Exit(0)` there panics; `internal/testguard` has its own `TestMain` for its tests that do). Their cleanup kills the private
  default AND `commander-ops` servers, by absolute socket. tui re-execs cmdr through `selfExe()`, which
  refuses under `testing.Testing()`. The engine is capped too: the launchd plists set
  `NumberOfProcesses` (per-UID RLIMIT_NPROC, inherited): cmdrd 1500, tmux keeper 2500
  (systemd: `TasksMax` only, per cgroup; never `LimitNPROC`, which on Linux counts every
  thread the user owns, so a desktop session alone starves cmdrd: `pthread_create` EAGAIN,
  SIGABRT, `br`/`tmux` fork failures, DEGRADED); test `install/install_test.go`. Caps take effect
  after `cmdr install` + a unit reload.
- **Operator tmux server is separate from cmdrd:** every Operator session is
  created on the tmux socket `commander-ops` (`tmux -L commander-ops`,
  `tmuxctl.OpsSocket`; `$COMMANDER_TMUX_SOCKET` overrides, `default` = old
  behaviour). A keeper agent (`install/launchd/org.commander-tmux.plist`,
  `install/systemd/commander-tmux.service`, running `tmux -L commander-ops -D`) owns
  that server, so a cmdrd restart/reinstall can never kill Operators; the user's
  cockpit stays on the default socket. `install.sh` loads the keeper BEFORE
  bootstrapping cmdrd and never boots it out when already loaded. Lookups by
  session name (`tmuxctl` SessionExists/KillPane/SendKeys/PipePane/Respawn,
  `RunOnSession`, `ListPanes` for the reaper) try the ops socket, then the default
  server (Operators started before the move). Attach with
  `tmux -L commander-ops attach -t <session>`. The first install that adds the
  keeper still ends Operators on the default socket if cmdrd's old job lacked
  AbandonProcessGroup: do it while none run. Test:
  `tmuxctl.TestOperatorSessionSurvivesParentGroupKill` (private tmux server).
  Routing invariant: every call site that addresses an Operator session goes
  through tmuxctl (spawn, reaper `ListPanes`, ceilings `RunOnSession`); the launcher's
  own `tmux kill-session` runs inside the pane, so `$TMUX` picks the ops server. The
  rest is the user's cockpit on the default socket by design: layout, livedata
  `ScrapeAgentStates` (session `commander`), wake (`op-<flat>` cockpit windows), tui
  columns. Host Operators are headless `claude -p`, woken by the mail hook, never by
  send-keys. A new tmux call on an Operator session must use tmuxctl, never bare `tmux`.
  Test: `tmuxctl.TestOperatorSessionOnOpsSocketIsReachable`.
- **Live-testing while the user is attached:** `cmdr` hard-codes the `commander`
  session, so run it on a separate tmux server with `TMUX_TMPDIR=$(mktemp -d
  /tmp/cl.XXXX)` (keep it short: socket paths over ~104 chars fail) plus a temp
  `COMMANDER_HOME`. **`/tmp/cl.*` and `/tmp/tk.*` may belong to running tests: never
  glob them away (`rm`/`rmdir /tmp/cl.*`); remove only the one directory you made, by
  its exact path.** With `$TMUX_TMPDIR` gone, tmux falls back to `/tmp` for `-L` and bare
  calls and can reach the real servers. In an invented missing-directory exercise,
  a cleanup would then address unrelated Operators. So a
  test's tmux cleanup kills by absolute socket (`testguard.KillTmuxServer(dir, name)`,
  `tmux -S`, no fallback), and `IsolateTmux` creates `<dir>/tmux-<uid>` at setup. Not
  covered: other `-L` calls in a binary whose dir was removed with `rm -rf`. Test:
  `testguard.TestIsolateTmuxCleanupNeverFallsBack`.

**Spawn validates `--step`:** `internal-spawn-operator` reads the step first (before deriving a Manager or any mail): `beads.ErrNotFound` or a nil bead (e.g. the literal id `null` from a failed jq) exits 1 with "does not name a readable bead", no mail; any other read error (busy, timed out) goes through the normal failure path (20s retry, then mail). Tests: `TestSpawnOperator_MissingStepNeverMailsOrCreates`, `TestSpawnOperator_BusyStepReadRetriesAndMails`, eval `TestSpawnMissingStepScenario`.

**Beads CLI is bd:** `beads.Binary()` defaults to `bd` (`$COMMANDER_BEADS_BIN` overrides); prompts, the Operator bootstrap (`bd --db <root>/.beads/beads.db`) and graph check/settle call bd directly. Test mains (`internal/beads`, `tests/evals`) pin `COMMANDER_BEADS_BIN=br` where scenarios assume br argv. The `~/.local/bin/br` stand-in (not in the repo) now also routes by `--db`/`-C`/`BEADS_DIR`/`BEADS_DB` to bd for any project with `.beads/dolt`; it is kept only for old callers.

**Beads state is not tracked:** `.beads/` and `.commander/` are in `.gitignore` (per-machine tracker state, no longer in git; a fresh clone runs `br init`). `internal-ship` still ignores `.beads` in its dirty check (harmless).

**Gates:** checks a stage (step) bead must pass before it closes. Every gate is
a bead (`internal/gates`): labels `kind:gate`, `gate-of:<stage>`, `gate-name:<n>`,
`verifier:<v>` (+ `approver:<who>` for user/reviewer gates, `gate-status:failed`
after a failed check), description = the check, closed = passed, evidence = the
latest `gate <status> by <who> at <unix>: <text>` comment. New gate beads are
siblings of their guarded stage (same real parent), avoiding a cycle when the
stage waits on its gate. A root-stage check has no parent. They carry no
`kind:objective`. Published approval cards with `gate`, or `kind:gate` plus
an approver, appear as steps without changing their stored labels. Held cards,
held/invalid/unloaded ancestry, proposed and parentless cards are excluded.
**Publish:** `cmdr internal-gate publish --project P --bead G --parent S
[--dry-run]` repairs only the real parent edge. Fresh reads must show that
G's existing `gate-of` stage has parent S, and that S has valid open objective
ancestry. Different existing parents, holds and cycles refuse. Only the user,
Commander or owning workstream Manager may run it. Dry-run validates the same
evidence without writing. IDs, pitch, labels, approvers, scope, status, proposals
and waits are preserved; publication does not approve a proposal or release a hold.
Tests: `TestPublication*`, `TestApprovalPublicationEligibility`,
`TestGatePublicationOwnership`, eval `TestApprovalPublicationScenario`.
`cmdr internal-gate add|apply|check|pass|list|rm|link|unlink|convert|publish|migrate`
(`cmd/cmdr/internal_gate.go`) are wrappers over them (verifier `auto` = shell
command run in the project root, `reviewer:<login>` / `user` = a person, whose
open gates get a matching `waiting:` record on `check`); redefining a gate
tombstones its bead and makes a new one. **Unlink** (`internal_gate_unlink.go`): `internal-gate unlink --project P
--bead <gate> --waiter <ids> --reason R` removes only "superseded step waits on gate" edges (internal-dep refuses gate
edges, link only adds): fresh reads, open gate left unapproved, each waiter superseded/retired (`beads.Superseded`;
`Show` now carries `close_reason` from the JSON form), not a gate/track, same workstream (`wsmgr.StrictRoot`;
a parentless `gate-of:` gate by its stage), not a plugged block; every waiter judged before any write; a note on the gate. Idempotent: a waiter whose edge is
gone is `already_removed` (exit 0, nothing written) only when an earlier `gate unlink by` note on the gate names it
(`unlinkNoted`); any other missing edge (a mistyped id) refuses before any write. So after a partial RemoveDep failure
(the note records what was removed) the same command finishes the job; a failed note prints the `internal-comment` command
that writes it. Reopening an unlinked step does not restore its wait; link it again with `cmdr internal-gate link`.
Owning Manager, Commander or user only; another Manager gets the exact command, Operators are refused. Tests
`TestGateUnlink*`, eval `TestGateUnlinkScenario` (`scenarios/gate-unlink.json`, scripted bd). The old state.db `gates` table is
read-only legacy: any verb (and `internal-gate migrate`, idempotent) moves a
project's rows into beads and drops them; livedata still shows unmigrated rows.
`internal-close-objective` and `internal-resolve-needs --action approve` refuse
until every gate is passed with evidence. `internal-close-objective` judges what a close
leaves on FRESH reads (`checkCloseLeavesWorkHeld` over `holding.NewlyUnheld`, the one predicate; never cmdrd's
snapshot). `--then-waiting`/`--then-owner`/`--then-decision` [`--then-step <id>`] name the next holder, written by
`internal-set-objective` BEFORE the close (`closingBead`). Never refused (custody instead, see Close/create custody):
no `COMMANDER_ROLE` (the user's own close, the app's Complete button), an Operator closing its own task or a step
under it, and a Manager over the step (any owner in `wsmgr.OwnerChain`); an ancestor the close empties always gets
custody. Any other agent is refused naming the `--then-*` flags. The close itself carries the door intent (M3,
below). Unreadable closure = not judged. Tests: `TestCloseDoor_*`,
`TestGateCloseWithItsNextHolderNeverReadsStateMissing`. Snapshot: step `gates[]` (with
`bead_id`), step/workstream `no_gates`, workstream `gate_nodes[]` (kind `gate`,
`blocked_by` = [stage], `blocks` = the steps waiting on that stage).
Build stages run `spec` > `pr-ready` > `reviewed` > `pr-approved` > `merged`:
`reviewed` (operator verifier) passes only on a clean `/ce-code-review` by a
SEPARATE review Operator, evidence = that review's bead id (rule in
`prompts/lead.md`, review role in `prompts/operator-code.md`).
**Review depth and recovery:** `cmdr workflow review-policy --repo PATH --base REF` resolves committed scope and a conservative full-review floor. Use `mode:agent depth:full` for that floor or an explicit full request; otherwise use the skill's supported `mode:agent depth:auto`, never an invented `depth:focused` argument. The skill owns its depth gate. Primary and adversarial Operators stay independent; an adversarial-only task performs a bounded challenge, not another full primary roster. `workflow review-exec` seals original scope/prompt provenance before releasing the child and records terminal status. Typed `workflow-review-result@v1` separates execution, source and coverage; completing missing coverage can recover an interrupted run at the same head, while actual earlier source findings and ambiguous legacy NOT CLEAN reports remain blocking. See `docs/testing/delivery-throughput.md`.

- **Manager enforcement** ("owning is not doing"): `manager-*` roles launch with
  `--disallowedTools` + `Edit,Write,MultiEdit,NotebookEdit` and a PreToolUse(Bash)
  hook `cmdr internal-manager-guard --role R` (`internal/managerguard`: classifier,
  pure reads (`reads.go`: a line of only read commands, /dev/null redirects, pure `$(...)`; git is an allow-list of read subcommands, tmux `;` chains and rg `--pre*` are refused, `home.go` refuses globs/recursive reads at the top of the commander home) pass even from a worktree or with `cd`/`git -C` into one; refusal log `$COMMANDER_HOME/logs/manager-refusals.jsonl`, 30-min Commander mail
  dedupe; fails open, errors in `manager-guard-errors.jsonl`). Takes effect only on
  a Manager's next (re)start. A closed Operator run's worktree is `git worktree
  lock`ed + a-w (`internal/worktreelock`); `spawn.gitWorktreeAdd` unlocks it.
  **Pure reads** (`managerguard/reads.go`, allowed from a worktree): no interpreters (python/node/ruby), no git
  `-c`/`--output*`/`-O`/`--ext-diff`/`--textconv`/`--upload-pack`, tmux args with `#(`, `sort -o`, sed `w`/`e`/`-f`,
  `>&file`, `<(...)`/`>(...)` (subs). `unwrap` knows wrapper flag values (`timeout -s KILL 5`, `env -S`).
  Any command naming `api-token` (also globs) is refused everywhere (`readsToken`). Tests `ReviewFindings*`.

**Tracks (one workstream = one area, one Manager):** a separate outcome inside a workstream is a
TRACK: a direct child of the workstream bead with label `kind:track` (`beads.TrackLabel`) and its own
gate chain (the gates/stages are children of the track). A track is never a workstream or a Manager of
its own: `livedata.NestObjectives` drops a track whose workstream is not in the list (no promotion by
the dotted-id fallback), ownership routing (`beads.Lineage`, real parent edges) sends its Operators and
wake-ups to the workstream's Manager. Verbs: `internal-create-objective --parent W --track`,
`internal-set-objective --track|--no-track`, `internal-track convert --project p --bead B --into W`
(parent edge via `Client.SetParent` = `br update --parent`, plus `kind:objective`+`kind:track`; dotted ids
do not change; regrouping real beads is the Managers'/Commander's call). Snapshot: step `track` +
`workstream`, NEEDS YOU `track`/`track_id` (item reads "Workstream › Track", `askWhere`), `no_gates` on a
track means no gate under it; a track is a Graph stage (`stageIDs`/`spineStageIds`, box tag "Track" at All).
Board (`livedata/board.go`): `trackLine` derives a track's line exactly like a row's (`trackWorkstream` = the track
viewed as a workstream); the workstream's line is `mostUrgent` of its tracks (needs > waiting > working >
stuck > stalled > idle), except that a track with live Operators working outranks a waiting sibling
(`liveOverWaiting`: Needs you still leads; the line is the live track's plus what waits beside it,
"Working on X · Cutover waiting for soak-period" / "+N more", age kept under the live work; golden
`track-live-beside-waiting`); tracks are indented sub-rows under the workstream (fold chevron saved in
`nav.tracksFolded`, app), the All / Focus switch also filters tracks (Focus = red only). Tests: `livedata`
`tracks_test.go`, `cmd/cmdr/internal_track_test.go`, `app/test/tracks.test.ts`, `prompts` `TestTracksInPrompts`;
shots fixture `app/test/fixtures/snapshot-tracks.json` (`node scripts/smoke.mjs --fake --fixture ...`).

**Install from the app (`internal/updatecheck`, `internal/api/update.go`, `internal/daemon/update.go`):**
the app's navigator shows "Update available: main is N commits ahead" (`GET /api/update`, cached 60s, no br call:
`updatecheck.Check` compares the installed `VERSION` (else the running build's `vcs.revision`) with local `main` of the
checkout, `git log installed..main` capped at 50; `git ls-remote origin` only when an origin exists, a hint, never fatal).
**Review marker rule:** a commit is `reviewed` only with a `Reviewed-by:` trailer, or a comment in the commander project's
`.beads/issues.jsonl` that names its hash (full or a prefix of at least 7 chars) AND has a verdict line that is exactly
`CLEAN` (no `NOT CLEAN` in that comment); else `not confirmed`. Never inferred. **Honest limit:** both markers are forgeable
by an agent (a trailer, a bead comment), so they inform the person, they prove nothing.
**Threat model:** agents run as the same OS user. They read `~/.commander/api-token`, copy any header and drive any client,
so no token, header, Origin or ticket can tell the app from an agent (the earlier ticket/HMAC/`X-Commander-App` scheme was
removed: it only looked like a barrier). The only protection that holds is an OUT-OF-BAND human act, and it lives in cmdrd:
any authenticated `POST /api/update/install` (`{"action":"install"|"rollback"}`; the app button just calls it and shows
"waiting for your confirmation on screen") makes cmdrd itself show a NATIVE dialog (`daemon.Prompt`: macOS `osascript display
dialog` with buttons Cancel/Install, default Cancel, `giving up after 120` = Cancel; Linux `zenity`/`kdialog`; the text goes
through argv, never into a script) naming the full short target revision, the behind-count, the first 10 commit subjects and
their review markers. It builds and installs only when the person presses Install. No usable dialog tool (`PromptAvailable`)
refuses 409 and points at the three terminal commands. A GLOBAL prompt floor (any action or target): one per 5 minutes and 3 per hour (429 "a confirmation was just shown" / "too many
confirmations this hour"), applied BEFORE any git work (Request is serialized, so a flood costs nothing), kept in memory and
seeded at boot from `Prompted` in the job file (a prompt in the last 5 minutes blocks; the file is forgeable but only ever makes
the floor stricter, so a cmdrd restart does not reset it). A call with `X-Commander-Role` is refused 403 without prompting; nothing
sends that header, so it is a convenience for well-behaved clients, not a barrier: the dialog is. The dialog opens with
"Requested from the Commander app." (header `X-Commander-Origin: app`, wording only, any client can send it) or "Requested via the
local API (not the app's button)."; after the first 10 commits it counts the hidden ones that are not confirmed. One job at a time.
**Pinned to the confirmed commit (`stageAndInstall`):** cmdrd re-checks that the checkout is on `main` and main is still the named
target (else "main moved, nothing installed, try again"; no longer the pin), then builds NOT in the live checkout but in a detached
`git worktree add` of the full target hash under `$COMMANDER_HOME/update-build/<rev>` (`daemon.exportCommit`: tracked files at that
commit only, hooks off; so an untracked `.go` file, a `go.work` or an edit in the live checkout is not part of the build; test
`TestExportCommit_BLOCKER1_OnlyTrackedFilesAtTheTarget`), with `GOFLAGS=` `GOWORK=off` `GOENV=off` and `CMDR_STAGE_DIR` outside the
export (`buildEnv`), `scripts/stage.sh` inside a build slot (`slot.RunEnv`). It refuses unless the staged `VERSION` equals the target
and BOTH built binaries carry `vcs.revision` == target with `vcs.modified=false` (`debug/buildinfo`). The export and stage dirs are
removed on every path. Then `binstage.Install` (keeps `.prev`) and a
detached (`Setsid`) run of the INSTALLED `<install dir>/cmdr install --restart-only --yes` (= `restartAndStopStrays`), so the
restart outlives cmdrd; chat hosts and Codex bridges are never touched. **Restart watch:** `Restart` hands back the result of the detached restart process; the OLD daemon waits 3 minutes
(`RestartWatch`) and flips the job to `failed` when that process fails or the old daemon is still alive then (unless a newer daemon
already wrote the job `done`), with the way back in the text: `cmdr install --rollback`, the manual `ln -sf <install>/cmdr.prev
~/.local/bin/cmdr` (same for cmdrd), and a crash-loop note (the panel shows `job.error`). The NEW daemon settles in `Updater.Boot`
(called once its API serves): `restarting` becomes `done` only when its stamp equals the job target, `failed` (same text) after the
watch time, and the settled state is written back. Tests: `TestRestartWatch_*`, `TestBoot_*`. Rollback takes the same confirmed path: `Request` reads the revision of `cmdr.prev` AND `cmdrd.prev`
(`BinaryRevision`) and refuses 409 unless both equal `VERSION.prev`; the dialog shows that revision read from the binaries, not the
`VERSION.prev` text (`TestRollback_PlantedPrevBinaryIsRefused`, `TestRollback_DialogShowsRevisionsFromTheBinaries`).
**Build dir hygiene:** `update-build` is chmod 0700 even when it already existed; at boot and before each build, entries for another
target older than 1 hour (a crashed build's leftover) are removed (`prepareBuildRoot`, `TestBoot_CleansStaleBuildDirsAndLocksParent`).
`cleanLine` strips every Cc/Cf/Zl/Zp rune (U+2028/2029, U+0085, bidi overrides, zero-width, ANSI escape bytes) from commit subjects,
collapses whitespace and caps at 100 runes (`TestCleanLine_*`). `stage.sh` keeps going when `codesign` fails (ad-hoc signed binaries,
macOS privacy grants may reset); that line shows in the job events as level `warn`. Job state (`confirming|declined|running|restarting|done|failed`, events) is persisted 0600 in
`$COMMANDER_HOME/update-job.json` so the new daemon reports the result; it is STATUS ONLY: nothing in it is executed or acted
on (test `TestJob_PersistedFileIsStatusOnly`), a forged file can at most fake what the app shows. Every attempt appends to
`$COMMANDER_HOME/logs/update-install.jsonl` (asked/declined/confirmed/installed/failed/refused; forgeable/truncatable by the same
user). Wiring: `daemon.apiOptions` sets `Options.Update` (`daemon.newUpdater`, nil without a known checkout -> 503; test
`TestAPIOptions_WiresTheUpdater`). The web build has the same button (it is only a request) plus the terminal commands.
**Remote/headless cmdrd (`app/electron/remote-update.ts`):** `GET /api/update` also reports `host` (cmdrd's hostname),
`repo` and `no_dialog` (why `PromptAvailable` fails, e.g. "no display"). When the desktop app's shell has `runUpdate` and
cmdrd is remote (`host` != `HostConfig.localHost`) or reports `no_dialog`, Install/Roll back go to the app instead of cmdrd
(`installRoute`): Electron MAIN fetches `/api/update?fresh=1` itself, shows `dialog.showMessageBox` (Cancel default,
Install; revision, host, first 10 commits with markers, the command) and on Install spawns `/usr/bin/ssh -o BatchMode=yes
-o ConnectTimeout=15 -- <target> '<cmd>'` (no local shell). The remote line: `export PATH=$HOME/.local/bin:$PATH
XDG_RUNTIME_DIR=/run/user/$(id -u); cd '<repo>' && { pin: on main, HEAD == confirmed main, no tracked edits; } && just build
&& cmdr install --yes --restart-cmdrd` (rollback: `cmdr install --rollback --yes --restart-cmdrd`). Output streams to the
panel (`update:remote-line`), failure shows the error + tail, then the panel re-fetches until the restarted cmdrd answers.
SSH target, read on the app's machine: `$COMMANDER_REMOTE_SSH` or `$COMMANDER_HOME/remote-ssh` (host, user@host or an
ssh config alias), else `<cmdrd host>` + `$COMMANDER_REMOTE_SSH_SUFFIX` / `remote-ssh-suffix` (e.g. `.example.net` for hosted
boxes), else the panel says to configure it. The renderer gets only `runUpdate('install'|'rollback')` (preload maps
anything else to install; main refuses anything else); one run at a time, one dialog per minute, 5 per hour. Agents on
cmdrd's machine cannot reach this dialog, so it is the same out-of-band protection. Honest limit: unlike cmdrd's path it
builds the live checkout (pinned by the checks above, not a detached export). The web page keeps cmdrd's path. Tests:
`electron/remote-update.test.ts` (fake runner), `UpdateBanner.test.tsx`, `TestUpdateStatus_SaysWhereCmdrdRunsAndWhyNoDialog`.
Main/preload changes need `cmdr app install`.
**Honest limits:** a process with screen control (computer use, accessibility) can click the dialog; an agent can still POST the
endpoint and pop a dialog the person declines. The prompt floor is in memory (seeded from the forgeable job file), so a cmdrd restart can show one more dialog, and
a dialog orphaned by a restart is not tracked by the new daemon (it times out as Cancel). A refusal after the floor (not on main, dirty,
up to date, `.prev` mismatch) still costs a little git work (`Branch`, `Check`) per request, bounded by the one-request-at-a-time lock.
A planted `.prev` pair with a matching forged `VERSION.prev` carrying an attacker-built binary is NOT stopped: the rollback check
only proves the binaries and `VERSION.prev` agree, and the person sees the revision the binaries carry in the dialog. The Manager/Operator Bash guards (`managerguard.TouchesUpdateAPI`) refuse commands
that, after removing quotes, backslashes, `$'...'` escapes and spaces, contain `/api/up` or `api/update`, or that aim
curl/wget/python/node/nc at the cmdrd port or `api-url` and mention update: a SPEED BUMP only (a script file or another tool
gets around it). Proven inert `rg`/`grep`/`egrep`/`fgrep` and `git grep` source
search argv are excluded: quoted patterns and paths are data. The exclusion
requires the existing pure-read checks, no substitutions, redirects or input
text, and no pipeline, interpreter or network consumer in the command. Program
options such as ripgrep preprocessors and Git pager/config execution retain
the conservative check. Tests: `TestSourceSearchEndpointData` and classifier-only
eval `TestSourceSearchScenario` (`source-search/cases.json`); no fixture is executed.
The real protection is the dialog. Agents get 403 from `/api/cmd` (`update`/`install` are not in the allowlist,
`TestUpdateCmdIsNotInTheAllowlist`) and there is NO CLI verb that installs for agents. Tests use fakes for the prompter, builder,
installer, restarter, slot and git (`update_test.go` in api and daemon, `updatecheck_test.go`, `UpdateBanner.test.tsx`); never
show a real dialog, run the real install or POST to the live cmdrd from a test. Shots: `?fixture=chat` shows the banner from
`demoStatus` (`&update=0` hides it).

**Stale cards and the push counter:** a card remembers what it was raised against. Migration 0019 adds a
nullable `pitches.basis` (JSON `store.Basis`: `spec` = `beads.SpecHash` = sha1 of the card bead's title+description+acceptance,
`gen`, `raised`, `named`/`prs`/`answers`, `age_checks`); `NeedsYou.Basis` is read from the latest pitch version, written by
`UpsertNeedsYou`/`RecordPitch` (the same card again is no new version unless it is re-raised against a changed spec or dependency basis) and set by
`cardBasis` in `internal-set-objective` (`--state needs-user|--decision`). The board derives staleness (`livedata.CardStaleIn`, pure over
the snapshot: `needs_you[].spec_hash`/`basis`, step `spec_hash`/`basis`): spec hash changed OR older than `[needs] stale_days`
(default 7, `0` = off; `livedata.SetStaleDays`, read by `LoadBoardZone`; raise time = basis.raised, else `since`); a card with no basis
only ages. The existing pitch watcher mails the owning Manager `[stale card]` once per card generation or re-baseline,
using the board stale flag; successful notices survive restarts in `$COMMANDER_HOME/stale-notices.json`. Failed sends retry; checks reset the notice key, and cards leaving NEEDS YOU are pruned. Mail includes the check, re-raise and withdraw verbs. Comments, labels, waits, wait-checked, owner and unrelated beads never stale it. Result: `display.asks[].stale` (`spec`|`age`|`push`|`named`|`pr`|`decision`) +
`stale_why`; the card stays answerable. Step 2 shows that reason on the board row, queue, pitch and diagram:
"Asked N days ago" or "Pushed back N times since Mon Jan 2" (pushes shown from 2). A spec edit keeps `stale: "spec"` for the
Manager's re-check mail, but `Board` drops its `stale_why`, so the user never sees it on any of them.
Snapshot asks/steps carry `push_count`/`push_first` from the existing labels; no extra table or polling.
Step 4 records named bead status/state/spec hash (legacy or missing hashes only compare state) (whole-token matching shared with held cards), tracked repository-qualified PR states, and answered pitch versions on the step and its governing user gates. `CardStaleIn` compares these over the snapshot; reasons contain no bead ids. `Source` reads existing pitch history and the waits watcher exposes PR states learned by its existing review polls (no additional GitHub calls; PRs without an observed state stay unknown). Missing facts and pre-basis cards are not evidence of a change. Missing answer map entries are omitted from the baseline, so recovery cannot invent a newer decision. Qualified PR references retain their repository; unqualified PR words resolve to the project repository and only match already tracked PRs. Still-right and re-raise refresh dependency fingerprints as well as the spec. Tests: `TestCardDependency*`, board fixture `stale-dependencies`, eval `TestStaleDependenciesScenario`.
`Client.Show` reads `acceptance_criteria` from `show --json` like List does (in an invented omitted-field regression, a gate card hashes without criteria and immediately reads "spec changed"; `TestShow_ReturnsAcceptanceCriteria`); cards raised before that fix on a bead with criteria need `--card-check still-right`/re-raise.
The renderer joins `display.asks[].stale_why` onto its needs-you items in `deriveSnapshot`; it does not calculate staleness.
Counted pushes surface as `stale: "push"` before spec/age. Push labels are reserved; reset through `--push-reset`. Config keys `[needs] stale_days|push_count|push_days` are pointer fields (unset = default; the
installed cmdr rejects unknown keys, so never write them into a real config; example in `docs/local-config.md`). Verb:
`internal-set-objective --bead B --card-check still-right --reason T` (`cmd/cmdr/card_check.go`; reason 15+ chars; re-baselines the
latest version in place, comment; refused when the card is not out of date, or when only age fired and a previous check already
answered age once; Operators refused). Push counter (`internal/beads/push.go`, `cmd/cmdr/push_counter.go`): a LATER `until:` wait target
(`applyWaiting`, judged before anything is written) or `--park --until` by more than 10 min is a push; labels `pushed:<n>`,
`pushed-first:<unix>`, `pushed-last:<unix>`, `pushed-flagged`; ci:/main-ci/prod-tag/review:/operator:/user and the Manager-to-close
wait never count. After `push_count` (3) counted pushes, or when the target would slide over `push_days` (7) from the first, the push
is refused unless `--push-reason` (15+ chars, not a verbatim repeat); a reason below the limits is a comment only; past them it counts,
sets `pushed-flagged` and mails Commander once. `--push-reset`: Commander and the user. Labels cleared on `internal-close-objective`.
Unreadable bead = fail open. Tests: `store` `basis_test.go`, `livedata` `stale_test.go` + goldens `testdata/board/stale-*.json`,
`beads` `push_test.go`, `cmd/cmdr` `card_check_test.go`/`push_counter_test.go`, `prompts` `TestStaleCardVerbsInLeadPrompt`, eval
`tests/evals/scenarios/stale-cards/` (`TestStaleCardScenario`), `stale-words.json` (`TestStaleWordsScenario`).

**Workflow templates (step 1 of reusable workflows):** `workflows/<id>.json` (embedded through `workflows/embed.go`, so an installed cmdr needs no checkout) is a fixed-schema template (`workflow-template@v1`: id, version, title, steps with stable `id`, `title`, `kind` task|checkpoint, `verifier` from a known list, `actor` operator|separate-reviewer|automatic, optional `after`, default the previous step). `internal/workflowtemplate` `Load`/`Parse`/`Validate` (unknown field/verifier, missing field, duplicate id, cycle fail loading) and `Instantiate` (key = 12 hex of project+parent+before+canonical PR; the definition JSON and its hash are snapshotted into the container's description, so editing the file never changes a plugged block). `cmdr workflow plug pr-review --project P --parent B --before M --pr URL` (`cmd/cmdr/workflow.go`) makes one `Review gate` container under B (labels `workflow-template:`, `workflow-version:`, `workflow-instance:<key>`, `workflow-hash:`), ordinary `kind:objective` children labelled `workflow-step:<id>` each waiting on the one before, and M waiting on the container, M's other edges untouched. Idempotent and resumable: it finds the instance by label (`List --status all`) and writes only what is missing; refused before any write for a cycle (parent == before, or parent already waits on M), a closed target, a second block for another PR before the same M, duplicate instance beads, or a version mismatch. Prints remaining steps as JSON; never closes anything. `workflow-` is in `reservedLabelPrefixes` (internal-label refuses it; create has no label flag). Close-time verifiers and the merge-gate rule: see "Reusable workflow completion checks". Tests: `workflowtemplate` `template_test.go`, `cmd/cmdr` `workflow_test.go` (stateful fake beads graph; no eval scenario fits: the harness has no beads graph backend).

**Review block in the diagram (step 3 of reusable workflows):** a plugged pr-review block is ONE step on its parent's level in the
display's `workflow` and `diagram` (`livedata/review_block.go`): `review` = `ReviewBlock{template, pr, head, done, total, stale, checks[]}`,
each check `{id, step, title, kind, text, verifier, head, finding, stale}` read from the rows' `workflow-check@v1` comments (folded
`workflow_check.steps` once closed) and the display's PR head (`PRHeadCache`), no GitHub call. Its line follows the row rules; a closed
block reads Done when its target meets the merge rule’s exemption (closed, merged or not, since a closed target gets no PR read; or settled PRs: one merged, none live), or every check has evidence for the known PR head and current writer binding revision (the shared
`CheckCurrent` predicate). Invalid template snapshots fail closed with an actionable message. Otherwise it reads "Checks need renewal: N to rerun" (stalled; an unknown head waits for a fresh read), the same steps `MERGE_REVIEW_INCOMPLETE` lists. The diagram draws it without children (progress "N of M checks done",
`Current` on a check maps to the block, `blockOf`); `cmdr board --diagram|--workflow` print the checks under it (`ReviewCheckLines`).
App: `components/ReviewChecks.tsx` in SequenceDiagram cards and WorkflowView rows, collapsed to "Show N checks"; a cmdrd without
`review` draws as before. Tests: `TestReviewBlock_*`, goldens `testdata/{diagram,workflow}/review-block` (regenerate the fixture with
`GEN_REVIEW_FIXTURE=1 go test ./internal/livedata -run TestReviewBlock_GenerateFixture`, also writes
`app/test/fixtures/snapshot-review-block.json`), eval `TestClosedTargetReceiptsScenario`, `app/test/review-block.test.tsx`; shots `node scripts/smoke.mjs --fake --fixture
test/fixtures/snapshot-review-block.json --query 'ws=1&flow=1'`.

**Approvals are gates:** `internal-create-objective` refuses titles that read as an
the user approval (Greenlight, Approve, Approval, Workflow approved, Spec approved,
Sign-off, Go/no-go prefixes) unless `--gate` is set (`--not-a-gate` for ordinary
work), so a greenlight is never a plain step that gates nothing.


**Spawn by project type:** `knowledge` projects spawn a host claude (`spawnKnowledgeHost`) with cwd = the project path and no git/worktree requirement; the legacy opencode flow runs only when `useHost` is off. `code` projects branch from `projects.repo_path` (migration 0012, `cmdr project set-repo <name> <path|--clear>`) when the beads root is not itself a repo; worktrees and `.commander/` stay under the project path. A code project with no repo fails with an error naming `set-repo`.

**Graph spine:** the Graph tab draws a workstream as its lifecycle stages only (`buildCanvas(..., {spine:true})`, `graph.ts` `spineItems`): gates, beads tied to a gate by blocks edges, and open beads tied to those are stage rows; other old steps fold into the first done non-gate stage ("Build done"). Done tasks are a count (click expands), max 3 open inline then `+N more`, a blocking stage is a short `blocked by` marker, no arrows. The Detail button restores the full tree. `graphCheck.ts` `expectedSpine`/`compareSpine` check counts and markers against br (the snapshot folds away a closed bead's children, so they are not counted).

**Park an ask:** `internal-set-objective --bead B --park [--until RFC3339|2h|3d] [--reason t]` / `--unpark` (`beads.Park`/`Unpark`, `internal/beads/park.go`; Managers and Commander, refused for `operator-*` and for a closed bead) is the supported way to put an ask the user put on hold out of play: status `deferred` plus labels `parked`, `parked-from:<status>` and `parked-until:<unix>`, and a comment with who/until/reason. Only status and those labels change, so a needs-user marker and the stored `needs_you` card survive; `--unpark` restores the old status (the card shows again), both are idempotent. cmdrd's minute loop (`waitWatcher.unparkDue`) un-parks a bead whose time passed and mails its Manager once ("parked ask ID is due again"). Snapshot step `parked_until`; the Graph word reads `Parked until <time>`. Works for older gates with a retired owner alias too. Tests: `Park*` in beads, livedata, daemon, `cmd/cmdr/park_test.go`, `app/test/needsyou-row.test.ts`.

**Standing decisions and proposals:** `cmdr decisions list|add|retire` (`internal/decisions`, `cmd/cmdr/decisions.go`) keeps the user's standing calls in `$COMMANDER_HOME/decisions/decisions.json` (flock, atomic; id `dN`, the user's words, date, scope `global` or a workstream/track bead, optional rule key `no-<words>`; retired ones stay on record). Add: Commander, the user, a Manager (default scope its workstream); never an Operator. Retire: Commander or the user only. The mail hook appends `decisions.Deliver` to every Commander/Manager turn whose context does not hold the current list (seen file `decisions/seen/<role>.json`, cleared at SessionStart, repeated every 30 min). **Door:** an agent write whose until-label, park reason, ask/question, gate name/check or gate/review step title names a rule key's words (whole words, slugged) is refused quoting the decision (`checkStandingDecisions`; words are never matched). A Manager's (`COMMANDER_ROLE=manager-*`) new needs-user ask (not on a gate bead), new gate (`internal-gate add`, not a same-name replace; `internal-create-objective --gate`) or review step (title `(re-)review`) once the scope's first gate is closed (`workflowApproved`), and an `until:` wait or park more than 4h out (none = no time) get label `proposed:<kind>:<unix>[:<prev state>]` (`beads.ProposedLabel`) in the same write and Commander a `[proposed]` mail (no `⚑ needs you` notice). livedata keeps a proposed ask/user gate out of NEEDS YOU (it rides `NoPitch` with `proposed`); the row reads "Commander reviewing the proposed <kind> on <step>", red after 30 min ("not reviewed: Commander's move"); the pitch watcher skips it. `cmdr proposals list|accept|overrule --project p --bead B [--reason]` (Commander/user only): accept drops the label; overrule (reason required) undoes the write (ask: previous state back, card deleted; gate/review: retired + closed or `gates.Remove`; wait: the proposed until labels cleared, never a Manager handoff receipt (`proposedWaitLabels`, read again after the hold); park: unparked) and mails the Manager why. **Overrule custody** (`cmd/cmdr/proposals.go` -> `holdReleasedLive` with `overruleTakes` and `boardManagerTurns`): before the undo, every bead of the closure it would leave with no holder (fresh `holding.NewlyUnheld`; a Manager the board shows mid-turn holds its steps) gets the Manager's `Manager dispatch` receipt (result `held[]`, mail "You now hold ..."); a retry reports a still-holding earlier receipt as held without a second one (`handoffWaits`, expired receipts not counted); never on a gate bead, a workstream bead or a step an Operator claims. Tests: `TestProposal_Overrule*`. Tests: `Proposal*`, `DecisionsCLI*`, `DeliversStanding*` in cmd/cmdr, `internal/decisions`, `livedata/proposed_test.go`, `TestStandingDecisionsAndProposalsInPrompts`.

**Graph levels:** the app's Graph shows Gates / Stages / All (default Gates). A bead's level is data: label `viz:1|2|3` (set with `cmdr internal-set-objective --viz 1|2|3|none`), else gate 1, stage 2 (`livedata/viz.go` `stageIDs`, mirrors `spineStageIds` in `graph.ts`), anything else 3; snapshot `viz` on steps and `gate_nodes`. Arrows only go from the bead right above; any other blocks edge is a `blocked by` marker. `cmdr graph check` runs at every level (rows, hidden counts, markers == blocks edges, arrow direction and crossing).

**Deferred = parked:** a bead with status `deferred` (`br defer`) is out of play: `beads.StatusDeferred`; livedata skips it (and its gate) in NEEDS YOU, waits, derived state, NoGates; the app's `viewmodel.parked()` drops it from next gate, progress, working/stalled; the Graph draws it muted with a `Parked` word (`.parked`), gate tone idle, `graph check` keeps status faithful and skips it in the order rule. The open-bead invariant (`OpenBeadInvariant`, so `Snapshot.broken`, `SnapshotViolations`, `graph check`, the invariantWatcher) treats a deferred bead as covered (never a violation, never an open child that covers its parent), and `managerLineOf`/gate waits in `rows.go` (check-in rows) skip it, so it never reads 'Needs you' or red. Tests: `*Deferred*` in `livedata/invariant_test.go`.

**Workflow gate:** every workstream/track gets a `Workflow approved` gate bead
(`internal-create-objective --gate --approver user --before <stages>`).
`internal-spawn-operator` refuses ("workflow not approved: <bead>") when the
step or an ancestor is blocked by an open gate bead, unless `--writes-workflow`. It
(and `internal-set-objective --state in-progress|--status in_progress`, also with
`--writes-workflow`) check too that the bead's scope has a closed first gate. **Warn-only for now:**
a missing/open first gate prints `warning (will refuse after the install): ...` on stderr, appends a
line to `$COMMANDER_HOME/logs/workflow-gate-warnings.jsonl` (time, bead, scope, gate, reason) and
proceeds; `COMMANDER_WORKFLOW_GATE=enforce` makes it a refusal (the install flips the default). The
blocks-edge "workflow not approved: <bead>" refusal is unchanged.
Scope = nearest `kind:track` ancestor, else the workstream (real parents,
`beads.Lineage`); the gate is a child `gate` bead titled Workflow/Spec/Approach/Route or containing `workflow approved` (any prefix)
or starting with Greenlight (case-insensitive) with a user approver, including the legacy owner alias
(`cmd/cmdr/workflow_gate.go`). The error names the missing/open gate and the
`internal-create-objective --parent <scope> ... --gate` command. Exempt: gate,
operational, closed/deferred beads, the workstream bead itself, a closed/deferred scope.
No backfill: `cmdr internal-workflow-gaps [--project p] [--json]` (read-only) lists
every open workstream/track the rule will block once enforced.
**Approving choice:** a decision-card option may carry `"approves": true` (`store.DecisionOption.Approves`, app types
`approves?`). `internal-resolve-needs --action decide` on a gate closes it (the Approve path: `GateChoice` = Approve,
same journal, dependants and mail) when the picked option approves. A card with ANY `approves:true` option decides by
those options alone (no label guess; a label not on the card approves nothing). Only a legacy card with no flag falls back
to the label: normalised, an exact allow-list (approve, go ahead, build it, merge it, ship it, install, ...) or `approve ...`,
never when it has a deferring word (later, back, but, not, no, change, ...). Change/Drop keep their meaning. The verdict is
journalled as `NeedsResolution.Approved` (JSON field, nil on older journals); `--resume-only` closes an answered gate left
open only when that verdict is true (older journals: the exact allow-list only) (`repairApprovedGate`).
Tests: `ApprovingChoice*`, `GateCardApproves*`.

Open gates approved by `user` are NEEDS YOU decision cards (Approve/Change/Drop,
`livedata.gateNeedsYou`); `internal-resolve-needs --action decide --choice
Approve` closes the gate; Change returns ownership to the Manager and Drop parks it.
Every answer clears the stored card and needs-user markers. `decision:answered`
suppresses automatic gate cards until the Manager explicitly re-flags with
`--state needs-user --decision` (which clears the marker). Reopening an approved
or parked gate to ask again requires `--status open --state needs-user --decision
'<json>'` together; `--status open` alone intentionally retains the answered marker.

Resolution journals each immutable answer by project, bead and pitch generation
in `needs_resolutions`, atomically with pitch history. Receipts advance through
journaled, bead-written, closed and mailed. Cleanup and queue deletion precede
the bead-written receipt; Close follows it. A missing Close receipt is recovered
from the matching closed bead's answered marker without replaying cleanup.
Re-flagging an answered card creates a new pitch version (even with identical
content); unfinished older entries are superseded before processing the new
answer. CLI and app `--resume-only` retries finish saved answers after cards
leave the snapshot. Pending mail retries do not rewrite bead state. Agent Mail
has no idempotency key: a crash or receipt failure after sending can duplicate
mail, so each notification carries a stable answer id for Managers to ignore
when already acted on.

**Approval carry-over and settled asks** (`livedata/cover.go`, `cmd/cmdr/door_checks.go`): `livedata.UserGatesOver(snapshot, step)` lists the user gates over a step: an open gate's run is its Workflow `unlocks` (`unlockRun`), an approved (closed) gate's the same run read with the gate and the closed steps it started still starting what follows (`withClosed`), so it reaches children and siblings it unblocks and stops at the next gate; gates are never covered. `requireWorkflowGate` (spawn, in-progress) passes a step an approved gate covers (`coveringApproval`), and `--prod-gate` counts covering as linked; no readable board = no carry-over. `checkAskNotSettled` runs on a NEW needs-user card from an agent (no role = the user, exempt): refused when the step, a gate over it, or a not-closed bead with the same PR (its `pr:` label vs the card's `PR #N`/`PR N`/`pull request #N`/`/pull/N`, never a bare `#N`, and the step's `pr:`) has a pending `review:` or future `until:` wait ("this is waiting on <who>; the user already said wait", the Manager-to-close timer excluded); when the step carries `decision:answered` without `--status open` in the same command (open gates sent back with Change exempt); when an approved gate covers it and the card is an approval ask (approves option or approving label); when an open gate is over it (update that card); when another bead with the same PR is needs-user/answered/parked or the board's open card on another bead names the PR (update that card). A GATE's card with an ancestor (track/step) holding the same PR: a settled ancestor card (answered/parked/closed, read fresh from the bead, so a stale snapshot never refuses) is no duplicate; an OPEN one is refused naming `cmdr internal-move-card --project P --from ANCESTOR --to GATE` (reads the card from the board, saves it under `$COMMANDER_HOME/moved-cards/`, puts the ancestor back in-progress under its workstream Manager, writes the gate through the same door, restores the ancestor on refusal) or `--not-the-same-ask <reason>` on a different question (reason non-blank, ≤500 chars; accepted use writes "second card allowed: not the same ask as ANC: reason (by ROLE at TIME)" as a comment on the gate and the ancestor BEFORE the card, a failed note blocks the card; `movedCardErr`; `TestAskDoor_*Track*`, `TestMoveCard_*`). The board matches a held gate by id (whole token), title, or its `pr` field (`SnapStep.PR`, the pr: labels; `livedata.PRRefs` is the one PR-ref function the door and board share). Board: an open user gate with no pitch whose ancestor's open card names the gate id/title or the gate's PR (`adoptHeldCards`, `board.heldCard`) reads Needs you (not "Manager writing the pitch"), diagram hand = you, workflow step `card_on` = the holding bead (`held_card_test.go`). Tests: `Covering*` in livedata, `ApprovalCarries*`, `*CoveredStep*`, `ProdGateCovering*`, `AskDoor_*`.

**Production exception (`--prod-gate`):** `internal-spawn-operator --prod-gate <bead>` (Manager callers only,
`COMMANDER_ROLE=manager-*`) lets an Operator take the ONE production action a gate covers: the bead must be a
CLOSED `kind:gate` with approver `user` (`ownercompat`, the legacy owner approver counts), a non-empty description (the exact
scope) and be linked to the step (`gate-of:` the step/an ancestor, or a blocks edge). Approving a gate never writes its description (an approval must not widen production authority), and a workflow approval (title matching `firstGateRE`: Workflow/Spec/Approach/Route, `workflow approved`) is refused whatever its description says, so the scope is always the gate's own description, the text its card showed the user (test `TestApprovingAGateNeverWidensProductionAuthority`). The task bead gets label
`prod-gate:<beads.EscapeLabel(id)>` (br labels forbid dots); the Operator's bootstrap prompt ends with a `PRODUCTION GATE` paragraph quoting the description
and env `COMMANDER_PROD_GATE` is set (`spawn.prodGatePrompt`). Without it the role prompt's no-production guardrail
and `operatorguard` stand. Tests: `TestSpawnOperator_ProdGate`, `TestHostBootstrapPrompt_ProdGate`, `TestProdGateInPrompts`.

**Invariant watcher reads incrementally:** `invariant.fresh` loads through `beads.Incremental(cached)` (the shared CachedClient store, only beads whose list key moved go to `br show`, never an export, export-sourced display entries are re-read once); action verbs keep `FreshDetails`/`ForActions`. Tests: `InvariantIncremental*`.

**One predicate for "held" (`internal/holding`, il9.204.2.1 step 1):** `holding.Held(bead, world)` is the only decision of whether an open bead has a holder (gate, user card, open child, park, typed wait, live Operator, open blocker of the bead or any ancestor). `livedata.OpenBeadInvariant` (so `SnapshotViolations`, `UnheldAfter`, `Snapshot.broken`, the diagram's `unheld` finding via `Broken`, `graph check` and the daemon's invariant mail) and the audit's `openWithoutOwner` both call it through `rowWorld`/`rowBead`; a Manager's name holds only mid-turn, a bead blocked only through its parent is held. **One state:** the snapshot build derives Broken again after the chat overlay (`markOverlaid`), so a Manager the overlay has mid-turn holds its step as the board's line says. **Expired waits:** a Manager handoff wait (`beads.IsManagerHandoff`) stops holding `beads.HandoffWindow` (1h) past its deadline (`beads.WaitHolds`; `OpenBeadInvariantAt` at the load's `LoadedAt`, `holdread.Derive` at wall time); other time waits hold until cmdrd's wake clears them; `OpenBeadInvariant`/`SnapshotViolations` (no clock, text waits) expire nothing. Tests: `internal/holding` `TestHeldMatrix`, `TestEveryHeldReaderCallsTheOnePredicate`, eval `TestHeldParityScenario`, `TestOverdueHandoffStopsHolding`, `TestOverlaidAgentStateIsTheInvariants`. Design and the remaining 7 steps: `docs/reports/2026-11-09-incorrect-state.html` section 6.

**Guarded holding layer, steps 2-8 (il9.204.2.2; the plan is `docs/reports/2026-11-09-incorrect-state.html`):** (2) the board reads an empty open scope (no steps) as idle "Not started: Manager to plan" (main's `hasPlan`), or "Manager planning" while it carries the `Manager to plan` handoff wait (`beads.ManagerPlanWait`, also `Manager dispatch`/`Manager close`/`Manager to recover`: all `IsManagerHandoff`, shown by `waitText`; plan/recover waits are never "expired"); a finished scope keeps "Nothing to do"; `StalledWork` ignores a level with no step. (3) Atomic birth: `internal-create-objective` writes the holder in the create labels: a workstream/track gets the plan wait (cleared by `clearPlanWait` when its first child lands), a plain step carries the holder it names (`--waiting`, `--deps blocked-by:` or `--owner operator-*`; a Manager/user owner is refused, nothing created) or else the Manager to start receipt (gates and `--operational` are exempt); `workflow plug` gives its first step a Manager dispatch wait. (4) `holdManagerOwner` (set-objective Manager owner / in-progress with no other wait) and resolve-needs decide/defer on non-gate steps add the dispatch wait. (5) `internal/holding` `Graph`/`NewlyUnheld`: `cmd/cmdr/holding_door.go` `requireHeldAfter` loads a write's closure FRESH (never the snapshot; batched: one `List{IDs}` + one `FreshDetails` per closure level, and a status-only list for the parent's other children and the blockers, so a close under a 250-child parent costs about 10 reads, not 2 per sibling; `TestLoadUnderALargeParentIsJudgedInBoundedReads`; more than 60 fully read beads = `holdread.ErrTooLarge`; a non-close verb whose closure cannot be read passes, the close door falls back to cmdrd's board (`closeCustodyFromBoard`, `livedata.UnheldAfter`: custody, or refusal for another agent; only with no board too does it pass, with a stderr warning; `TestCloseDoor_LargeParentStillGetsCustodyInBoundedReads`, `TestCloseDoor_ClosurePastTheBoundFallsBackToTheBoardsCustody`)) and refuses an agent "refused: <verb> would leave <bead> with no holder. Hold it in the same command: ..." (verbs with no holder flags, dep rm and place fold-back, name the verb to run first: `depRmFix`, `moveFix`); a wait on an Operator holds only while it runs (`holding.Bead.OperatorWaits`, as `MarkLiveOwners`); `--state not-started` simulates the card it takes away (M5, `TestNotStartedOnACardKeepsAHolder`); wired into `internal-dep rm`, `internal-gate pass|rm|apply|check` (`requireGateClosesHeld`), set-objective `--state not-started`/`--owner none`/`--waiting none`/`--unpark`, the close door (after the `--then-*` hold, ancestors skipped), moves (`requireMoveHeld`: track convert and place hold a parent they empty with Manager to plan, written before the move; unwrap is judged by `requireHeldAfter`; `Graph.With` moves a bead between child lists). Writes that hand work back by design hold what they release for the Manager BEFORE the write (`holdReleased`/`holdWithManager`: proposals overrule = Manager dispatch, a failed spawn's removed task = dispatch on its step and Manager to recover on a task that stays, a failed provider respawn = Manager to recover). The close door decides on FRESH reads only (`checkCloseLeavesWorkHeld` over `holding.NewlyUnheld`; `livedata.UnheldAfter` is no longer the door's). **The user is never refused:** a role-less write (the app's /api/cmd included) that leaves work unheld owes the Manager's "Manager to start" receipt (`custodyOwed.settle`: before a write that only ends a blocker, a gate pass or dep rm, else right after the write; result `custody`), unless it carries `--leave-unheld '<reason>'` (stripped from any `internal-*`/`workflow` verb's args by `run`, 10+ chars, user only; recorded as a `left unheld by the user` comment and `logs/leave-unheld.jsonl`). Tests `TestUsersHolderDropGetsTheManagersReceipt`, `TestDepRmRunsTheClosureCheck`. Loader shared with cmdrd: `internal/holdread` (`Load`, `EditsFromOps` simulates door argv, `HolderFirst`). `TestEveryHolderDroppingVerbRunsTheClosureCheck`, `TestHolderRefusalsNameExistingVerbsAndFlags`, `TestLeaveUnheldIsTheUsersRecordedEscape`. (6) cmdrd: operator waits re-read the running set before clearing (compare-before-clear); `runSettler.endFinishedRuns` (minute loop) kills a host Operator's pane 3 min after a `Final report:` comment (or a report on a closed bead), no stop marker; the pane's end HUPs the launcher before it writes its exit marker, so `spawn.EndFinishedSession` writes that marker first (taken back if ending the pane fails) and the sweep settles it as exited, not died (`TestEndFinishedSession_RealLauncherReadsAsExited`, real launcher, private tmux); comments are one batched `FreshDetails` per project under `endReadTimeout` (10s), a failed or hung read skips that project this tick, and a batch failing on one missing task bead is reread one bead at a time so only that run is skipped (`TestEndFinishedRunsBatchSurvivesAMissingBead`); a run is resumed when mail was shown to it after the report (its mail cursor moved, `mailstate.CursorAt`, either role spelling: the hook injects a Manager's answer, usually inside the 2-min wrap-up window) or its pane output (the `.fifo` mtime, `spawn.RunOutputPath`) moved more than 2 min after the report; a resumed run is ended only after 20 min with neither moving, and never while its output is unreadable (M4/R2; a resumed tool call silent that long is still ended; a new report restarts the 3-min grace); a run ended for good is handed back with owner + `Manager to recover` (1h) wait; a live gate's first pass mails the Manager. **Door intent:** `/api/write` takes `intent:{bead, verb, leave_unheld}` (`beads.TxnIntent`): cmdrd reads the bead's closure fresh (`holdread.Load`, liveness from `Daemon.liveOwner`), simulates the ops, refuses an agent's unheld result; the user's (no role) gets the Manager's receipt ops added instead (its `leave_unheld`, 10+ chars, passes as is, logged); runs holder-adding ops first and answers `holders` (bead -> holder kind); unreadable/unsimulatable = not judged, `door-errors.jsonl` (`TestDoorIntent`). **Sent by:** `beads.WithIntent(ctx, ...)` marks a verb's decisive write (`runWrite` puts it on the request, `TestRunWrite_CarriesTheIntentFromItsContext`): `internal-close-objective`'s close and `internal-set-objective`'s state flush (`verbIntent`). Not yet: create (the door cannot simulate `create`; born held by `birthHolder`), gate, dep, park, proposals (their own fresh check runs, no board patch). **Single-write settle:** `moveBead` is ONE `UpdateFields` (`beads.Fields` gained `Status`/`AddLabels`/`RemoveLabels`): exited = Manager owner + review + Manager to close in, Operator claim out; died/stopped = reopen + Manager owner + Manager to recover in (a respawn's `afterRespawn` replaces them with the new run in one write); the waits wake replaces a fired wait that was the bead's only holder with Manager dispatch in the same update (`onlyHolder`: `holding.Held` over the bead's own fields, so an expired handoff holds nothing, m2), and `unparkDue` unparks with that wait (`beads.Unpark(..., hold...)`). Tests `TestSettleDiedHeldScenario` (`tests/evals/scenarios/settle-died-held.json`, every outcome x a failed write at each point, fails on the old settle), `TestWakeClearKeepsAHolder`, `TestUnparkHoldsAStepWithNoOtherHolder`. **Git stash:** the Operator guard refuses `git stash` (not `list`/`show`), also past git's separate-value global options (`gitValueOpts`: `-C -c --git-dir --work-tree --namespace --config-env --attr-source --super-prefix ...`): worktrees share refs/stash (`TestOperatorGuardRefusesGitStash`). (7) `Server.settle` loads again up to 3 times when a load kept last-good rows; an intent write's simulated result (`intentPlan.after`, Plan.After) is laid on the served board at once (`Server.patchNow` -> `livedata.Patched`: status/state/owner/waits, Broken and the display derived again, unchanged rows keep their age), listed in `Snapshot.pending` until a clean load that began after the write reads it back; a load that kept last-good rows keeps the patched rows (`refreshLocked`) for at most `pendingPatchTTL` (2 min, `TestPendingPatchExpires`). Tests `TestPatchedShowsTheAnsweredWrite`, `TestBoardReflectsAnsweredWrite`, `TestKeptLastGoodKeepsPendingPatch`, scenario `tests/evals/scenarios/close-then-board.json` run by `internal/api:TestCloseThenBoardScenario` (door-private; fails without the patch). Only intent writes patch. (8) `managerguard.writeVerbOf` is an allow-list of bd/br read verbs (`bdReadSubs`, `bdReadSecond`); anything else, including `batch`/`sql`/`comment`/`tag`/`link`, is refused; `scripts/br-bd` still wraps br only. Prompts teach one holder rule (`TestHolderRuleInPrompts`). **Scope adds:** an open user card (needs-user or a stored `decision` on an open step) is the You lane whatever the approver label says (`onUserCard`, diagram `hand`/`restingHand`/gate split; fixture `testdata/diagram/card-you-lane.json`, `TestDiagram_OpenCardIsTheYouLaneWhoeverApproves`); `cmdr internal-gate convert --project P --bead S --approver A --criteria C` turns an open plain step into a gate in place (criteria, approver labels, then the `gate` label; card, owner, waits and edges kept; owning Manager/Commander/user; refuses a gate, closed, workstream, track or operational bead; `TestGateConvert*`).

**Open-bead invariant:** every open bead (not a gate bead) has a live agent (owner +
working Operator / live owner role), a recorded wait (`waiting:` / `until:` label) or is
the user's (open gate, needs-user); a parent with an open child, or a bead that is not in progress and has at
least one open (or parked) blocker, is fine (not ready yet; the blocker is judged on its own, no chain check;
in-progress and ready beads still need an owner). Else it is broken data: `livedata.OpenBeadInvariant` (one rule)
feeds `Snapshot.broken[]` + `SnapStep.broken` (row reads "State missing",
never "Stalled"), `cmdr graph check` (`livedata.SnapshotViolations` over the snapshot;
a violation fails it, JSON `workstreams[].violations`) and cmdrd's `invariantWatcher`
(`internal/daemon/invariant.go`, 3-5 min jittered, skips a degraded snapshot) which mails
the owning Manager once per bead after two passes, with the fix commands.

**Live gates** (`internal/daemon/livegates.go`, `gateWatcher`, started with the waits
watcher when mail is up): every `verifier:auto` gate bead of an open stage is
re-run (same `gates.RunShell`/`EvalAuto` as `internal-gate check`) when the head
sha of a PR in its check (`gh pr checks|view N`) or main moves (polled every
minute, read-only `gh`) and every 3 min; a changed outcome goes through
`gates.SetResult` (a break reopens the bead with `gate-status:failed` and a
"gate failed by auto at <unix>: <out>" comment) and the Manager is mailed only on
passed<->failed. `user`/`reviewer:`/`operator` gates latch: the daemon never
touches them. Skipped while the store is degraded or the stage is closed, and for a gate the merge
rule protects whose check is not exactly the standard merged template (`protectedByWorkflow`,
`gates.IsMergedCheck`, and that form must name every PR the target is judged on: its plugged blocks', its own
and its stage's `pr:` labels; none known = protected; cmdrd has no completion door, a failed read counts as
protected; tests `TestStandardMergedCheck*`).
**Graph shape is the Manager's job** (`prompts/lead.md`): parents by br
parent-child, real gate beads, history under its stage, viz levels set
(`viz:1|2|3` / `internal-set-objective --viz`), then `cmdr graph check`.

**Waits:** a bead label `waiting:<unix>:<kind>...` (`internal/beads/wait.go`),
set by `internal-set-objective --waiting`. The watcher lists from the export, which can lag the db, so before it mails, `wake`
re-reads the bead (`beads.FreshShow`): a wait gone from the db mails nothing, and a fired wait is remembered (`woken`) while stale reads
still show it, so it fires once (the once-a-minute `[operator] X is no longer running`; a cmdrd restart forgets, the fresh read still holds). Before that mail the watcher re-reads the running runs, so a spawn landing between its run list and the bead read raises no false notice (`operator-wait-spawn.json`). `internal/daemon/waits.go` polls `gh`
(read-only, injected `ghRunner` in tests) every minute (`waitPollInterval`), mails the workstream's
Manager once per event and clears the wait; a poll with no event writes nothing and only moves the in-memory
last-poll time (`CheckedAt`; snapshot step `waiting_checked`, shown as "checked N min ago" on the
chat's Watching row) and never mails (no "still waiting" nudge; the minute watch no longer
delivers automatic check-ins, only ones a Manager set with `internal-checkin`); livedata shows the Manager as
`waiting` (`SnapAgent.wait`, `SnapStep.waiting`). Kinds: `ci:#N`, `main-ci:<sha>` (a label cannot hold a full sha and `gh run list --commit` gives a short one 0 runs with no error, so `fullCommit` resolves it through `gh api repos/R/commits/<sha>` each poll; all runs completed, one wake with the failing count. GitHub answering 404/422 for `mainCIMissingGrace` (30 min) wakes once with `[main-ci] <sha> not found in <repo>` and clears; any other failed read retries. A real commit with no runs still waits: nothing tells it from runs not started yet. Tests `TestMainCIWait*`, whose fake gh answers only a full sha; eval `TestMainCIWaitScenario`, `scenarios/main-ci-wait.json`, private to cmdrd so it runs in `internal/daemon`), `prod-tag:<sha>` (`gh api` matching-refs for the tag prefix, `$COMMANDER_PROD_TAG_PREFIX` default `prod-`, newest 15 tags, compare `sha...tag` is ahead/identical), `review:`, `user`, `operator:`, `until:<RFC3339>[:label]` (label `waiting:<since>:until:<target unix>:<EscapeLabel'd label>`; no gh call: `pollUntil` runs every minute and mails `[wait] <label> time reached` once at/after the target, then clears it; never stamps `wait-checked`; snapshot step `waiting_until` + `waiting_until_as`, the row reads `Waiting for <label> · <local time>`, the Watching row `<label> · <time>`); `main-ci`/`prod-tag` take `<owner/repo>@<sha>` for another repo. No `deploy:` kind: `prod-tag` covers it.

**Agent write guard (agents lose direct bd/br writes):** an agent (`manager-*`, `operator-*`) writes beads only through cmdr verbs (the write door), never `bd`/`br`.
The Manager guard (`managerguard.Classify`) and Operator guard (`operatorguard.Classify`) refuse `managerguard.BeadsWriteVerb`: bd/br
every verb NOT on its read allow-list (`managerguard.writeVerbOf`: `bdReadSubs` show list ready blocked children search query count status
statuses types stale history lint graph export state info where context version help prime quickstart onboard memories recall find-duplicates
stats diff; `bdReadSecond` dep list|tree|cycles, label list|list-all, config get|list, human list|show; `comments <id>`; `sync --flush-only`),
so a new write verb (batch, sql, comment, tag, link, human respond|dismiss ...) is refused unseen. Judged under global flags (`-C --db ...`;
a `-v`/`--help`/`--version` before the verb hides nothing: `bd -v close X` is a close), env/timeout wrappers, `cd x &&`, `bash -c`, `$(...)`.
Test `TestBeadsWriteVerbFlagsBeforeTheVerbAndHuman`. The refusal names the cmdr verb and goes to
`$COMMANDER_HOME/logs/agent-write-refusals.jsonl` (no Manager sidebar note or Commander mail). The user (no role), Commander and cmdrd are unaffected (the hooks act
only for those roles). `scripts/br-bd` (the installed `~/.local/bin/br` stand-in) refuses the same verbs when `COMMANDER_ROLE` is manager-/operator-* unless
`COMMANDER_BR_HELD=1` (cmdr's own br children). Off switch: `COMMANDER_AGENT_WRITE_GUARD=off|0|false|no` in the hook's / script's environment. **Speed bump only:** a script file,
another tool, or the real `br-rust` binary gets around it; the real boundary would be database grants (not done). Takes effect on an agent's next (re)start with an installed cmdr,
and the stand-in only after copying `scripts/br-bd` over `~/.local/bin/br`. Cmdr verbs that replace the writes: `internal-comment --project P --bead B --text|--file`, `internal-dep --project P add A B`
(A waits on B), `internal-phase --project P --bead B --phase X` (`bd set-state cmdr=`), `internal-create-objective --operational --parent B` (kind:operational), plus `internal-set-objective|close-objective|gate`.
Also now: `internal-dep add|rm`, `internal-label --bead B add|rm <label>` (plain labels; state labels such as waiting:/until:/hold/viz:/gate/kind:track/decision:/needs-user/cmdr:/pr: are refused naming their verb, `reservedLabelPrefixes`), `internal-update --bead B [--title|--description|--description-file|--notes|--acceptance|--priority]`, `internal-close-objective --reason`, `internal-create-objective --deps blocked-by:ID|blocks:ID` (bare ID = blocked-by). **Invented guarded-edit exercise:** `internal-update` refuses a closed bead, `--description`/`--acceptance` on a gate bead (names `internal-gate add`) and a `--title` that reads as an approval on a non-gate; `internal-dep add|rm` and `internal-create-objective --deps blocks:ID` (non-gate) refuse a gate or `kind:track` bead at either end (`checkNotGateEdge`); add and rm differ for a nested `kind:workstream` bead: add is refused (`internal-gate link` adds that edge), `internal-dep rm` passes since nothing else removes it, and a top-level one passes both (`Show` reads the title, status, parent, labels, type and assignee from `show --json` (a multi-line title once misparsed status in the text form; `show-multiline-title.json`), on bd and br, so a body line such as `Labels: plain` sets none of them; only if that JSON read fails do the text parser's values stand, and a nested bead then reads top-level; `TestInternalDepEdgeRules_BDIntegration`, eval `TestNestedEdgesScenario`, `scenarios/nested-edges.json`); `dep add` refuses a cycle (the "stage edge" rule is only gate + `kind:track` beads: a plain step that a gate sits between is not detected); `internal-label` also reserves `superseded retired wait-nudged reviewed approved` and bare `waiting`/`owner`, and refuses a leading `-`; dotted labels stay refused (the br label grammar forbids them); `internal-phase` validates the phase list. Guards: `(bd close X)`, `{ bd ...; }` and `xargs [opts] bd write` are refused, `bd <verb> --help`/`-h` (flags only) is allowed, and any command that NAMES `COMMANDER_BR_HELD` or `COMMANDER_AGENT_WRITE_GUARD` is refused (verb `bypass`) unless the kill switch is on. `eval` is re-scanned like `bash -c` (its joined arguments, nested `eval`, inside env/command/`cd &&`/`{ }`/`( )`; `eval "bd show X"` is a read and allowed; `TestBeadsWriteVerbEval`, `TestGuardsRefuseEvalOfBeadsWrite`). **Indirection still not caught:** variables (`c=close; bd $c X`, `B=bd; $B close X`), command substitution that builds the verb or the command name, pipes into a shell (`echo 'bd close X' | sh`), `python -c`/`node -e`, and script files. **Known gap (scope):** the cmdr bead verbs do not check that the bead belongs to the caller: an Operator does not know its task bead (no env carries it; `COMMANDER_ROLE` names only the Operator), so "own task or descendant" has no cheap source, and a Manager rule would need `beads.Lineage` per call. Any agent can still edit any bead of the project through these verbs; the write door sees only the role.
Every refused verb's reason names an existing cmdr verb (`TestEveryRefusedVerbNamesAnExistingCmdrVerb`). Nothing is left without a verb except deleting/merging/importing beads and `config`/`sync` (the reason says to mail your Manager). Tests: `agentwrite_test.go`, `OperatorGuard*`/`ManagerGuardRefuses*` in `cmd/cmdr/agent_write_guard_test.go`, `scripts/br-bd_test.sh`, `TestAgentsDoNotTeachDirectBeadsWrites`.

**Tmux kill guard:** `managerguard.TmuxKill` (used by `operatorguard.Classify` and `managerguard.Classify`, own refusal text) refuses `tmux [-L/-S x] kill-server`, `kill-session -a|-t ...`, `killall|pkill tmux`, and `kill`/`xargs kill` in a line naming tmux; read verbs and a bare `kill-session` pass. Also refused: unique prefixes (`kill-serv`), `\;` / quoted-`;` sequences, `run-shell|run|if-shell` bodies and `#(...)` (re-scanned), `source-file`, `kill-window|kill-pane` with `-t|-a`, `respawn-pane -k -t`, `send-keys -t` with exit/C-d, any kill-* with `-L|-S`, `kill|pkill` naming `commander-ops`, `eval`. The launcher's own `tmux kill-session` runs in its shell script, not the Bash tool, so it is unaffected. In text it cannot parse, a kill word must start a word (`killWord`: `skill` is none; `killServer`, `killed`, `pkill`, `SIGKILL` are), and a backslash escape starts a word (`\tkill`, `\x20kill`; the cost: a literal `dir\skills` reads as a kill). `parse` keeps a backslash inside double quotes as a shell does (only `` $ ` " \ `` and newline are escaped), so this holds in `python3 -c "..."` too. Not caught: a tmux word right after an escape (`\ntmux`; `tmuxWord` needs a word boundary). Speed bump only (a script file gets around it). Tests: `TestTmuxKillRefusesKillWordAfterEscape`, `OperatorGuardTmux*`, `ManagerGuardTmux*`, `TmuxKillAllowsKillInsideAnotherWord`, `TmuxKillStillRefusesKillWords`. The keeper unit/plist no longer runs `tmux -D` (it joins an already running server as a client and dies "open terminal failed: not a terminal", a restart loop while the legacy job owns the socket): it starts the server detached with a `keeper` session, `exit-empty off`, and polls (`TestTmuxKeeperUnitsHaveASession`); takes effect only after `cmdr install`.

**Build slots:** `cmdr internal-slot run [--timeout D] -- <cmd...>` (also `acquire`/`release`/`status`; `internal/slot`)
bounds full builds/suites machine-wide with N kernel `flock(2)`s on `$COMMANDER_HOME/build.lock`, `build.lock.1` ...
(holder info in `build.lock[.i].info`: display line + JSON with workstream and dir; waiters in `build-slots-waiting/`;
"waiting for a build slot (k/N held: ...)" every 30s; freed when the holder exits or dies; a child runs with
`COMMANDER_SLOT_HELD=1` so nested acquires do not self-deadlock). N = `$COMMANDER_BUILD_SLOTS`, else `[concurrency]
build_slots`, else max(2, cores/2), read at every acquire. Fair across workstreams (from `COMMANDER_MANAGER_ROLE`): a
waiter whose workstream holds more slots than another live waiter's does not try. `cmdr internal-ship` holds one from the head of its line through the merge. Operators iterate with TARGETED tests (`go test -p 2 ./pkg -run X`, `vitest <file>`,
`pytest path/to/test_x.py::name`; their launch env has `GOFLAGS=-p=2`) and run full suites only inside a slot; a
PreToolUse(Bash) hook `cmdr internal-operator-guard --role R` (`internal/operatorguard`, registered by
`agentrt.ClaudeHostOperatorSettings` on the host Operator's `--settings`, `agentrt.CodexHookArgs` on Codex) refuses bare `go test|build|vet ./...`,
un-targeted `npm test`/`vitest`, `npm run build|dist|smoke*|shots`, and Python suites (`pytest`/`python -m pytest`/
`uv run pytest`/`just test` with no `.py` file or `::` node id; `cargo test|build`, `make test`, `docker build`,
`ansible-playbook`, `molecule`) and root-wide scans (`find`/`bfs`/`fd`/`mdfind`/`locate`/`rg`/`grep -r` from `/`,
`/Users`, `/home`, `~`, `$HOME`; `Verdict.Scan`, its own message naming `git grep`/a named dir), logs to
`$COMMANDER_HOME/logs/operator-refusals.jsonl`, fails open. Takes effect on an Operator's next (re)start.

**br call limiter:** every `br` process goes through `beads.defaultRunInDir` -> `limitedRun` (`internal/beads/limiter.go`):
per project db at most 2 concurrent (`COMMANDER_BR_PER_DB`, `[concurrency] br_per_db`) and 6 overall (`COMMANDER_BR_GLOBAL`,
`br_global`), in this process by a FIFO queue (writes before reads, a cancelled waiter leaves, the 40s per-call timeout starts when
the call runs, queue waits over 5s log, rate-limited) and across processes by flock tokens under `$COMMANDER_HOME/br-slots`
(`<hash of dir>.<i>` + `global.<i>`, taken together and non-blocking with 25-50ms jittered retries; `COMMANDER_BR_HELD=1` skips them and is set in every br child's env). Fail-open: a token open/flock error other than would-block (unwritable `br-slots`, EMFILE) runs the call with the in-process limit only (logged at most once a minute); waits are capped at 3 min (`COMMANDER_BR_WAIT_MAX`, a Go duration): tokens then run anyway, the in-process queue runs a write without a slot and fails a read with "br queue wait exceeded". Tokens are not FIFO and writes-first is in-process only, so reads can starve under constant writes from another process (bounded by the cap). The limiter key resolves symlinks (`/tmp` = `/private/tmp`).
`FreshDetails` reads drop from 3 to 1 parallel batch when more than 2 calls of the project queue (`QueueDepth`); display reads are always 1 (`parallelism`). The spawn gate
checks (`requireWorkflowGate`, `openGateAbove`) retry a killed/timed-out read once; a still-busy read fails with "could not read the
workflow gate of X (busy), retry in a minute", and in warn mode `requireWorkflowGate` warns and lets the spawn go. **Reserved write token:** per db the tokens are `br_per_db` shared ones plus one `.w` token only writes take (a write tries it first), and the last global token is write-only (reads use all but the last; in process the same caps in `dispatchLocked`), so one write is always runnable while display reads hold the rest. A write waiting for tokens keeps a `<hash>.waiting.<pid>.<ns>` marker file in `br-slots` fresh (touched each poll; stale after 1s, removed after 1m); display reads (`details`, not fresh) take one db token at a time and call `YieldToWrites` before each batch (200-500ms naps while `WritesWaiting` or a write was seen in the last second). Token files carry a holder stamp (pid, command) that a write waiting over 5s logs. A queue/token wait that times out is `br is busy (N waiting, write not started); retry in a minute` (`beads.ErrBusy`/`IsBusy`: the call never ran, safe to repeat); `internal-spawn-operator` retries create/label/status steps 3 times (5s, 15s; `retryStep`), continuing on the task bead already made. Tests: `WritePriority*`, `ReservedToken*`, `BrConcurrency*`, `SpawnGateRetry*`. **Invented write-priority exercise:** `noteWriteSeen` fires only for a write that had to wait (in-process queue or token), never an idle one; a display `Details` call carries `WithYieldBudget(YieldBudget=1.5s)` so steady writes can never throttle a batch more than that in total. `DoorResponse.Busy` carries `ErrBusy` through the door (set by `ApplyOps` when nothing applied), so `beads.IsBusy` and the spawn create retry work against cmdrd; a lost answer is never busy. `retryStep` gives each try its own context (`spawnStepTimeout`). **Limit:** the in-process queue's waiting writes are visible only inside that process; other processes see a waiting write only through its token marker file (after the queue it is in, e.g. cmdrd's, lets it start waiting on tokens), so yielding to a write queued in another process lags until it reaches the token wait.

**Wake from sleep:** the minute loop (`reapHostRuns`) measures the WALL-clock gap between ticks (`sleepWatch`, `Round(0)`: macOS monotonic time stops during sleep); over 3 min, the runs that sweep closes go through `runSettler.afterWake`: vanished sessions and bare-shell exits without a final report are settled as died (bead open, owner none, respawn within the auto-respawn cap, Manager mailed "died while the Mac slept (gap N min)"), Commander gets one summary above 2. Invented wake regression: the first post-wake sweep closes a run, then its settle (`br update`) fails under load; the closed run row must not leave the bead working indefinitely. Failed settles retry each sweep (`settleRetries`). `batteryWatcher` (macOS `pmset -g batt`, injected reader) mails Commander once per discharge at or below 10%, latch clears on AC or above 20%. `sleepWatch` measures only the wait from the end of an iteration to the next tick (a slow sweep is not a sleep). Failed settles retry at most 10 times / 30 min; a retry re-reads the bead and drops when it is closed or claimed by another Operator, and a failure after a respawn (`partialErr`) retries only the remaining steps (never a second spawn). Heuristic: a run that exited with no final report right after a wake is treated as died (it may have been a user stop). Tests: `WakeFromSleep*`, `SleepGap*`, `SettleRetr*`, `SettleReaped*`, `LowBattery*` in `internal/daemon/wake_test.go`.

**Operator cap = per workstream:** `internal-spawn-operator` (and the scheduler) refuse a spawn when the step's
workstream (`beads.Root`) already has `[concurrency] max_operators_per_workstream` (default 8) running Operators
(`spawn.CheckWorkstreamCap`, counting running agent_runs by their task's workstream); a `max-operators:<n>` label on the
workstream bead overrides it (`cmdr internal-set-objective --bead <ws> --max-operators <n>|none`); with nested Managers on see that section for per-owner and subtree caps. There is no
per-project or global cap (`max_operators_per_project`, `max_operators_global`, `projects.*.max_operators` still parse,
ignored).

**Worktree gc:** `cmdr worktrees gc [--project p] [--apply] [--older-than 72h] [--archive] [--json]` (`internal/worktreelock/gc.go`,
`cmd/cmdr/worktrees_gc.go`) is a DRY RUN unless `--apply`; Managers/the user only (Operators refused). A candidate is an `op/*`
worktree whose runs are not running, whose task is closed (or run older than the threshold), not held, clean, and whose branch is on
main/origin. Unique unshipped commits = KEEP ("archive-needed" in the report) unless `--archive` writes `refs/archive/op/<branch>-<sha>`
first. `<project>/.commander/worktree-keep` (one shell glob per line, `**` any depth, matched on dir name, project-relative path, branch with/without `op/`, task bead id) and a worktree `.keep` marker always win. Ignored content outside rebuildable caches (node_modules, .venv, dist, build, target, `*.pyc`, ...) = KEEP `ignored content: <3 paths>`. `--apply` needs a dry run of that project from the last hour (`<project>/.commander/gc-dryrun.json`) and, with 100+ worktrees and no keep file, `--dangerous-no-keep-file`. Only worktrees under `<project>/.worktrees` or `<project>/.worktrees.noindex` (`.worktrees` may be a link to it; paths are compared as real paths, one entry per real path, keep globs and records may name either folder; tests `WorktreeGC*Noindex*`) with a matching run of that project are candidates (others kept: `outside <project>/.worktrees`, `no matching Operator run`); a failed `br list`, an invalid keep glob or an unreadable keep file fails the pass. `--apply` is bound to the reviewed dry run: the record (0600: paths, tips, flags) must cover every removal with the same `--archive`/`--older-than`/limit, else refusal; status, tip and run are re-checked right before each removal and the branch is deleted only at the decided tip. A dry run writes only `gc-dryrun.json` (listed in its output); gc touches no Spotlight setting. Held = `beads.Lineage` real parents; last activity = latest run start or when the run froze the worktree. At most 100
removals per run; `--apply` refuses a dirty/mid-rebase main checkout (a dry run only warns). `spawn.gitWorktreeAdd` has its own 3 min budget
and prunes only past 300 worktrees. Not run by cmdrd: housekeeping hook needs Commander's OK. **Batched reads:** one `for-each-ref` of every branch tip and one `rev-list --stdin --not main --remotes --glob=refs/archive/` for all `op/*` tips (a tip it prints is on none of them), runs indexed by task, parents from the one `br list` plus one batched `FreshDetails`, `git status` 4 at a time; only `git status` is per worktree (a constant ~11 other git calls per pass; `TestWorktreeGCGitCallsDoNotGrowPerWorktree`, `COMMANDER_GC_BENCH=1` runs 2,000). The pre-removal recheck still reads its one branch afresh. Tests: `WorktreeGC*`.
**Finished-run sweep** (`daemon/worktree_sweep.go`, every 10 min, separate from gc): prunes a finished Operator's worktree when its
task bead is closed and its work is on main. Statuses come from ONE `List` (status all) per project per pass through cmdrd's
cached export reader (`Daemon.reads`), never `Show` per run (an invented batch of 200 worktrees needs one List rather than 200 Shows).
Never a worktree a running run works in, by real `spawn.RunWorktree` path (a `--branch` follow-up takes over a finished run's worktree under a new name; a running run whose project cannot be read prunes nothing; `TestSweepNeverTakesAWorktreeARunningRunUses`). Each removal holds the worktree's claim (`spawn.TryClaimWorktree`, the flock a spawn holds from before it unlocks a taken-over worktree until its row lands) and re-reads the running runs under it; a claim held elsewhere or a failed read removes nothing (`TestSweepSkipsAWorktreeASpawnHoldsTheClaimOf`, `TestSweepRereadsRunningRunsUnderTheClaim`). A takeover re-resolves the branch checkout and checks that its directory still exists after acquiring that claim; if the sweep won first, it refuses with a retry message before starting anything (`TestTakeoverSweepScenario`, `tests/evals/scenarios/takeover-sweep.json`). Work kept by a `refs/archive/` ref counts as landed too (`worktreelock.Prune`: the branch is deleted only at that tip), so worktrees of a rebuilt main still go; the dir is `spawn.RunWorktree` and must pass `worktreelock.IsProjectWorktree` (`TestPruneRemovesWorkKeptByAnArchiveRef`). Test: `TestSweepReadsStatusesWithOneListPerProject`.
**Disk guard** (`internal/diskguard`, `daemon/disk.go`): `diskguard.Stat` (statfs; mount = topmost dir on the same device, paths deduped by device) reads the disks of the commander home, its `tmp` root, the system temp dir and every project path/repo. cmdrd's `diskWatcher` (every minute) raises health `disk:<mount>` at `WarnPct` (80) and `disk-full:<mount>` at `RefusePct` (95), Commander mailed once through the health board, both cleared when the disk drops. At the refuse line every spawn is refused naming the disk (`spawn.CheckDisk` in `Spawner.Spawn`, so crash restarts and the scheduler too; `internal-spawn-operator` before the task bead, `internal-operator-provider` before the old run closes; an unreadable disk passes). Env `COMMANDER_DISK_WARN_PCT`/`COMMANDER_DISK_REFUSE_PCT` (0-100] override. **Per-run scratch:** the host launcher sets `TMPDIR=$COMMANDER_HOME/tmp/op-<sha1(session)[:10]>` (`diskguard.RunTmpDir`, short for socket paths) and removes it when the agent exits (code runs; a knowledge run `exec`s, so only the sweep removes its dir); every 10 min cmdrd removes real directories of that root named exactly `op-<10 hex>` (`IsRunTmpName`) older than 1h that no running run owns (`SweepTmp`; any other file, dir or link there, an agent's script say, is never touched; an unreadable run list removes nothing). `checkRoot` refuses any root but an exact clean `<dir>/tmp`, not itself a link, whose parent is not `/` and neither is (raw or resolved) `/tmp`, `/private/tmp`, `/var/tmp`, `os.TempDir()` or the user's home (`COMMANDER_HOME=$HOME`): once a `/tmp/` root passed a looser check in a test and swept the machine's `/tmp`. **Tests never call `SweepTmp` on anything but a `t.TempDir()` root**; bad roots go to `checkRoot` alone. Nothing outside the root (`/tmp/cl.*`, `/tmp/tk.*`, other scratch) is ever touched. Takes effect after an install (launcher: for Operators spawned after it). Tests: `diskguard_test.go`, `TestDiskWatcher*`, `TestSpawnRefusedOnAFullDisk`, `TestSpawnHost_RunTmpDirIsRemovedWhenTheRunEnds`, eval `TestDiskFullSpawnScenario` (`scenarios/disk-full-spawn.json`).

**Hold:** label `hold` (`beads.HoldLabel`) on a task bead or any ancestor, set with `internal-set-objective --hold <reason>` / `--no-hold` (Managers, Commander; refused when `COMMANDER_ROLE` is `operator-*`), reason and who in a `hold by <who>: <reason>` comment. `internal-ship` resolves the branch's task (Operator run whose worktree has the branch) and refuses at the start AND right before the ff-merge (after the long verify) while the task or an ancestor is held; `beads.FindHold` reads fresh, 3 attempts, and a hold it cannot read counts as held. Tests: `ShipHold*`. In the snapshot only for a workstream root (`workstreams[].hold`, `hold_reason`; see the check-in section), not on the Graph or the board's lines.

**Shipping and push policy:** `cmdr internal-ship <branch> [--project p]`
(`internal/ship`, `cmd/cmdr/internal_ship.go`) ff-only merges a branch into main
of a local project (commander has no remote); an Operator runs it once tests are
green. It refuses unless the main checkout is on main, clean except `.beads/`,
and the branch is a strict descendant of main, the branch's own worktree has no
uncommitted/untracked files, and the exact branch tip (checked out in a temporary
worktree) passes `go build ./...` + `env -u TMUX go test -p 1 ./...` (+ app
`npm run typecheck` + `npm test` when `app/` changed; node_modules borrowed from
the main checkout). **`--dry-run`** (`Options.DryRun`) does all of it, queue, slot, lock, hold checks, replay onto a moved main and the checks, and stops before the merge: it prints `a real ship would move main X -> Y` and `nothing merged`, a refusal reads `ship dry run, a real ship would refuse: ...` (a stop or an expired wait, `ship.Inconclusive`, reads `ship dry run, no verdict on the branch: ...`), and it writes no `operator-shipped` event. A flag after a stray word is refused with the usage, so a `--dry-run` can never be read past into a real merge. An installed cmdr older than this refuses the flag. Tests: `TestDryRun*`, `TestShipDryRun*`, eval `TestShipDryRunScenario` (`scenarios/ship-dry-run.json`). **Verify footprint:** one ship verifies per machine (flock `$COMMANDER_HOME/ship-verify.lock`, held through the ff-merge, so a replay keeps its turn; `internal/ship/queue.go`). Order: ticket, then the build slot once the ticket is at the head of its project's line, then the lock, with both held to the end, so no slot wait runs under the lock and one stuck slot holder cannot stop other projects' ships. Cost: one idle slot per project with ships waiting on the lock; `internal-slot status` lists it as `cmdr internal-ship verify`.
First come, first served per project: a flocked ticket in `$COMMANDER_HOME/ship-queue/<project>-<hash>/` names the arrival order, a ship tries the lock only with no live ticket ahead, a ticket whose owner died has no flock and is skipped and removed (a crash cannot wedge it, a reused pid cannot hold a place); another project's ships have their own line and meet this one only at the flock. After the lock wait the ship re-reads main and replays first, so it never verifies a tree that cannot land. Main still moves under a held lock when someone merges outside ship: it replays and re-verifies, at most 3 times.
**Budgets:** the line, slot and lock waits share one bound, 4h (`COMMANDER_SHIP_QUEUE_WAIT`), and honour cancel. Everything under the lock runs under one hold deadline of 2h (`DefaultHoldBudget`), however often main moves, plus at most `pipeGrace` (10s) to end a step, so 2h is the worst case hold of one ship; a replay's verify runs on what is left. Inside it: 90 min per verify (`Options.VerifyBudget`, timer starts once the thread wait, check slot and checkout are done) and 15 min per short step (`DefaultStepBudget`: hold check, catch-up/replay, check slot, checkout, merge). Expiry names the step and budget, says the lock was given up and never reads as red; a timed-out merge says to check main first, since it may have landed. **Ending a step:** `runBounded` (every check and git call) runs the command in its own process group, kills the group at the deadline and when the command returns, and closes the output pipes after `pipeGrace` on anything that left the group, so a surviving child cannot outlive a budget (a step that exited 0 stays green). A signal to `cmdr internal-ship` cancels its context, which ends the group; a SIGKILL of cmdr leaves the check running, and the next ship may share its verify tree. A hold read that ignores its context is abandoned, the process ends soon after.
Before a verify a ship waits (message every 30s, up to 20 min, then starts) while the user's thread count (`/proc/*/task`) is above
`COMMANDER_SHIP_MAX_THREADS` (default 2400, 0 = off; also `[ship] max_threads`, env wins; `COMMANDER_SHIP_THREADS_WAIT` bounds it; the waits honour cancel, the queue wait prints its place and the holder pid/age every 30s); unit tests run `-p 1`, children get
`GOMAXPROCS=4` unless set. A red step prints the FIRST failing package and its first 40 lines; pthread_create/EAGAIN/cannot-fork is
labelled RESOURCE EXHAUSTION with a retry hint, and a package is exhaustion only with anchored runtime resource lines and no `--- FAIL:`/`panic:`/test-file lines, and any output no package line accounts for stays red; one exhausted package (alone among failures) is rerun alone once, keeping the first run's output and a 'retried package X: passed alone' line
(`internal/ship/resources.go`, tests `resources_test.go`). The verify runs with whatever `cmdr` you invoked (the installed one), so
this takes effect only after an install. It NEVER installs or restarts
anything. Installing (`just install`, `install/install.sh`, anything restarting
cmdrd) needs the user's OK given to Commander, who asks once per batch. Operators
(not Managers; the Manager guard denies pushes) push their own PR/feature
branches freely, `--force-with-lease` included; never main/protected branches;
merging to main of a product repo stays the user's (the `merged` gate). Prompt
contract: `prompts/assets_test.go` `TestPushAndInstallPolicyInPrompts`.

## Graph == beads

`cmdr graph check [--project p] [--json] [--snapshot f]` (read-only; allowed in
`/api/cmd` and the Manager guard) compares the app Graph with the beads: it reads
cmdrd's snapshot, fetches every bead + edge per project from `br`, and runs
`app/scripts/graph-check.sh` (esbuild bundle of `scripts/graph-check.ts`, needs
node on PATH and `app/node_modules`). The pure model is
`app/src/renderer/src/graphCheck.ts` (`expectedGraph` from br, `actualGraph` from
`buildCanvas`, `compareGraph`); exit 1 on any mismatch (status, nesting by the
parent-child edge, edges, gate bands and their inputs, muted closed inputs).
Work order: rows and boxes are listed by `workDepth` (blocks-edge depth, plan order within a depth), so a blocker is never drawn below what it blocks even when the blocked stage is hidden at that level (`compareOrder`, kind `order`). A bead labeled `superseded`/`retired`, or closed with a reason saying superseded, is `superseded` in the snapshot and not drawn at any level; a closed gate nested under a finished stage is folded away with it. A gate bead (label `gate`) is always a band; its children and the beads it waits
on are listed inside as inputs, closed ones muted. The snapshot nests by the
parent-child edge (`beads.Client.Parents`, cached per bead `updated_at`), not by
the dotted id. Tests: `app/test/graphcheck.test.ts` (sample fixture
`fixtures/graph-sample.json`). Snapshot and br move independently, so a bead
changing between the two reads shows as a transient mismatch: rerun.

**Plan rule `stage-done`** (`livedata/rules.go` `stageDone`): fires only for an ordinary open container with at least one done child and none open; never for a gate bead, a bead with an open user card (`needs-user` or a stored decision), or one whose only children are superseded (closing those would pass a user gate without the user). It also stays silent while the stage has an unpassed own gate (open or failed) or the stage or an ancestor waits on an open blocker (`OpenBlockerOf`; closed, parked, superseded and unknown blockers hold nothing): finished children do not pass a merge/CI check or a predecessor's decision. Tests: `TestPlanRules_StageDoneSkipsGatesCardsAndChildless`, `TestPlanRules_StageDoneWaitsForGatesAndDependencies`, eval `TestGatedStageDoneScenario` (read-only fixture: `cmdr board --snapshot tests/evals/scenarios/gated-stage-done.json --diagram demo-flow`).

**Plan rule `started-early`** (`livedata/rules.go` `startedItself`): judges the bead's own word (status `in_progress` or an owner claim), never the rollup: `DerivedLive` makes an open stage with done children `in-progress`, which no state write on the bead can clear. Rule tests build rows through `snapSteps` so `State` is derived, not hand-set. Tests: `TestPlanRules_StartedEarlyJudgesTheBeadsOwnState`, eval `TestStartedEarlyRollupScenario` (read-only fixture: `cmdr graph check --snapshot tests/evals/scenarios/started-early-rollup.json demo-flow`).

## One board

`livedata.Board` is the only derivation of what a row says (rows, tracks, NEEDS YOU with track
letters, Commander's line). cmdrd puts it in every snapshot as `display` after laying each running
native chat's state over the store's (`overlayChats`, from `chat.Engine.Live`) and filling `since`
for untimed states from the `board_since` table (`sinceTracker`). Text never holds an age: readers
format `since` with `FmtAge` (app: `fmtAge`). **Working names the live Operator's step first** (`board.liveSteps`, gated or not; a stopped, waiting or Broken step is not work under way), a live Operator's row keeps its age through the Manager's turns, and a Broken step beside a live Operator reads State missing with the live work in `why` (`brokenBeside`; a busy Manager reads "Manager picking it up", as on every stalled row, under the live work's age: `age_as` (first live step, no `+N more`), which `sinceTracker` keys on). **Liveness is a fact:** cmdrd puts `live` in the snapshot
(`SnapLive`: the Operators alive per `spawn.LiveRuns`, looked up at most every 10s, `daemon/liveness.go`; runs spawned
after the look count as live) and corrects claims before deriving anything (`MarkLiveOwners`, `withLiveness` in
`Board`): a claim on an Operator that is not running reads as stopped (owner_state died/finished), a wait on one holds
nothing, the watch lists only live ones; `owner_why`/`owner_since`/`restarts` on a step and `why` on a row/track/workflow
step say what cmdrd saw (the app shows the row's under its state line; `cmdr board` appends ` · why: ...`). A NEEDS YOU
decision whose prerequisite work a live Operator is on reads as that work (`heldByLive`); an open card outranks a live Operator on the same step or its internal task; a live Operator under a
closed step keeps its row working (`hiddenOperator`). No `live` (older cmdrd) = claims as recorded. Golden:
`testdata/board/liveness.json`. **Reported gap:** an Operator's process ends up to a minute before the sweep and its settle takes several writes, and all that time its bead still claims it with no wait, so in an invented late-settle regression a report could read as a death (Broken, "nothing holds X" beside live work, plan health). `live.reported[]` (`SnapReported`, cmdrd's `reportedRuns`, `daemon/liveness.go`) names a run that ended on its own (`Reaper.FinishedRunningRuns`: the sweep's own test, closes nothing, clears no marker: `stopMarked`, not `stoppedBy`) with a report (`readReport`, the settle's own test; read once per run) from the first 10s look, or `exited`'s start, until `settleOrRetry` sees the settle land; a settle still failing at `maxSettleAge` (30 min) goes off the list, so the bead speaks for itself. An entry keeps its first `since` while its row runs, so a stuck sweep still flags. Each look (no lock held across tmux and comment reads: others answer from the last look, its names and reports publish together) prunes an entry whose run row closed with no settle coming (`prune`: a runtime or account switch closes the row itself; the ceilings watchdog would too, though nothing calls its `Register` today); the sweep brackets its closes (`beginSweep`/`endSweep`) and a settle claims its entry (`claim`/`settling`), so a sweep's closed row keeps it through a failed attempt until the retry. `MarkReported` (rows, also in the invariant watcher's fresh read) and `reportedStep` (`withLiveness`) read such a step as the settle will leave it: review, on its Manager, `Manager to close` until since + `beads.ManagerCloseWithin`. `holds` refuses a step with the wait already, a live or other Operator, a closed/parked step, a user card or no owner on a step not in progress (a reopen or a settled death), and reads the Manager's owner label in either spelling (a dotted workstream's settle writes it dashed); an Operator ended from outside or with no report is never listed and reads stopped as before. Limit: a handoff leads its row's line as it does once settled, so a step truly nothing holds beside it shows in `broken` and the plan findings, not on that line. Tests: `TestBoardReported*`, `TestReported*` (livedata and daemon), `TestLiveOperatorsNoteFinishedReports`, `TestSettleHoldsTheReportedWindow`, `TestFinishedRunningRuns*`, `TestReportedThroughDaemonWiring` (real `New()`, private home, fake beads CLI), golden `testdata/board/reported-gap.json`, eval `TestReportedGapScenario`. The app renders `display` as is; `cmdr board
[--role manager-<bead>] [--json]` prints the same lines for agents. Golden fixtures:
`internal/livedata/testdata/board/*.json` (a snapshot and its clock) with `*.want.json`
(`TestBoardFixtures`; add a branch to `branches.json` and its expected display by hand).
**Workflow:** each display row carries `workflow` (`livedata/workflow.go`, derived in `Board`): `steps[]`
(`id`, `title`, `kind` = the board's colour class or `done`, `text`, `gate`, `who`, `track`, `children` (left out once
done), on a container `current`, on an open user decision `unlocks`) plus the row's `current` (the ask the board leads
with, else where work is now, else the next gate) and its `unlocks`. A track reads exactly as its sidebar row; leaves
read by who acts (Needs you, Operator working, Waiting for CI, Not started, Not reached, Parked, Done...). `unlocks` =
open steps whose every open blocker (own or a container's) is the decision or already started by it, in rounds,
stopping at (and listing) the next gate. App: `components/WorkflowView.tsx` renders it as is: the strip above a pitch's
question (done count, Deciding now, Approving starts) and the Workflow tab a sidebar workstream/track click opens
(`store.openWorkflow`); a cmdrd without the field shows neither. Agents: `cmdr board --workflow <n|bead> [--json]`
(derives locally when cmdrd predates it). Goldens: `testdata/workflow/*.json` (`TestWorkflowFixtures`, `UPDATE_BOARD=1`);
`checkWorkflows` holds every board fixture's workflows to its rows. App fixture `test/fixtures/snapshot-workflow.json`
(`node scripts/smoke.mjs --fake --fixture test/fixtures/snapshot-workflow.json --query ask=1`).

**Board time zone:** every clock time the board formats ("Waiting for reconcile · 07:04 PM", "Parked until Wed 07:04 PM")
is formatted by cmdrd/`cmdr board` itself in ONE zone, `livedata.BoardZone()`: `[user] timezone = "America/New_York"` in
`~/.commander/config.toml` (IANA name; `config.User.Location`), default the process zone, an invalid name logs a warning and uses
the default. cmdrd reads it at start (`daemon.go`, restart to change), `cmdr board` via `livedata.LoadBoardZone(home)`; tests use
`SetBoardZone`. No marks or later resolving: Rows, BoardLines, BoardEntries, WorkflowLines, the check-in and the app all show the
text as is (the Watching row and the Graph's Parked until stay on the browser zone). `GET /api/settings` carries the zone as `timezone`; My Progress formats cycle dates in it (empty or invalid = browser zone). Tests: `board_zone_test.go`.

**Diagram:** each display row also carries `diagram` (`livedata/diagram.go`, derived in `Board` from the same flow as the
workflow): the steps at one level (the workstream's, and in `tracks` each open track's) in blocks-edge work order, each with
the workflow's `kind`/`text` plus a `lane` (`you` | `agents` | `elsewhere`, `""` only when nothing holds it) and `who`, gate
`approvers` and `criteria` (the bead's acceptance criteria), `after` (steps of the level it waits on), and `findings`
(`CheckDiagram`: `unheld`, `lane-label`, `gate-approvers`, `gate-criteria`, `cycle`; advisory `gate-title`, `order`; plus `needs-you-lane` from `board.needsYouLane`, which needs the cards so it runs in `Board`: an open card drawn outside lane you or on a row/track row not reading Needs you; `heldByLive` and a gate behind an open blocker are exempt). Lanes
derive in leafLine's order (decision/needs-user, Operator/Manager, review fact, waits, gate approvers, owner); label
`lane:you|agents|elsewhere[:<who>]` (`internal-set-objective --lane`) only fills the resting case. Door
(`cmd/cmdr/door_checks.go`): a new gate needs `--criteria`, a "Greenlight" gate needs approver user, `--lane` must parse and
never goes on a gate, a `--decision` holding a ```cmdr-graph is refused. Agents: `cmdr board --diagram <n|bead> [--json]`,
`cmdr graph check <n|bead> [--json]` (exit 1 with findings). App: `components/DerivedDiagram.tsx` above the Workflow tab's
list and as a pitch's Execution workflow (`PitchView` drops drawn graphs; a cmdrd without `diagram` falls back to
ExecutionFlow). Goldens: `testdata/diagram/*.json` (`TestDiagramFixtures`, `UPDATE_BOARD=1`); `checkDiagrams` holds every
board fixture's diagrams to its workflows. Each step also has an `actor` (you | manager | operators | reviewers | automatic,
the ExecutionFlow lifelines); a gate waiting on someone other than its approvers is two steps (`<id>#wait` on the waiter's
line, then the gate on the approvers'); done steps come first; every track (parked and done too) has its diagram. The app
draws every header click and pitch with `SequenceDiagram` (ExecutionFlow), never the old list: the latest 4 done steps
always show, at least two lifelines, `fromWorkflow` when cmdrd sends no `diagram`, "Nothing planned yet" with no steps.

## Check-in report and sanity audit

The Commander check-in (`internal/daemon/watch_commander.go` `commanderPrompt`) always carries
`livedata.CheckinReport`: one line per workstream row (`livedata.Rows`, read from the snapshot's `display`
block), `Stopped or stalled (n; ...)` and `Screen mismatches (n)` when any, `NEEDS YOU (n)`, and
`Sanity audit: n suspects` (`livedata.AuditSnapshot`, pure; rules in `audit.go`: stalled with agent/wait,
next gate parked/closed/not earliest, flag iff red, NEEDS YOU not an open user gate, stale reviewer gate
(`AuditOptions.Approved` is the gh seam), open bead with no owner (snapshot form of the
open-bead invariant), done-state with an open bead, `needs-you-lane` (from the display's diagram findings, so plain `graph check` exits 1 on it too)). `cmdr graph check --audit [--snapshot f] [--json]` prints it
on demand (no br/node); plain `graph check` also lists suspects per workstream under `audit`.
**A workstream on hold** (root bead has `beads.HoldLabel`; a hold on a track or step changes nothing here): `livedata.Checkin` is the one split, read by `commanderPrompt` and `CheckinReport`.
Its stalled places, workflow errors and sanity-audit suspects leave the headline and the lists and are counted on one line under `Held (n; ...)`: `- Alpha (w) | 2 stopped or stalled, 1 workflow error, 1 sanity-audit suspect | reason` (reason flattened to one line, from the latest `hold by` comment; dropped if the snapshot has none; a held workstream always has its line). Quieted too, or the headline would not move: `commanderCandidates` raises no `:stalled:` or `:stuck` issue for it, and `checkPlans` raises (and keeps) no `plan:` health, so its Manager gets no `[health]` mail for it. Unchanged: review blockers and advisories (they never raised the headline), NEEDS YOU, health and degraded (still NOT all on track), screen mismatches (a ticket status Commander can fix, not attributed to a workstream), waiting-on-Commander, slow-CI and quiet-Operator issues, `moving`, the board and the app's badge. `AuditSnapshot`, `WorkflowFindings` and `StalledWork` stay whole, so `cmdr graph check` still lists a held workstream's findings and suspects and exits 1 on them; `--audit` text prints the split report. With no hold `Checkin` equals those three reads (`TestCheckinWithNoHoldIsTheUnsplitReads`). Tests: `TestHoldOnTheRootReachesTheSnapshot`, `TestCommanderCandidatesHoldQuietsOnlyWhatNobodyIsMoving`, `TestCheckPlansSkipsAWorkstreamOnHold`, held cases in `scenarios/checkin-headline.json`.

## Build & automated tests

```bash
go build ./...        # must be clean
go test ./...         # 31 packages; must be green before you commit
```

The **eval harness** (`tests/evals/`) builds the REAL `cmdr` binary, runs it in a
REAL tmux session, and swaps in `tests/evals/fakeopencode` — so it needs no real
opencode. Add a `scenarios/<name>.json` for each new Commander flow (input →
expected `reply_contains` / `action_contains` / window selection). This is your
end-to-end coverage; lean on it.

`COMMANDER_EVAL_TIMEOUT_SCALE` scales all harness deadlines (default 1; numeric
1–10). Ship checks default it to 3, preserve an explicit override, and run Go
test packages serially (`-p 1`) inside the existing build slot, so eval tmux
startups do not compete with other packages in the same run. Binary builds have
their own scaled budget before the scenario timer starts. Polling stays fast;
hung commands and missing replies still fail at bounded deadlines (the negative
`scenarios/deadlines/hung.json` fixture is tested at scales 1 and 3).

> **Never run `go test ./tests/evals` or tests that touch tmux with `$TMUX` set**
> (i.e. from inside a cockpit pane): run `env -u TMUX go test ...`. The eval
> harness's kill-session otherwise hits the real `commander` session.
> `cockpit-test.sh` runs on its own tmux server and is safe.

Every cockpit window (root, `manager (<p>)`, `op-<task>`) is three columns:
Workstreams (`cmdr __column cmdr-objectives [--project p]`) | chat | context
column. The chat pane carries the tmux pane option `@cmdr_chat=1` — find it
with that, not by index (windows built before the tag have chat first).

## Beads reads and writes

Each `br` process has a 40s timeout; objective edits and API commands have a 3m
budget. Idempotent label/status assignments retry once on killed/busy/locked
errors (never create). Objective edits validate and collect assignments before
one `br update` per bead; mail and NEEDS YOU synchronization follow that write.
Snapshot refreshes use the regular poll to coalesce write bursts. Display Details accept consistent JSONL up to 30s old, then use batched DB-backed
`br show`. Snapshot edges and gate evidence (`gates.All` / `ForStage`) use cached
`Details`; never force fresh evidence on every display poll. Enforcement uses
`beads.FreshDetails` (bypassing export and detail caches), or `gates.AllFresh` /
`ForStageFresh` for gate evidence; these retain the client's List policy.
Gate check/pass/close, gate mutation/migration, workflow approval, spawn guards,
track moves, ownership/cap lineage and objective edits use fresh details.
`checkReached` (`--state in-progress|needs-user`, `--waiting user`, spawn) names a blocker from cmdrd's snapshot, which can lag a write by minutes, and refuses only when `blockerHolds` confirms it: `FreshDetails` up the real parents for the blocks edge, `FreshShow` for the blocker still open (closed, parked and superseded hold nothing), then the next blocker the board names; a failed fresh read refuses as before. It adds no edge the board lacks. Tests: `TestCheckReachedConfirmsTheBlockerWithAFreshRead`, eval `TestStaleBoardRefusalScenario` (`scenarios/stale-board-refusal.json`). The CLI gate list shares the enforcement loader. Invariant mail reloads through its own `ExportReads(IsolatedIncremental(cached))` reader before deciding; a failed or spent read sends and clears nothing. Action guards retain `FreshDetails`/`ForActions`. List and Details share a fingerprint check, with
up to three attempts when files move. A failed project read keeps that project's
last good rows (including edges and moved parents), marks the snapshot degraded,
and does not advance its version; recovery replaces those rows. WebSocket events
still deliver degradation changes at the retained version.
In an invented slow-reader exercise, per-bead reads accumulate latency, so `Details` reads in batches of 20, one retry each (a display read one batch at a time, always; an enforcement read 3 parallel, 1 when more than 2 calls of the project queue); a display read that leaves
beads unread returns the rest plus `beads.PartialDetailsError` (project shown from what was read, flagged degraded, retried
next tick; `CachedClient` keeps the salvaged details while the files stand still), enforcement (`FreshDetails`) still fails
whole. `internal-workstreams` lists the healthy projects and warns on stderr (`livedata.OnlyProjectReadErrors`).
**Incremental display reads:** `CachedClient.Details` keeps each bead's Detail with a key from `br list --all` (`detailKey`: updated_at, status, dependency/dependent/comment counts, labels and its own dependency edges; bd may leave updated_at and counts unchanged on a blocker swap, so the list's dependencies are keyed too; the far end still relies on dependent_count). A fingerprint change costs one cached list plus `br show` for only the changed/new ids (`ChangedDetails`, never the export); removed ids drop; a failed batch keeps the old Detail and reports `PartialDetailsError`; the first read of a project is the full read. An entry taken from the export whose list key did not move but whose export did re-takes its Detail from the newer file under the first read's limit (`exportDetails`, `maxExportAge` 30s), so a moved export sends no unchanged bead to `br show`; a bead the file lacks, or a file past the limit, does go, which keeps the display lag bounded (`TestIncrementalDetailsMovedExport*`, `TestFreshDetailsSeesDBEdgeBehindDisplayExport`). A first read past the limit goes to `br show`, so it may not re-take: a flush that began before a write can land after it with older content. It owes a show instead (`detailEntry.owesShow`): once the file moves it goes to `br show`, once, which learns a change its key cannot see, since bd leaves updated_at alone on an edge swap (`TestIncrementalDetailsShownFirstReadIsNotRetaken`, `TestIncrementalDetailsPastLimitFirstReadLearnsAKeyStableChange`). A DB-backed entry also learns an edge swap when the list carries its own dependencies. Backends that omit those edges still rely on timestamps and counts. A re-take can still replace `br show` content by two routes: a bead the file lacked at the first read, and a first read of a file that failed to parse, both stamped and re-taken at the next moved export. A `noExport` read never re-takes (`TestIncrementalDetailsNoExportReadSkipsTheRetake`). In an invented restart exercise, a display load after a flush must not reread an entire 400-bead project; the case past the limit (a failed or rate-limited flush, a reader with no export client in front, or a direct cached-client caller on an idle project after a restart, whose owed read covers only the ids it asked for) still costs it at the first read and one more read after the file moves: at the first load to reach the store, since behind `ExportReads` a successful flush or an aged export that is not behind answers from the file and the debt waits, or earlier from a `noExport` read (`TestIncrementalDetailsOwedShowHappensOnce`). `Incremental` still shows every export-stamped entry once, holding its own read slot, and they are DB-backed afterwards (`TestIncrementalDetailsNoExportReadStillShowsExportEntriesOnce`). One such read runs per project at a time (`acquireReread`): overlapping loads (the poll, a settle, the 10s health loop) queue, then find the first's result in the store and read only what still moved. Not a shared result of the whole read: each read runs on its own caller's context, so one caller's cancel fails nobody else and a read that waited still sees a write made meanwhile (a settle needs that). Tests: `TestIncrementalDetailsOverlapping*`, `AWaiting*`, `ACancelled*`, `AQueued*`. Enforcement reads (`FreshDetails`) ask only for the ids they need (the workflow-gate check reads candidate first gates only). Tests: `IncrementalDetails*`. A List/Details read that overlaps a write (`beads.ErrChangedDuringRead`, after 3 attempts with 200-500ms jittered backoff) keeps the project's last-good rows with no degraded flag or mail, but a settle over such a load answers `settled:false` (`Snapshot.KeptLastGood`, off the wire; `TestWrite_SettledIsFalseWhenAnOverlappedReadKeptLastGood`); 5 consecutive misses of one project degrade it with "busy: beads changing continuously" (`livedata` `busyMissLimit`). `showOnce` does not retry after a deadline kill. Regression gate: `go test -p 2 ./internal/beads ./internal/livedata ./cmd/cmdr
./internal/api ./tests/evals -run 'BrTimeout|Retry|StateWrite|LastGood'`.

**Write door** (`internal/beads/door.go`, `txn.go`, `internal/api/write.go`): every `br`/`bd` write (`beads.isWrite`, except `init`) leaves a
client through `runWrite` -> `POST /api/write {dir, args | ops, atomic, env}` on cmdrd, same bearer token as the rest of `/api` (no cookie, so a
browser needs the token); this one endpoint is where write checks go. **No queue:** writes run concurrently (the backend is bd on a Dolt
server, safe for concurrent writers), bounded only by the br limiter caps (`br_per_db` 12, `br_global` 24 in `~/.commander/config.toml`);
cmdrd's own writes (`SetDoorServer`) take the same path. cmdrd answers once a snapshot loaded after the write is current (`settle`: the
wait is bounded by `settleWait` 20s, the load is not cancelled by it and runs under `refreshBudget` 3m, counted from when it holds `refreshMu` (time queued behind another load is not its own, so it never starts expired), so a timeout answers
`settled:false`; a load that outruns its budget publishes nothing (see Spent load below); queued waiters skip a load a later one already covered). Reads and validation
stay in the client. **Transactions:** a request may carry `ops` (a list of argv, `args` ignored) and `atomic`; the client call is
`beads.Txn(ctx, dir, ops, atomic)`. cmdrd applies it with `beads.ApplyOps`: when the backend is bd and every op fits `bd batch`'s narrow
grammar (`close ID [--reason R] [--force]`, `update ID --status|--priority|--title|--assignee V`, `create TITLE [--type T] [--priority N]`,
`dep add|remove A B [--type t]`, translated by `BatchScript`) the whole list is ONE `bd batch` = one Dolt transaction and one commit, rolled
back whole on any error (answer `atomic:true`, `applied:n`; a failure says "nothing applied (rolled back)", checked against a real bd by
`TestTxn_BDBatchIntegration`, `COMMANDER_BD_INTEGRATION=1`). Everything else (labels, comments, a create with `--parent`/`--labels`, br) has NO
transaction: with `atomic:true` the request is refused before anything runs (`ErrNotAtomic`); without it the ops run in order, stop at the
first failure and the error says how many stay applied. Batch limits: a created id cannot feed a later op of the same batch, values with
newlines are not translatable. **Batched-close regression example:** a batched close rides as `update ID status=closed [force=true]` (bd batch's own `close` skips the open-children/blocker refusal), so it refuses what `bd close` refuses (the refusal rolls back the whole batch) and `--force` overrides; a close with `--reason` is NOT batchable (no place for it; atomic is refused). Priorities must be 0-4. A request carries at most `beads.MaxOps` (50) ops and runs under one `writeDeadline` (2 min): later non-atomic ops do not start past it. A batch killed/timed out says "may have been applied", never "rolled back". Env passthrough is an allow-list (`USER`, `LOGNAME`, `COMMANDER_ROLE`, `BEADS_ACTOR`, `BD_ACTOR`, `BR_ACTOR`). `/api/write` takes the bearer header only (no `?token=`). `settled:false` also when the settling load was degraded or kept last-good rows; `settled:true` otherwise proves a clean load after the write, not more. **Door checks** (`internal/api/doorchecks.go`): a request whose env carries `COMMANDER_ROLE` (an agent; no role = the user, exempt; cmdrd's own writes carry none) is checked before it runs, mirroring the client rules judgeable from argv + a fresh read: typed waits (`waiting:*` must parse; slug `until` labels, PR-review timers, review waits with no `pr:` label refused), `in_progress` with owner user, `blocked` with no wait/open blocker, a new `gate` label without approver/`--acceptance`, Greenlight not approved by user. Refusal = `Exit 1` + `Error` (nothing written, whole request), logged to `$COMMANDER_HOME/logs/door-refusals.jsonl`; `COMMANDER_DOOR_CHECKS=off` in cmdrd's env disables; unreadable beads fail open. **Fail open:** a rule whose bead/deps read fails lets the write through and logs `logs/door-errors.jsonl` (identical lines once a minute); an ops refusal names the op (`op N of M`); `/api/cmd` strips `COMMANDER_ROLE` from the cmdr child env. Known limit: `--set-labels` (replaces all labels) is not checked. Not mirrored (need the board snapshot or CLI-only context): close-leaves-unheld, ask-not-settled, workflow gate, owner-live. Tests `TestDoorChecks*`. Other validation is not part of the door yet. **Fallback:** only when nothing was sent (dial
failure, no `api-url`/`api-token`, HTTP 401/404/503) the client runs the write directly (same `ApplyOps`) and logs
`$COMMANDER_HOME/logs/write-door-fallback.jsonl`. A request sent but not answered (cmdrd restart mid-call) is NEVER repeated; its error
says the write may still have been applied (a create or comment may show as failed while done: check before repeating; only creates
look it up themselves, see Client budgets). A write killed or timed out while it ran carries `DoorResponse.MaybeApplied` (the same
`ErrMaybeApplied` for the client, also on the direct fallback; `beads.MayHaveApplied` is the one test, never true for a busy write). **Attribution:** the client sends its `USER`, `LOGNAME`, `COMMANDER_ROLE`, `BR_*` and `BEADS_*` env (not `BEADS_DB`/`BEADS_DIR`/
`BR_DB`, never `COMMANDER_BEADS_BIN`; `DoorEnvKeep`) and cmdrd adds the allowed ones to the write's env. **Rollback:**
`COMMANDER_WRITE_DOOR=off` (also `0|false|no`) in a process skips the door (direct, no log); the old cmdrd without the route answers 404 and
clients fall back. **Honest limits:** any token holder can POST any write (incl. delete) for any absolute dir with a `.beads`, bypassing
CLI-side validation and gates, and agents can still run `br`/`bd` directly: nothing blocks that until the later step that moves them behind
the door. The Manager/Operator guards do not name `/api/write`. Tests: `Door*`, `Write*`, `Settle*`, `RunWrite*`, `Txn*` in
`internal/beads` and `internal/api`.
**Client budgets:** the bead-write verbs (`internal-comment|dep|phase|label|update`, `internal-create-objective`, the objective
edits) share `beadWriteTimeout` (3 min, `cmd/cmdr/internal_bead_write.go`), above the door's worst case (br 40s + settleWait 20s +
the door checks' reads); in an invented short-timeout exercise, a client can give up before cmdrd applies its write. A sent-but-unanswered door call
wraps `beads.ErrMaybeApplied`. `internal-create-objective` never reports a create that landed as failed: one that `MayHaveApplied` is
looked up for a short while (the one bead with that title under the parent created since the call, `findLandedCreate`, 4 looks over 20s),
and once the bead exists a failed label strip, Manager start or Commander mail is a `warning: bead X was created (do not create it again)`
on stderr and/or in the JSON `warnings`, exit 0. Links retry inside the verb (`writeLink`); a wait still missing is **exit 3**
(`exitUnlinked`) with the bead id, `complete:false` and `unlinked[]` (`waiter`, `on`, `finish`), so a `... --gate --before X && spawn`
chain stops; `finish` is `cmdr internal-gate link --project P --bead B --after|--before ID`, the verb that takes gate and track edges
(internal-dep refuses them), with the same cycle checks. Follow-up steps get a fresh `beadWriteTimeout`. `internal-spawn-operator` never
repeats an attempt whose create may have landed: it carries on with the bead `findLandedCreate` finds, else reports without repeating; a
task it cannot remove after a failure also ends the repeats; `spawnStepTimeout` = `beadWriteTimeout`. **Never someone else's task:** each attempt
labels its create `spawn-attempt:<16 hex>` and adopts only the bead carrying that marker, so a same-title spawn's task is never taken for its
own; a task with an `owner:` label, a running `agent_runs` row or an unreadable state is neither adopted nor deleted (`taskHeldBy`): it stays, the
attempt is not repeated and the report and mail name it. Spawn has its own 5-minute context. Tests: `TestTwoSameTitleSpawns*`, `TestSpawnNeverRemoves*`, eval `TestSameTitleSpawnScenario`, `TestCreateMarkerListsBack_BDIntegration`,
`TestBeadWriteVerbsOutwaitASlowDoor`, `TestCreateThatLandedNeverLooksFailed`, `TestUnlinked*`, `TestGateLink*`, `TestFindLandedCreate*`,
`TestSpawnCreate*`, `TestApplyOpsMaybeApplied*`. **Known, not fixed (A3):** `isWrite` (`beads/limiter.go`) counts the snapshot's `bd export`
as a write, so exports take write priority and the reserved `.w` token and a door write can queue behind one.

## Cockpit state API (cmdrd, `internal/api`)

The sidebar's data layer lives in `internal/livedata` (no bubbletea/lipgloss):
`Load` -> `Data`, workstream order, step tree, NEEDS YOU (+ decision cards),
agent pane scrape + hook merge, unread mail, and `BuildSnapshot` (the JSON).
`internal/tui` aliases it; put new data logic there, rendering in tui.
A load over 10s (`refreshLocked`, `slowLoad`) logs its time, each project's beads read, slowest first, and its phases (`LoadPhases`: `lock` = Source.mu wait, `pre-read`, `beads`, `gates` = attachGates, `store` = the other store reads, `build`) to cmdrd's log (`snapshot load took`); so does the 10s health loop's own load (`health loop load took`, `timedHealthLoad`). The invariant whole-project load has a 15s budget and a separate persistent cache/reread slot (`IsolatedIncremental`), so a slow DB-only hydration cannot hold the display slot. Completed show batches survive a timed-out pass and the next pass resumes; no mail or alert clearing uses spent/partial data. Backend limiter/write priority remains shared. Detail diagnostics log caller (`api-refresh`, `api-settle`, `health`, `invariant`), project, requested/stale ids, actual show batch attempts, cold/warm and noExport policy, slot wait and hold duration (slow/error reads and DB-only passes). Cancellation stops unscheduled batches. Regression: `TestIsolatedIncrementalRetainsProgressWithoutBlockingDisplay`, `TestInvariantBudgetSkipsMailAndRetainsEvidence`; eval `TestInvariantLoadIsolationScenario` (`scenarios/invariant-load-isolation.json`, in internal/api) proves a close advances snapshot version/time while synthetic 6219-row invariant hydration stalls. Tests: `TestRefreshLogsASlowLoadPerProject`, `TestRefreshLogsTheLoadsPhases`, `TestLoadPhasesTimeTheGateRead`, `TestHealthLoopLogsASlowLoad`.
`Source.Snapshot` may be called concurrently (the health loop runs outside `refreshMu`, beside the API's load): `Source.mu` covers everything but `Load`, so loads overlap and a slow one stalls no other caller, and building from a load is one at a time. Its callbacks run under that lock and must not call `Snapshot`. Limit: an older load that finishes last still builds last. Tests: `TestConcurrentSnapshots*` (`-race`, and a plain crash on `sinceTracker`), `TestSnapshotDoesNotWaitForAnotherCallersLoad`.
**Spent load:** a load whose own context ended (a settle's `refreshBudget`, a shutdown; `ctx.Err()`, so whichever read it ended in, LoadErr or not) is dropped whole: `Source.build` returns before it touches last data, the error, `OnSnapshot`, `since` or the board, and `refreshLocked` publishes nothing, leaves `loadKept` true (settle answers `settled:false`) and logs `kept the last board`. In an invented expired-load regression, publishing would falsely show no Operators and mail Commander urgent. A real store error (locked, missing, schema) arrives with the context alive and degrades as ever. A partial read with a live context is still published, flagged degraded. **Hung load:** only a settle's load has a deadline (Poll's and the health loop's have none), so a hung load parks them on `refreshMu`/in the load and no count of expiries would ever fire; `LastLoadErr` instead says `beads not answering: no load has finished for N` after `noLoadFor` (10 min since a load last finished, none before the first), which raises `degraded.json` and the Commander mail through the watch and stops the gate watcher. No load finishes meanwhile, so the app shows no banner, only a board that stands still. Not covered: a hang in state.db itself, since the watch reads the store first and parks there before this is read (SQLite busy still returns an error after 5s, so it takes something like a wedged disk); and a wedged tmux, since the minute loop lists panes with no timeout (`spawn.ReapHostRuns`) before it runs the watch, so no mail or `degraded.json` comes. The gate watcher skips its poll while `LastLoadErr` is set (a load hung in beads or in the load's own tmux scrape, on the default socket); a state.db hang parks it on its own `ListProjects` read instead, and with only the `commander-ops` socket wedged loads keep finishing and it keeps polling. Tests: `TestSettle_ALoadThatOutlivesItsBudgetPublishesNothing`, `TestRefresh_ACancelledLoadPublishesNothing`, `TestRefresh_ARealStoreErrorStillDegrades`, `TestSpentLoadReachesNoWatcherAndKeepsLastData`, `TestNoLoadFinishingForLongReadsAsDegraded`.
A failed load (`LoadErr`) deletes no `board_since` row (`sinceTracker.fill`): it proves nothing about a row it does not show, so ages survive until a clean load says the row left. Tests: `TestSinceKeepsRowAgesThroughAFailedLoad`, `TestSnapshotWithAFailedLoadKeepsBoardSince`, `TestSettle_BudgetStartsOnceTheLoadHoldsTheLock`.

cmdrd serves it on `127.0.0.1:8787` (`[api] port` in config.toml; a taken port
moves to the next, the chosen URL is in `$COMMANDER_HOME/api-url`). Every `/api`
call needs the token in `$COMMANDER_HOME/api-token` (0600):

```bash
H="Authorization: Bearer $(cat ~/.commander/api-token)"; U=$(cat ~/.commander/api-url)
curl -s -H "$H" $U/api/snapshot | jq .                   # full snapshot, .version bumps on change
curl -s -H "$H" -d '{"argv":["tab","ls","--json"]}' $U/api/cmd   # allow-listed cmdr verbs only
# WebSocket: $U/api/events?token=... sends {"type":"snapshot","snapshot":{...}} on connect + change
```

`/api/cmd` runs `cmdr` (argv, no shell) for `internal-{set,close,create,rename}-objective`,
`internal-resolve-needs`, `internal-open-workstream`, `internal-mail`, `tab
add|rm|focus|move|ls`; anything else is 403. Live-test it on a private home:
`COMMANDER_HOME=/tmp/cl.x/ch TMUX_TMPDIR=/tmp/cl.x AM_HTTP_URL=http://127.0.0.1:1
cmdrd --notifications=false` (short path: the unix socket must stay under ~104 chars).

## Desktop app (`app/`, Electron + electron-vite + React, a thin client over cmdrd)

The renderer is a plain web page: it reads host features only through the
optional `window.commanderHost` (config, Cmd/Ctrl+n, `.webview` inspector
bridge) and `window.commanderShell` (nav events, cmdrd health, dock), both from
the Electron preload; without them it is the page cmdrd serves at
`<api-url>/app/` (from `app/dist`, or `$COMMANDER_APP_DIR`) with the token from
its one-time `#token=` link (kept in sessionStorage; `&api=<origin>` points a
page served elsewhere at cmdrd). Same bundle, macOS and Linux. Snapshot -> view
model is pure (`src/renderer/src/viewmodel.ts`) and mirrors the TUI sidebar
rules; types mirror `livedata.Snapshot` in `src/shared/snapshot.ts` (keep them
in sync).

Layout (`src/renderer/src/`): navigator (Needs you on top =
`features/decisions` `<NeedsYouQueue>`, then Your agents by tier: spend/counts
header (was the Fleet footer), Commander, Workstream Managers (expand to
steps), Operators from `operatorsVM` with task, worktree, state, cost) | middle header +
`components/ChatSlot` (`features/chat` `<ChatPane>` attached to the selection's
live chat: `commander`, the workstream's `manager_role`, or an Operator picked
in the Operators tier; WS replay from `since=0` is the history.
`chatApi.ts` polls `GET /api/chat` every 3s into the store (`chats`,
`chatEngine`). NEVER offer Start for a session another process holds (it forks
it): status `owned_by` (or a 409 on start) shows the chat in terminal mode
(header badge "terminal session" + Move to app = the handover confirm;
`headless` = no action), with the composer: cmdrd answers send with
`delivered:"mail"` and the bubble says "delivered by mail to <role>" (raw "no
running process" errors are rewritten). Only an unowned stopped chat gets the
Start card. Running: header menu Pop out to terminal. Both handover/popout go
through `ConfirmDialog` and POST `{confirm:true}`; nothing starts on its own.
"chat engine not available" when `GET /api/chat` is 404/503) |
`components/InspectorColumn` (`features/inspector` `<InspectorPane>` for the
selected window, or the `<DecisionCard>` of the selected ask; picking a ⚑
step with an ask opens it too; the Decision view unmounts the pane so native
webviews never cover the card, and the column widens to 460px). cmdrd
clients are built from the store's config in `api.ts`. Main
(`src/main/index.ts`) hands lifecycle to `electron/app-shell.ts` (single
instance, menu, shortcuts -> `nav:expand-or-go` etc., window state, dock badge
= NEEDS YOU count, cmdrd health) and calls `registerInspectorIpc` per window.
cmdrd health (`electron/cmdrd.ts`): on macOS the app only `launchctl
kickstart`s the job and never spawns cmdrd (in an invented startup race,
a child with a bare PATH could take :8787 without `br` and show no workstreams); Linux
falls back to a direct start with the unit's PATH. The renderer counts an
accepted `/api/events` socket as live while it holds a snapshot: cmdrd's first
snapshot after a restart is built cold and can take minutes under load.
electron-vite outputs: renderer `dist/` (what cmdrd serves), main/preload
`out/main`, `out/preload` (electron-builder packs those three).

```bash
cd app && npm ci           # postinstall fetches the Electron binary
npm run dev                # Vite HMR + Electron auto-restart (scripts/app-dev.sh also ensures cmdrd)
npm run dev:web            # renderer only on 127.0.0.1:5173 (VM: ssh -L 5173:127.0.0.1:5173)
npm run dev:renderer       # the LIVE dev server: Vite HMR on 127.0.0.1:5173/app/ (strictPort; test with --port 5199)
cmdr app dev enable|disable|status   # keep dev:renderer always up (launchd org.commander-ui / commander-ui.service, log ~/.commander/logs/ui-dev.log)
cmdr app dev               # electron-vite dev --watch in the foreground (relaunches Electron on main/preload edits; opens a window)
npm test && npm run typecheck   # one vitest run: renderer+features (jsdom) and electron/ (node)
npm run test:layout        # real layout: build renderer to a temp dir, headless-measure the navigator on test/fixtures/snapshot-overflow.json (no sideways scroll, pills inside their row); npm test runs it too (test/nav-no-sideways.test.ts), skipped without a headless browser (smoke.mjs falls back to a system Chrome on Linux) unless `COMMANDER_LAYOUT_CHECK_REQUIRED=1`, which `internal-ship` sets on npm checks: then it fails, so an app-changing ship needs a browser (`$CHROME_HEADLESS_SHELL` or `~/.commander/browsers`)
# Queue geometry (after app build): node scripts/queued-message-check.mjs; uses
# tests/evals/scenarios/queued-message-layout.json, dark/light at two sizes, the short
# demo reply and reply=long (taller than the view). The Jump row keeps its height at
# latest (only the button hides), so reaching the bottom never resizes or moves the view.
# Headless frames stop after a demo send (pre-existing): judge layout, not the PNG.
# Go entry: COMMANDER_APP_LAYOUT_EVAL=1 env -u TMUX go test -p 2 ./tests/evals -run TestQueuedMessageLayoutScenario
# (scaled deadline capped at the test's -timeout less 30s, Ctrl-C/SIGTERM end the run; the group gets SIGTERM,
# 3s to run its handlers, then SIGKILL; check.mjs passes a signal on to smoke.mjs and waits, smoke.mjs ends its
# detached Chrome; TMPDIR + NoLeakedProcesses as a backstop. Tests TestQueuedMessageLayout{Stop,Grace,Interrupt,Budget}*).
# Demo Manager shots: --offline --query 'fixture=chat&ws=1&agent=manager';
# fixture=chat uses its own live connection even with --offline (no cmdrd).
npm run build              # electron-vite: dist/ + out/
npm run shots              # SEE THE APP: build + shoot dark/light/glass headlessly -> out/shots/<theme>.png (~3s)
npm run smoke              # build, load dist in chrome-headless-shell against the REAL cmdrd -> out/smoke.png
npm run smoke:offline      # fake cmdrd sends test/fixtures/snapshot.json then dies -> offline banner
# Every smoke mode fails on a thrown error, an unhandled rejection, console.error or an error the browser logs
# (a failed load); layout-check.mjs runs on it, so an app ship does too. `expectedPageError` in smoke.mjs names the
# few expected ones by route, most only for the fake cmdrd. A screen that reads a new route under --fake fails until
# it is named there with its reason; any write to the fake cmdrd fails, except Overview's POST /api/overview, which only reads (`isWrite`). Errors count until 2s after the shot, later ones go unseen.
# Any browser call unanswered for 30s (--answer-ms) fails naming the call, with the page errors it has; on a page that never connected it reads `expected conn=...` first. Test: test/smoke-page-errors.test.ts.
# Known limit: on a page that takes seconds to connect, the browser's Runtime.evaluate poll stalls under `cmdr workflow review-exec` (12 of 12 runs, 0 of 8 without; cause not known), and that test passes either way.
# npm run shots uses the fixture only when no cmdrd answers (`/api/settings` in JSON; a stranger on a stale api-url is none); a failed shot of a cmdrd that answers exits 1 and names its source.
node scripts/smoke.mjs --served   # the page cmdrd itself serves at /app/ (cmdrd must run the new build)
npm run dist -- --mac dir --arm64  # package -> release/mac-arm64/Commander.app (electron-builder last in `dist`)
cmdr app install           # npm ci + dist + copy to /Applications (CMDR_APP_INSTALL_DIR + COMMANDER_HOME for dry runs)
cmdr app                   # ensure cmdrd (launchd/systemd, else spawn), open the app (packaged or npm start)
cmdr app --web             # print <api-url>/app/#token=...; default on Linux without DISPLAY (SSH -L 8787)
```

**Live UI (the headline loop).** the user's installed app loads the renderer dev
server (`http://127.0.0.1:5173/app/`) instead of its `dist/` bundle whenever it
answers with a Vite page (`electron/renderer-source.ts`: probe at startup and
every 3s, back to the bundle when it dies; View menu "Renderer: Live (dev
server)" / "Bundled"; no badge in the page).
cmdrd's `/app/` proxies to it the same way (`internal/api/appdev.go`,
`$COMMANDER_APP_DEV_URL`, `off` disables; same env in Electron main). So an
edit under `app/src` reaches the user's screen through HMR in under a second: no
build, reinstall, restart or revert. Main/preload/`electron/` edits do NOT
hot-apply (they need `cmdr app install` or `cmdr app dev`). The dev server
serves under base `/app/` (Vite redirects `/`); the build stays `base: './'`.
Renderer Vite config is shared: `renderer.config.ts` (electron-vite build and
`vite.renderer.config.mts`). Never take port 5173 for your own tests (it is
the user's live server): `npx vite --config vite.renderer.config.mts --port 5199`.
If :5173 is down the bundle loads silently (no error UI; probe timeout 500ms).

"The app broke"? Read `~/.commander/logs/app.log` first (`electron/app-log.ts`):
main-process uncaught errors/rejections, renderer console warnings/errors,
`render-process-gone`, unresponsive, failed loads (a dead dev server is INFO),
and every Live/Bundled switch.

**Seeing the user's actual window:** `just app-shot` (scripts/app-shot.sh) saves a
PNG of the real Commander window, without focusing it, to
`~/.commander/shots/app.png`; read it. Use it whenever he says something looks
wrong: a long-lived window can differ from a fresh page (hot-reload state).

**Seeing the app without Screen Recording:** `cmdr app shot` (also
allowed through `/api/cmd` and the Manager guard; read-only) asks cmdrd
(`GET|POST /api/app/screenshot`, `internal/api/appshot.go`) to get a capture from
the open app window and writes `~/.commander/shots/app.png` +
`app-text.txt` (visible text, 30k chars max), printing only the paths and source. The text is the user's private queue and
chat: grep the section you need from the file, never cat it whole; `--text-stdout` prints it (`--text` is a no-op now). The renderer
(`appCapture.ts`, `useAppCapture`) holds a socket to `/api/app/connect` (newest
window wins) and answers `capture` requests within 10s: the PNG comes from Electron's
`webContents.capturePage` via `commanderShell.capturePage` (IPC `app:capture`,
`electron/capture.ts`; needs `cmdr app install` and does NOT include the native
inspector views), else from a DOM render (`html-to-image`, `source: dom`), which
works at once over HMR with no install. No window connected = 503
"no app window connected". cmdrd must run a build with the endpoint (restart).

**Page capture:** OFF by default (Settings > Browser capture toggle, localStorage `cmdr.capture.on`; the note there says the
screenshot and text go to the model and live in the chat log and Claude's transcript, the sweep deletes only the uploads copy). When on, a message to
Commander/a Manager sent while a web tab shows carries that tab's screenshot (<=1280px) + `innerText` (<=20k) as attachments `capture-<14-digit ts>.png|.txt`
(cmdrd saves them under `uploads/<role>/` 0600; a send over the 10-file cap drops the captures, not the message) plus a line in the `<user-screen>` note naming
origin + path only (no query, fragment or title). `chat.SweepCaptures` deletes only files shaped `<stamp>-<n>-capture-<14 digits>.(png|txt)` older than 24h, on a
capture upload, at cmdrd start and hourly (`RunCaptureSweep`). The chat log keeps only the attachment path (the image base64 goes to claude's stdin, not the log).
The decision (tab id, block list, expected origin+path) is snapshotted at Send (`pageCapture.snapshot`), not when a held message drains. Electron main
`inspector:capture` (`electron/inspector/capture.ts`) reads only the SHOWN view, refuses a changed address (vs `expect` and across the screenshot) and a URL on the
block list (`src/shared/captureBlock.ts`: patterns go through `new URL` so a pasted scheme, port, trailing dot, case and IDN/punycode all match; paths are decoded,
slash-collapsed and also matched against `#/route`; an unreadable line is flagged in Settings and blocks everything; host blocks are the reliable form). Renderer
`pageCapture.ts` (2s timeout, never for Operators, none outside Electron; `?capture=demo` only in dev builds); the composer shows "Attaching what <host> shows"
with a per-message "Don't attach". Needs `cmdr app install` (main/preload). Screenshot: `smoke.mjs --fake --query 'fixture=chat&capture=demo'`.
Tests: `capture.test.ts(x)`, `captureBlock.test.ts`, `pageCapture.test.ts`, `TestSweepCaptures*`, `TestSaveUploadsDropsCapturesOverTheCap`

**Inspector sign-in (`electron/inspector/policy.ts`, `guard.ts`):** `AUTH_HOSTS` (Microsoft, AWS `awsapps.com`/`signin.aws`/Cognito, Okta, Auth0...) always stay in the tab/popup child window (`AUTH_PATHS` match on whole path segments). `decideNavigation` keeps only home's site and listed hosts; every other way in is a session. A sign-in on an UNLISTED provider is a SESSION (`decideHop`/`decidePopupHop`, invented catalog sign-in regression), held only by an https target off home's site and off the list, never on a home where people post links (`newSession`, the one place that says who may hold one; `postedContentHome`: the sites of `AUTH_PATHS`/`SITES` plus gitlab/bitbucket/atlassian.net, which sign in through listed hosts anyway). `startSession` is the one decision. It starts when a listed host hands the tab to an unlisted https host, with no click needed because SAML hand-offs are often server redirects and the question is the gate (`handedOff`, also moves a running session; a crafted tenant link gets a question naming the attacker's host); when the user (click or key in the page within `GESTURE_MS` 5s, from `input-event`; a redirect inherits the click of the navigation it redirects; a sign-in window inherits the click that opened it), on the tab's own site, opens an https login path (/login|/signin|/sso|/oauth|/auth) whose return key (`redirect*`/`return*`/`next`) names the tab's EXACT home origin; when the user's click takes the tab from its own login page to an unlisted provider (with no click these two need the home+provider pair already trusted, else the browser with no question; http always goes to the browser); or, with no click, from a redirect of a load we started (`ownRedirect`: followed unjudged, so the page is in the tab anyway and the session only keeps its host's next steps). While it runs, every step and popup stays in only on the provider's host (https), a known sign-in host or home's site (a popup follows `decidePopup` for the latter two); home's site ends it, a listed host may hand it on (a new question), anything else opens in the browser and ends it, as do 10 min (`SIGN_IN_MAX_MS`) or 20 steps (`SIGN_IN_MAX_HOPS`). **Asked once per home and provider:** the first step on a session's provider stops on a native question ("Continue to <host> in this tab?": neutral, since a plain link cannot be told from a hand-off) leading with the registrable domain, then the exact host (`providerToConfirm`, `guard.ts` `settle`, `trust.ts` `providerGate`/`askInWindow`/`providerDialog`; default and Esc = Open in browser, Continue here = yes; parented to the sign-in window that asked, else the main window; a throw is a no). A yes is remembered in `<userData>/trusted-sign-in-providers.json` as `{home, provider}` pairs of exact hostnames (old bare entries read as not trusted; edit or delete it to be asked again; no UI). A no opens that page in the browser, ends the session and is remembered until the USER acts, not until the page moves (`NavState.refused`: one record shared by a tab and all its child windows, emptied in place; its time takes back every click made before the no, in all of them). Until then a page acting alone gets no question, opens no browser window by any route, whichever host it names, and opens no child window (a popup with no click is denied; a click still opens one; before any No nothing changes); moving inside the app still works. The user's act: our load or the user's reload (`ours`, `oursKeepingSession`; crash recovery is `recovering` and ends nothing, a page can crash itself) or a page's step under 5 seconds after a click or key in that same page (`acted`), which empties the record for all of them. Such a step is judged afresh: on the refused host it asks again, to a real provider it asks for that host, to anywhere else it opens the browser. A page acting more than 5 seconds after a click, while a No holds, is stopped; the way out is a reload or a typed address. One question at a time across all windows (`asking` in `trust.ts`, per process): a second attempt gets `busy`, the browser, and nothing remembered. `showMessageBoxSync` on purpose: `window.open` needs its verdict before it returns and a stopped form post could not be re-sent, so the main process holds for that one question (real event behaviour unproven: no test launches Electron). A session from `ownRedirect` asks at its next step on that host, not at the redirect, so a typed short link raises no question until its page moves on. A listed host, home's site and `postedContentHome` homes (no session there) never ask. Same-user agents can write that file (threat model above). `guard.ts` (Electron as types only; `guardTab`, called from `views.ts`) runs the same rule on will-navigate AND will-redirect for the tab and for each sign-in child window (which inherits the session it was opened for); redirects of navigations we started (loadURL, back/forward, typed URL, a changed tab address: `ours`, which also ends a session; reload: `oursKeepingSession`; crash recovery: `recovering`) are not judged while nothing is held, so a same-site link that 302s to another site now opens in the browser. Not kept in: a login page that auto-submits to an unlisted provider not yet trusted with no click, any federation on a posted-content home, and a provider moving to a second unlisted host except through a listed one (`console` to a regional console). Costs: any link off a listed page to an unlisted host asks too, and each user click can raise one more question. **Sites:** `registrableDomain` is a public suffix lookup with the private section (`tldts`, main process only, pinned exact; ~130 KB min, 3.6 MB on disk with its core and sources; the list ages with the pin, and a suffix it does not know reads as one site, which can be worse than the old two-label guess, which split some of them by accident), plus `LOCAL_SUFFIXES` for hosting suffixes not on it (the longer of the list's and the local reading wins, so `up.railway.app` stands under `railway.app`; never `atlassian.net` or `slack.com`, `postedContentHome` compares `siteOf` on them, so their tenants stay in as a known limit; it names only suffixes met so far, so any other short name under a country suffix neither list carries reads as one site (`a.xyz.io`/`evil.xyz.io`, a known limit; the old guess stays gone, it split `hex.pm`/`sso.hex.pm`); a region label under a suffix, `a.usw2.devtunnels.ms`, reads as the name, so that region's tenants are one site; an entry also splits the platform's own site, `gitpod.io`/`api.gitpod.io`) and `EXACT_HOST_SUFFIXES` for IP-embedding DNS (`nip.io`, `xip.io`, `sslip.io`: a host under them is only itself; a bare suffix, IP or single label likewise, compared exactly). So `a.vercel.app`/`evil.vercel.app`, `alice.github.io`/`evil.github.io`, `10.0.1.5`/`8.8.1.5` are neighbours: a page goes to the browser, a sign-in gets the question, a trusted pair stays with its home. `siteOf` is that plus `SITE_ALIASES` (every `*.notion.site` stays Notion's; known limit: from a Notion tab `evil.notion.site` stays in with no question and no session); the dialog leads with `questionLead`, never an alias (`evil.notion.site`): `registrableDomain`, or under `EXACT_HOST_SUFFIXES` the suffix, the only part nobody picks (`nip.io (accounts.google.com.8.8.8.8.nip.io)`); display only, the boundary stays exact. Tests: `policy.test.ts`, `guard.test.ts` (fake emitter), `trust.test.ts` (temp file), `views.test.ts` and `ipc.test.ts` (mocked electron). A sign-in child window opens no windows: its popups are denied, and a non-sign-in one opens in the browser. The share chip reads `Sharing your window` (no site name) unless the page is on the block list. Main change: needs `cmdr app install`.

**Seeing the app:** `npm run shots` (or `just shots` from the root) is the
one command for "what does it look like right now": it builds, shoots the real
cmdrd (fixture if cmdrd is down) in every theme, and prints the PNG paths. Read
them after every visual change. `-- --query 'ws=2'` (or `ask=1`, `fixture=chat`)
shoots another screen, `--themes light` one theme, `--no-build` skips the build.
`node scripts/smoke.mjs --offline --query fixture=chat --eval '<js>'` runs JS in
the page before the shot (e.g. type + send in the composer to see the mail note);
Keep captures local: archived cockpit screenshots may expose workspace data.

Theme: three surfaces, no rules between them: `--desk` (navigator, inspector),
`--sheet` (the chat column, the one raised card), `--raised` (tool cards,
composer). Structure comes from tone and spacing; do not add borders or
divider lines, and keep labels in sentence case. Every color is
`light-dark(light, dark)` in `styles.css`; the switch in the navigator title
bar (`theme.ts`, saved in localStorage) sets `<html data-theme>` to light, dark
or glass (none = follow the OS), and `?theme=` forces one. Glass makes the
surfaces translucent: in Electron on macOS the window has `vibrancy` under a
transparent page (`data-vibrancy`), in a browser it sits on an ambient wash.
The chat (`--cc-*`), inspector (`--insp-*`) and decisions (`--ctp-*`) take
their tokens from the app there, so new colors go in the app tokens, not in a
feature.
No Tailwind/shadcn (see `docs/plans/2026-10-20-assistant-ui-eval.md`).

`?fixture=chat` needs no cmdrd: the fixture snapshot plus a fake chat engine
(`demoChat.ts`: cmdrd wire frames through the real `EngineMapper`); add `&ws=n`,
`&demo=web`, `&theme=`. cmdrd's chat frames
(`chat.Event`) are mapped in `features/chat/engine.ts`, not passed through.

Screenshot fixtures for the built page (no Electron): `?ask=n` opens the nth
NEEDS YOU card, `?ws=n` goes to workstream n, `?demo=web|electron` fakes the
inspector's tab list (electron: a stand-in drawn at the native view's rect).
Serve `dist/` on 127.0.0.1 and shoot
`index.html?ask=1#token=<api-token>&api=http://127.0.0.1:8787` with
chrome-headless-shell (`--force-dark-mode --blink-settings=preferredColorScheme=0`
for dark). Keep generated screenshots outside tracked files.

**Never pop windows on the user's screen.** Smoke = chrome-headless-shell from
`~/.commander/browsers/chrome-headless-shell/` (`$CHROME_HEADLESS_SHELL`), never
Google Chrome.app, Electron or Commander.app. `npm run smoke:electron` /
`smoke:inspector` launch hidden Electron and are opt-in; avoid them on the user's
Mac. Read the PNG after every visual change. Electron's file:// page sends
Origin "null", which cmdrd refuses, so main rewrites Origin on API requests
(`allowApiOrigin`). npm here blocks dependency install scripts; the root
`postinstall` still fetches Electron. Linux: AppImage needs libfuse2
(`libfuse2t64` on 24.04) and cannot be built on Apple silicon (build it on the
VM); if the AppImage dies on the user-namespace sandbox, use the deb.

## Run & test it live — `scripts/cockpit-test.sh`

The cockpit runs as a **detached** tmux session, so you verify it with zero
display and without disturbing the user's screen.

```bash
scripts/cockpit-test.sh up            # create/refresh the detached cockpit
scripts/cockpit-test.sh text          # capture every pane as text  ← AUTHORITATIVE
scripts/cockpit-test.sh keys "Down"   # drive it (then re-run text to assert)
scripts/cockpit-test.sh keys "hi" Enter
scripts/cockpit-test.sh down          # tear down
```

The verify loop: `up` → `keys ...` → `text` → assert the captured text changed as
expected. That's how you "see" the cockpit.

### Seeing colors (you can — don't say you can't)

`text` shows structure, not color. To judge contrast, selection bands, hover and
solid backgrounds, render a pane to a PNG and Read it (you are multimodal):

```bash
swiftc -O scripts/ansi2png.swift -o /tmp/ansi2png          # once
tmux capture-pane -p -e -t <pane-id> | /tmp/ansi2png --out /tmp/pane.png --bg '#181825' --fg '#cdd6f4'
```
`--bg` is the pane's window-style background (tmux does not put it in captured
cells; chrome panes are `#181825`, the chat pane `#1e1e2e`). Use a private tmux
server (`TMUX_TMPDIR=/tmp/cl.x`, `unset TMUX`) when you don't want to touch the
live cockpit, or capture live panes read-only. `screencapture` of the real screen
needs Screen Recording permission this shell doesn't have; the renderer doesn't.
Do this after every visual increment, before reporting it done.

### Inspector page pictures (kitty graphics)

`web` tabs and `cmdr tab add --url U --renderer browser` show a headless-Chrome
screenshot (`internal/inspector/browser.go`, throwaway profile under
`$COMMANDER_HOME/inspector`, PNG cached in `inspector/shots/`) drawn with kitty
**unicode placeholders** (`internal/kittyimg`): the PNG is uploaded once via DCS
passthrough (tmux `allow-passthrough all`, set in `ensureCommanderSessionChrome`),
and the body is rows of U+10EEEE cells, so `capture-pane` shows those glyphs, not
the page. j/k crop a 2x-tall shot. Detection: `$CMDR_GRAPHICS=kitty|off`, else
Ghostty/kitty env or tmux `client_termname`; cell px: `$CMDR_CELL_PX=WxH`, else
tmux `client_cell_*`, else 9x21. `CMDR_KITTY_MEDIUM=file` sends a path, not data.
Without graphics or Chrome (`$CMDR_CHROME`) it is the link card plus a hint. `B`
opens `carbonyl` in a tmux popup if installed. Only Ghostty shows the pixels: to
check bytes headlessly, attach a client under `script -q log tmux attach` and grep
the log for `ESC _G`.

Logged-in shots: `cmdr browser login [--url U]` opens a HEADED Chrome on the
persistent profile `$COMMANDER_HOME/browser-profile` (0700); the user signs in and
quits Chrome (⌘Q). Once that dir exists, shots use it instead of a throwaway
profile, serialized by flock on `browser-profile.lock` (12s wait). Chrome allows
one instance per profile: a live `SingletonLock` (login window open) makes a shot
show the cached PNG (or a throwaway shot) with "login window open".
`inspector.HasSession(profile, host)` reads a copy of `Default/Cookies` (5s
cache; Notion=`token_v2`, GitHub=`user_session`, Datadog=`dogweb`, Slack=`d`,
Linear=any cookie) and drives the "signed in"/"not logged in" hint, `cmdr
browser status`, and `Fetcher.Renderer`: Notion/Linear URL tabs without an
explicit renderer become pictures when signed in + graphics + Chrome. Tests use a
fake Exec / `browserLaunch` and a fabricated cookie DB; never open a headed
Chrome in tests.

### Do NOT disturb the user's monitor

`up`/`text`/`keys`/`down` are pure tmux (detached, one private `-S "$TMUX_TMPDIR/cockpit.sock"` socket, `$TMUX`
unset; cmdr reaches it through a `tmux` shim on the isolated PATH) and never touch the compositor or the
default/`commander-ops` servers. There is no `shot` subcommand (removed); use `text`, or render a pane with
`ansi2png`. Test: `scripts/cockpit-test_test.sh` (fake tmux, starts no real server).

## House style

- Defer to `~/.claude/CLAUDE.md` (global prefs): simple, explicit, minimal diffs,
  conventional commits (`feat:`/`fix:`/`refactor:`/`docs:`), tests for new code.
- Go: standard `gofmt`; follow the patterns already in `internal/`.
- The backend (`internal/{spawn,store,beads,layout,...}`) is UI-agnostic and
  individually unit-tested — keep it that way; don't couple it to tmux rendering.

## Updating this file

This file is the **single source of truth** for working here, and it is expected
to drift unless you maintain it. When you learn something the next agent needs —
a new dependency, a changed launch flow, a testing gotcha, a new
`cockpit-test.sh` capability — **update this file in the same change**. Keep it
terse and accurate; a stale instruction is worse than a missing one. If you find
something here that's no longer true, fix it before doing anything else.

## Local integrations

Installation-specific integration settings live outside the repo in
`~/.commander/local.toml` (beside `config.toml`, respecting `COMMANDER_HOME`).
`[integrations.linear] workspace` supplies ticket links through `GET /api/settings`;
absent/empty settings leave ticket IDs unlinked. Reload the app after editing.
Sprint reads: `internal/linearsync` caches one viewer/active-cycle GraphQL read
for five minutes (singleflight), invalidated on key-file presence/mtime changes; authenticated `GET /api/linear` is separate
from the snapshot. `[integrations.linear] team` names the team to read (unset or
empty = off). The team key is also the only ticket prefix board text drops from titles
(`livedata.SetTicketPrefix`/`LoadTicketPrefix`, app `plainTitle` via `linear_team` in `/api/settings`): `(TEAM-12)`
goes, `(UTF-8)`/`(SHA-256)` stay; no team = nothing dropped. cmdrd reads that prefix at start (restart it to change; the app and
`cmdr board` re-read it each time). `cmdr linear set-key` takes stdin or a hidden prompt and stores
only `$COMMANDER_HOME/linear.key` (0600). Both agent guards refuse commands
naming the credential file (filepath.Match globs against bare names or expanded home paths; source globs stay readable) and `cmdr linear set-key`; fetch
panics return unreachable and release waiting readers. `cmdr linear status` is a local pure
read, allowed through the Manager guard. The app Settings > Connections has a password input (cleared at send) and Replace/Remove;
authenticated POST/DELETE `/api/linear/key` atomically save/clear the same private file.
GET `/api/linear` adds only `key: saved|not set`. Keys reject whitespace and values over 4096 bytes;
route errors never echo bodies. The app lazily polls at most once a minute while visible,
joins title ticket IDs onto navigator workstream/track rows and focus headers, and marks
ticket/step completion disagreements in amber without changing either. Fixture verification:
`cmdr internal-slot run -- npm run shots -- --fake --fixture test/fixtures/snapshot-linear.json --themes dark,light`
from app; screenshots stay in out/shots. Key safety: `TestLinearKeyPrivate`; app:
`src/features/linear/sprint.test.tsx`. The app and cmdrd only read Linear (Commander updates statuses itself, d27). Shared transport:
`internal/linearapi` (also used by the inspector). Test with fake HTTP only:
`TestSprintCache`, `TestSingleflight`, `TestLinearCommands`, and eval
`TestLinearSprintScenario` / `scenarios/linear-sprint.json`. No real key is
required; never call Linear from verification without an authorized live gate.
**My Progress** (`app/src/features/linear/MyProgress.tsx`, `sprint.ts`): a card at the top of the navigator, above the tabs, from `GET /api/linear`: days left and to do / in progress / done counts; click = a dialog (cycle name, dates, tabs In progress, To do, Wrong status when any, Added, Finished; a workstream button = `openWorkflow`). **Join:** title ids (`ticketIds`, any case, matched only against real issue ids, so `utf-8` joins nothing; `splitTickets` still strips and pills uppercase forms only) plus `ticket:<ID>` labels (snapshot `tickets` on workstreams and steps, `livedata.TicketLabels`), on the workstream or an OPEN track (closed, parked, superseded tracks claim nothing), every carrier listed. **Worked:** a ticket on the workstream by the row's count (Manager and Operators), on a track by that track's live Operators (`progressWorkstreams`). cmdrd's Commander lines and health badge join the same way (`livedata.WorkstreamTickets`, `daemon.ticketCarriers`): keep the two in step. **Wrong status:** an agent on a ticket that is not started, or no agent on one named In Progress (idle In Review, Approval, Blocked are right; they still count as to do). **Read only:** the app and cmdrd only read Linear; `SnapWrong.Text` (the user's badge) says "update it in Linear", Commander's copy (`SnapWrong.Agent`) ends "Update the ticket's status in Linear yourself to match the work (decision d27)." (the ticket-done line ends "Check whether the work or the ticket is out of date, and fix that one (decision d27)."): per standing decision d27 Commander fixes status mismatches itself (through its Linear MCP tools) instead of asking the user. **Dates:** YYYY-MM-DD values are calendar dates in any zone; instants read in the settings timezone; the end is exclusive, so the last day is shown (`cycleLastDay`) and days left counts calendar days to it (0 = "ends today", negative = "ended"; `cycleDaysLeft`). **States:** `no_key` Connect Linear, `bad_key` Replace key, `ok` with no cycle "No active cycle" (no button), `unreachable` "Linear unreachable, last read <age> ago" on the dimmed card, or that note alone when nothing was ever read, `off` hidden. Tests: `MyProgress.test.tsx`, `wrong_test.go`, `sprint_board_test.go`, `livedata/tickets_test.go`; shots: `node scripts/smoke.mjs --fake --fixture test/fixtures/snapshot-linear.json` (CHROME_HEADLESS_SHELL=/usr/bin/google-chrome works on Linux).
See `docs/local-config.md` and the neutral `docs/local.toml.example`. Never
commit a real workspace or credentials. Generic vendor adapters remain supported.

## Term gate

`scripts/term-inventory.sh --check` (CI: `.github/workflows/term-gate.yml`, push + PR) exits 1 listing every
non-allowlisted hit of `scripts/forbidden-terms.txt` in tracked file CONTENT and NAMES (whole-word, `cs:` =
case-sensitive, `re:` = extended regex, `sub:` = substring so `forbidden_term_x` hits for `forbidden_term`; symlink targets, `-z` names and lines of any length are scanned; submodules fail the check; covers company domains, personal handles, `/Users/<name>`), 0 when clean.
Allowlist in `scripts/term-allowlist.txt`: `path:term # reason` (reason required, else `--check` fails);
`*:text` exempts a term repo-wide AND any content line containing `text` (the Go module path).
Home paths use dynamic values or generic example paths; no literal user-home exceptions remain. Test: `scripts/term-inventory_test.sh` (temp repo: planted terms fail, clean passes).
Optional pre-commit: `scripts/term-inventory.sh --check` as a local hook.
**Fixtures are invented, never copied from the real board** (no real project names, task titles, ticket prefixes or PR numbers; tickets read `TEAM-12`). The public term list never names a real ticket prefix; the private list (`--terms`) covers it.

## Portable owner identity

Retired owner names come from `$COMMANDER_HOME/legacy-owner-aliases` (one per line) or `COMMANDER_LEGACY_OWNER_ALIASES`, read by `ownercompat.Aliases` (cached: stat at most once a second (`statEvery`), re-read only on a size/mtime change, ~0.4µs per `Normalize`; the source holds only a synthetic alias). Without that file a stored `approver:<old name>` is just another approver and `--prod-gate` refuses it. **Agents never widen it:** a process with `COMMANDER_ROLE` or `COMMANDER_MANAGER_ROLE` set ignores the env variable (only the user's file counts), and role names (`commander`, `manager[-*]`, `operator[-*]`) are never aliases. **Speed bump only:** agents are the same OS user and can write the file (or point `COMMANDER_HOME` elsewhere); the Manager/Operator guards refuse any command naming `legacy-owner-aliases` (`NamesProtectedCredential`), which a script file gets around. Keep the list to the names really used. A configured `needs-<alias>` reads as needs-user and is cleared by every state edit and answer (`ownercompat.NeedsLabels`, `IsLegacyNeeds`) and reserved in `internal-label`. Tests: `TestConfiguredAliasesCountAsUser`, `TestEnvAliasIgnoredForAgentsAndRoleNamesRefused`, `TestAliasFileCacheInvalidates`, `BenchmarkNormalize`, `TestSpawnOperator_ProdGateConfiguredLegacyApprover`, `TestSpawnOperator_ProdGateIgnoresEnvAliasInAgentRole`, `TestConfiguredNeedsAliasIsCleared`, `TestLegacyAliasFileProtection`, eval `TestLegacyApproverScenario`.

New states and labels use `needs-user`; waits, approvers and human gate verifiers
use `user`. `internal/ownercompat` normalizes legacy CLI inputs and stored labels
so running Managers remain compatible. Leaving a needs-user state clears its legacy
marker too. `[user] name` is the display name, default `the operator`; prompts refer
to the user. Personal-term exceptions live in `scripts/term-allowlist.txt`, honored
by `scripts/term-inventory.sh`; the Go module coordinate is intentionally unchanged.
Launchd units use `org.commander` (plus `-tmux` and `-ui`). Explicit installation
cleans up the old daemon unit, leaves a running legacy keeper alive until logout,
and writes the neutral keeper login entry. Daemon start retains a legacy fallback
until that upgrade; no installation or restart is needed merely to land this change.

## Reusable workflow completion checks

`cmdr workflow plug pr-review --project P --parent B --before M --pr URL`
creates a review block from `workflows/pr-review.json`. Its snapshotted verifier
runs at `internal-close-objective`, `internal-gate pass`, and
`internal-resolve-needs approve`; GitHub errors refuse completion. Checks record
`workflow-check@v1` JSON evidence in a bead comment, bound to the PR head.
A new push makes earlier evidence stale, on closed steps too. Renew them in
template order with `cmdr internal-close-objective --project P --bead S`: on a
closed step it reruns the same fresh check and records evidence for the new head,
and bd's close is a no-op, so nothing reopens and a closed block stays closed
(a refused check records nothing). Review steps first need a proof for that head,
below; `final-check` goes last. Test `TestWorkflowClosedStepsRenewThroughTheCloseDoor`,
backend `TestCloseOfAClosedBead_BDIntegration`. `ci-green`/`head-current` accept
SKIPPED and NEUTRAL on completed CheckRuns when at least one check succeeded;
`deploy-ok` stays strict. They judge only the latest run per check (workflow + job name, or
status context; newest `startedAt`, then `completedAt`, then the URL's run id), as `gh pr checks`
does, so a failed run re-run green passes (`latestChecks`, `TestCIGreenJudgesTheLatestRunPerCheck`); GitHub leaves workflow_dispatch suites (a manual deploy) out of the rollup though `commits/<sha>/check-runs` lists them, so a failed dispatch deploy never refuses; gh's dedupe key also has the event, which `gh pr view` JSON lacks, so a push and a pull_request run of one job merge here (`TestVerifyCIIgnoresDispatchRunsLikeGHPRChecks`). Diagram checks and both forms of `cmdr graph check` report
`MERGE_REVIEW_REQUIRED` with the plug command (or, when plug would refuse, the way out) or `MERGE_REVIEW_INCOMPLETE` with the
exact commands that finish it (plug first only when a step or edge is missing).
Closed targets are exempt, and so is a target whose PRs are settled (`Settled`: one merged,
none live); age alone is not. `workflow plug` refuses a PR GitHub already reports as merged.
Display facts may lag; completion always reads fresh GitHub facts, and the bead, its edges,
comments and the block's beads through `FreshDetails`. **A close's reads follow the bead, not the project** (`workflowNodes`, `cmd/cmdr/workflow_verify.go`): no unfiltered List and no display `Details`. A generated step reads its instance (`List` by `workflow-instance:` label). A target reads itself, its waits and the stage a gate guards (`ListOptions.IDs` = `bd list --id a,b`, one process), a container the targets that wait on it (`Client.Dependents` = `dep list --direction up`) and theirs, then every instance among those; all edges and comments through `FreshDetails`. Never call `List` with empty `IDs` here: that is the whole project (`readWorkflowIDs` guards it). **A short answer refuses:** an id left out of the list with exit 0 is a short read until the bead's own `FreshShow` says `beads.ErrNotFound` (the exact sentence of each backend about that id: bd `Issue X not found`, br `Issue not found: X`; any other failure refuses too), and only then is it no node, as a dangling wait or stage is in a whole-project read. Spelling: `--id a,b` when the binary is `bd` or the project sits under a `.beads/dolt` (`doltProject`: the stand-in's own routing rule, also any wrapper name), repeated `--id` for real br (it reads a comma list as one id; bd keeps only the last repeated one). A label-filtered instance read has no such check: the rule reads a missing step as stale. Invented bounded-read exercise: a close on an eight-bead review block in a 400-bead project should read its relevant beads, not serially show all 400. Tests: `TestWorkflowCloseReadsDoNotGrowWithTheProject`, `TestCloseVerbOfAMergedPRTargetReadsOnlyItsOwnBeads`, `TestWorkflowCloseJudgesLikeAWholeProjectRead`, `TestWorkflowCloseRefusesWhenANarrowReadFails`, `TestWorkflowCloseRefusesWhenAnyReadFails` and `TestWorkflowCloseRefusesEveryShortIDRead` (every read of every kind in `closeKinds`, one at a time: add a new target kind there), `TestShortIDReadNeverPassesAGuardedClose`, `TestNarrowReads_BDIntegration` and `TestGuardedCloseAgainstRealBD_BDIntegration` (`COMMANDER_BD_INTEGRATION=1`, also through `scripts/br-bd`). **Rule and read must agree:** `TestWorkflowNodesFollowsEveryMergeRuleRead` pins, by parsing `internal/workflowtemplate`, every place the rule looks past the judged bead (`mergeRuleReads`); a new one fails it until `workflowNodes` reads it too. The parity test hangs a live PR on each relationship the read skips (`seedRelationshipBait`). Not narrowed: `--then-owner`/`--then-decision` on a close still do one whole-project or `kind:objective` List through their hold write (`workflow_gate.go`, `internal_objective_edit.go`).

**Settling a block whose PR merged outside the review:** `cmdr workflow settle-block --project P --block B --reason R` (`cmd/cmdr/workflow_settle.go`; owning Manager, Commander or the user; Operators refused) reads the block's PR fresh and refuses before any write unless GitHub reports it MERGED (any read error refuses) and every open target the block was plugged before is `Settled` (a second live PR still needs its review). It then closes each open step in template order with one `workflow-check@v1` comment, verifier `settled` (`workflowtemplate.SettledVerifier`), finding `settled: PR merged at <sha>, review not completed. Reason: R`: no writer binding, proof or coverage is written; steps already closed keep their evidence. Last it closes the block through `internal-close-objective` (merge rule, gates, custody); a refusal there leaves the steps settled and names the command. A rerun records nothing twice. A refused step close whose block PR merged names the verb (`settleHint`, one gh read). Board: a settled check reads "Settled: PR merged, not reviewed" (`ReviewCheck.settled`, not in `done`), the closed block "Settled: PR merged, review not completed", progress "N of M checks done, K settled: PR merged, not reviewed" (`ReviewBlock.settled`). Tests `TestSettleBlock*`, `TestReviewBlock_SettledBlockReadsSettledNotReviewed`, eval `TestSettleBlockScenario` (`scenarios/settle-block.json`, private to cmdr so it runs in `cmd/cmdr`).

**Who the rule applies to** (`internal/workflowtemplate/rule.go`). Door and graph check: a merge
target (`MergeTarget`: `gate-name:merged`, label `gate` with merge/merged as a word of its own
in the title, or a step with a `pr:` label whose title means merging the PR (`isMergeStepTitle` fails closed: any merge/merged/merges/merging word, "Squash-merge" included, unless every one sits in
an exclusion: merge conflict(s), merge main/master into X, merge queue, pre-/post-merge, merge commit(s), merge request(s); a wrongly
matched step's MERGE_REVIEW_REQUIRED names the plug command and an `internal-update --title` retitle; `TestMergeStepTitles`)) or a
bead a pr-review block was plugged in front of (`PluggedBefore`: the block's instance key names it;
`BlockPluggedBefore`, one bead's edges, no project list). A bead merely downstream of another
target's block closes normally. A plain `pr:` step is neither: the
label only says which PR it tracks, so it closes with no GitHub call. Out of play, no finding:
closed, superseded, retired, generated workflow beads, and parked beads (a door still judges a
parked merge gate someone closes). Standalone gate beads are not steps; `attachMergeGates` puts
open ones on their stage (`gate_checks`). A closed block's steps fold off the board and ride on
the block's node (`workflow_check.steps`, `attachFoldedSteps`), so a complete block reads complete.
A block folded because an ancestor closed (`foldedUnderClosed`) has no row at all: `attachFoldedBlocks` carries it and its steps on each open bead or merge gate waiting on it (`workflow_check.blocks`, `Node.Blocks`, read only by `workflowInput`, one copy however many beads wait on it). Nothing draws it, and the door and board now agree on that shape. A label-free target of such a block gets its PR read through `PRHeadCache` like any candidate. Tests `TestWorkflowReview_FoldedBlock*`, `TestWorkflowCloseFoldedBlockAgreesWithTheBoard`, eval `TestFoldedBlockScenario` (`scenarios/folded-block.json`, regenerate with `GEN_FOLDED_BLOCK_FIXTURE=1 go test ./internal/livedata -run GenerateFoldedBlockFixture`).
**A ship with no PR** (a deploy on a VM): `cmdr internal-gate pass|rm|apply --project P --bead STAGE ... --no-pr '<why, 15+ chars>' --evidence '<the user's approval: answer id or decision>'` (`cmd/cmdr/gate_no_pr.go`) waives only the rule's "has no PR yet" finding (`Finding.NoPR`: no `pr:` label on the target or its stage, no review block plugged before or waited on); any other finding still refuses, naming why. The caller is judged before any read or write: Operators (`callerIsOperator`) never, a Manager only when it owns the stage (`ownerRoles`), Commander and the user always. Before the gate changes, one comment `merge rule waived for a ship with no PR by <who>: <why>. Approval: <evidence>` goes on the gate. It reads no GitHub. A speed bump, like the other guards: the approval text is recorded, not verified. Tests `TestGate*NoPR*`, `TestMergeRuleNoPROnlyWithoutAnyPR`, eval `TestGateNoPRScenario` (`scenarios/gate-no-pr.json`, real cmdr + real bd, `COMMANDER_BD_INTEGRATION=1`, ~2 min). Takes effect after an install.
**Fix lines plug accepts:** `MERGE_REVIEW_REQUIRED` names plug only when plug would take it: a target waiting on another pr-review instance (`otherBlock`, plug's own "refusing a second block") gets `--review none` on the bead holding the label, or says to look at the wait when no label names that PR; a plug that finishes a target's own block names the block's parent, so the key matches. Test `TestMergeRuleNeverOffersARefusedPlug`.
**Clearing a label:** `internal-set-objective --review none` removes `in-review` and every `pr:` label in any spelling (fresh read; a failed read clears nothing), refused while a review wait remains unless the command also passes `--waiting`. It stays the only agent verb for `pr:` (`internal-label` keeps it reserved); Operators are refused, as for hold and park. Every removal is recorded: one comment `review labels cleared by <role>: <labels>. Reason: <why>`, written after the command's other checks and before the labels go (a refused command records nothing, a failed comment clears nothing). On a merge target or a merge gate's stage (`MergeTarget`, a `gate-of:` gate that is a `MergeGate`) with a `pr:` label it needs `--reason` of 15+ characters, else it refuses naming the labels. It reads no GitHub, so a real repair of a mistaken label for a live PR stays possible and a failed read opens nothing; a failed read of the stage's gates clears nothing. `--review none --waiting review:x` is refused (no PR left), at the door too (a `pr:` label the same write removes does not count). A plugged block keeps its own PR reading, so a target stays judged at the doors; no refusal or warning for that case. **Limit:** the swap still closes a gate: clear a live PR's label with a reason, label any merged PR, close. The merge rule reads what is left. Nothing detects this; it is on the record, and these guards are speed bumps. Tests `TestSetObjective_ReviewNone*`, `TestWorkflowCloseLabelSwapIsRecorded`, `TestWorkflowCloseMistakenReviewLabelOnALaterStep`, `TestReviewNone_BDIntegration`, `TestDoorChecks`, eval `TestStateWriteScenario/review-none*`.
**One PR reading:** `TargetPRs` is what a target is judged on: its plugged block's PR (`BlockPR`,
from the block's description, the one the instance key binds), then EVERY `pr:` label of the target and
of the stage a gate guards (`LabelPRs`: `pr:N` through the project repo, `pr:owner/name#N`, a URL;
all canonical, lower case). A URL in a target's description or a gate's check names nothing.
Door and display share it (`workflowRepo` = `livedata.GitHubRepo`, snapshot `workstreams[].repo`).
Node `pr_states` holds GitHub's state per PR (missing = open). Every live PR needs its block, so a
merged label cannot excuse an open PR and a live PR with no block of its own is `MERGE_REVIEW_REQUIRED`
(`plug` allows one block per target, so two live PRs refuse until one merges or closes). A CLOSED
(abandoned) PR needs no block and does not refuse the live PR's complete block; alone it is no merge. Known route: a target
with no `pr:` label and a block plugged before its PR merged reads as merged, whatever the target ships.
**No gh per snapshot:** `livedata.PRHeadCache` answers from memory, reads stale PRs one at a time
in the background (`PRHeadTTL` 5 min, a merged PR never again) and keeps the last good value on a
failed read. An invented repeated-poll exercise requires 300 snapshots to share one gh read (`TestWorkflowReview_SnapshotsDoNotSpawnGHPerPoll`).
Until the first read after a cmdrd start a merged target reads as not merged for a few seconds. Tests:
`livedata/workflow_review_test.go` (rows through `projectObjectiveRows`/`BuildSnapshot`), `rule_test.go`, `workflow_verify_test.go`.

Before checking any step, the owning Manager explicitly selects the original writer
and every actual fix writer relevant to the reviewed diff from known commit reports:
`cmdr workflow bind-writers --project P --block R --writers original-task,fix-task --head <current-full-sha>`.
Commits from the user's own sessions (no task) bind as an external writer:
`--external-writer user --commits <base-sha>..<last-sha>` (or a comma list of full SHAs), `--writers` optional for fix
tasks. The range resolves through GitHub's compare API (ahead, full count, fail closed) and every commit must be on the
PR; the record gains `external:{author:"user",commits:[sorted full SHAs]}`. A commit an Operator run of the project
wrote is refused (`workflowOperatorCommits`, `cmd/cmdr/workflow_operator_commits.go`, local repo, read only): by the
identity in the commit object, never a reflog or message. The host launcher sets each Operator's committer
(`spawn.OperatorGitEnv`: `GIT_COMMITTER_NAME=<agent>`, `GIT_COMMITTER_EMAIL=<agent>@operator.commander.invalid`; the
author stays the user's), git writes it on every object a process makes (commit, amend, cherry-pick, revert, am, merge,
rebase) and on none for a ref move (reset, fast-forward, checkout), so a reviewer that reset onto the PR head is no writer.
An author or committer under that domain is an Operator's (the bind counts any; reviewer independence only the review
task's runs' identities). A copy made outside an Operator (user rebase/amend/cherry-pick, GitHub update-branch) is matched
by change: its `git patch-id --stable` first appeared (committer time, ties = the Operator's) in an Operator commit of the local repo
(`--all --reflog`, every worktree; `operatorCopies`; so the user's own commit stays the user's after an Operator copies it and the
user copies that again). All reads
ignore replace refs. A git error refuses, a commit not in the local repo passes. Limits: an Operator started before the
install, or one that unsets the variables, commits as the user and is not attributed; `--committer-date-is-author-date` can date a
user copy before an amended Operator commit; the same change made independently counts as the Operator's when its commit is first; a copy with a different change (squash,
conflict, moved context, edited amend) or whose original left the repo (branch, worktree and reflogs gone) passes. Reviewer independence is judged against
the bound tasks AND the bound commits (a review task whose runs wrote one is refused). The user's CLI, Commander (external
commits only; the Manager's stored task writers for the PR are kept) or the owning Manager may bind one;
Operators never. Rebind hints keep the stored writer kinds (`workflowtemplate.RebindCommand`). Tests
`TestWorkflowBindExternalUserWriter`, `TestWorkflowExternalWriterReviewIndependence`, `TestExternalWriterBindingParse`,
`TestOperatorAuthorshipReadsCommitIdentities`, `TestOperatorAuthorshipReadsTheAuthorToo`, `TestOperatorAuthorshipFollowsRewritesOutsideAnOperator`, `TestOperatorAuthorshipIgnoresReplaceRefs` (real git), `TestHostOperatorCommandSetsTheRunsGitCommitter`, `TestRebindCommandKeepsTheWriterKinds`, `TestRangeCommitsFailsClosed`, eval `TestExternalWriterBindingScenario` (`scenarios/external-writer-binding.json`, runs
in cmd/cmdr). No descendants or reviewers are inferred as writers. Tasks must have fresh ancestry
in the block's workstream and task-bound runs in the same project. The current PR
head is required. Selection is persisted as `workflow-writers@v1` on the block;
`internal-comment` reserves this prefix for the binding command. A selection/head
change gives it a new revision and invalidates all earlier check evidence. Identical
binding and idempotent `workflow plug` preserve it. Legacy unbound blocks fail with
this migration command; renew all checks in template order after binding. See
`docs/workflow-writer-binding.md` for the standalone CLI recipe.

For a review step, add a comment with `cmdr internal-comment` whose text is
`workflow-review-proof@v1 {"review_bead":"<separate-review-task>","head":"<full-sha>","fix_commits":["<full-fix-sha>"]}`.
The referenced bead must be closed, backed by task-bound Operator runs independent of every explicitly bound writer, and its report
(its latest comment with a line that is exactly `CLEAN` or `NOT CLEAN`; later notes do not
count) must name the full current head (`Head: <full-sha>`, `prompts/operator-code.md`) and hold
no `NOT CLEAN`; an earlier `NOT CLEAN` on that same head stands. A typed
`workflow-review-result@v1` receipt on an exact line inside the original report
also supplies the verdict without a standalone CLEAN. This compatibility path
requires the actual task/run/actor, matching sealed head/base/scope/manifest,
original prompt digest and successful terminal evidence, with execution complete,
source clear, coverage complete and an explicit empty findings array. Duplicate
receipts/fields, malformed or contradictory history and missing provenance refuse;
CE JSON and CLEAN in prose supply no verdict. The receipt may also be split: a line exactly `workflow-review-result@v1` then, on the very next line, one complete JSON object (no blank line, wrapped object, prose or leading space; counts toward duplicates; judged as embedded, same provenance checks). Tests `TestSplitReceipt*`, scenario cases `split-*`. Test `TestEmbeddedTypedVerdict` and
eval `TestEmbeddedReviewVerdictScenario` (`scenarios/embedded-review-verdict.json`). A malformed proof comment is
ignored with a warning (the newest well-formed one counts). The commands `MERGE_REVIEW_INCOMPLETE`
prints hold placeholders, with `no_findings` named only as the alternative. Fix commits
must belong to the PR; use `"no_findings":true` and an empty fix list when the
review found nothing. The adversarial reviewer must differ from the first.
`fix_commits` are the review loop's fixes for that step, including earlier rounds; `no_findings` only when the review found nothing.
**Bounded review loop** (decision d78, `workflowtemplate.MinorPolicy` = `one-minor-fix-pass@v1`): blockers/majors are fixed and rechecked as before; minors get at most one fix pass; minors left on the passing head never block. A **delta recheck** proof adds `"prior_review":"<bead>","prior_head":"<full-sha>"` (with nonempty `fix_commits`): the recheck's typed receipt must have `base` = `prior_head` (it reviewed only the fix delta), the prior review must be a closed separate Operator review with a complete typed result on `prior_head` whose own base is the PR base or an ancestor of it (compare `base...PR base` ahead/identical; a PR commit, any other ref or a failed read refuses: a FULL review, coverage never chains through another recheck; a later recheck names the full review and covers every fix since; `fullCoverage`), every blocker/major of ANY valid typed result of the prior review on `prior_head` (an interrupted run then a clear resume included) must be listed in the recheck receipt's optional `resolved` (ids the recheck verified fixed; an unknown id refuses), `prior_head` must be on the PR, and it must be a different review bead (`deltaRecheck`). A plain proof is held to the same full-coverage base rule (a delta-only review, or one from an off-PR base, is refused). Adversarial independence compares against every review the primary evidence names (`review_bead` and `prior_review`), and the same reader judges the adversarial proof's own `prior_review`. Every typed minor on the passing head (`currentResult`) is filed by the close door after the head re-read (`Review.FileFollowUps` -> `fileReviewFollowUps`, `cmd/cmdr/workflow_followups.go`): one plain step per finding under the owning workstream (`beads.Owner`, never under the block's stage, whose open child would hold the merge), labels `kind:objective`, `workflow-followup:<16 hex of instance/step/review/head/finding>` (idempotent: a retried close finds it) and `workflow-followup-of:<block>`, a `discovered-from` link to the block (a failed link only warns), and a Manager-to-start receipt (`holdForManager`). The evidence records `follow_ups` and names the policy; no filer, a failed filing or a count mismatch refuses the close and records nothing. `no_findings` with typed findings is refused; `no_fix_required` minors are filed too. Not enforced: that only one minor fix pass happened (the verifier never asks for a second; the prompts teach it). **Delta renewal** (`renewal.go`): a proof with `"delta_renewal":true`, `review_bead` = the review of `prior_head`, nonempty `fix_commits` and no addendum carries that complete TYPED review (legacy untyped CLEAN refused; its verdict judged on `prior_head`; a full review, or a delta recheck whose proof adds `prior_review` = the full review it retained, judged as that recheck) to the current head with no new review, only when the verifier reads `gh api repos/R/compare/<prior>...<head>` itself: status `ahead`, 1 to `RenewalMaxFiles` (3) files, every name (and a rename's old name) on the prose allow-list (`proseFile`: `.md .markdown .rst .adoc` under `docs/`, or a top-level README/CHANGELOG/CHANGES/HISTORY/CONTRIBUTING/AUTHORS document; never `.txt` (build inputs wear it), never in a dot or `prompts|agents|skills|commands|hooks|rules` directory, never a name whose stem contains claude/agent/gemini/skill/conventions/copilot/cursor/windsurf/codex/requirements/constraints/cmakelists/makefile/runtime/dockerfile/pipfile/gemfile/pyproject; so `CLAUDE.local.md`, `AGENTS.override.md`, `docs/CMakeLists.txt`, `docs/dev-requirements.txt`, `docs/runtime.txt`, `.claude/agents/*.md`, nested READMEs are not prose), every patch readable (no patch = binary, refused except a pure rename), no removal, at most `RenewalMaxLines` (20) lines and `reviewpolicy.Select` reading it focused (its sensitive-word list also refuses). The measured delta is the cumulative one from the reviewed head, so renewals cannot be chained; a failed read refuses; the evidence says `delta renewal of <prior>: N documentation lines in <files>`. **Report check:** `cmdr workflow review-report-check --head SHA --file report [--receipt file]` (read-only, `workflowtemplate.CheckReport`) refuses before the reviewer posts what the door would refuse: no `Head: <sha>` line, no receipt (a verdict only in mail), a receipt for another head, no sealed launch record (`reviewrun.ReadRecorded`) or one that does not match, an embedded receipt that is not clear (or has `findings:null`), and a verdict line disagreeing with the receipt (CLEAN with a blocker/major, NOT CLEAN with only minors, any verdict on an incomplete run). Tests `TestMinorPolicy*`, `TestDeltaRenewal*`, `TestMinorFindingDoesNotRequireSourceChangeOrNewReview`, evals `TestReviewMinorPolicyScenario` (`scenarios/review-minor-policy.json`, real close door, in-memory beads, scripted gh incl. compare) and `TestReviewReportCheckScenario` (`scenarios/review-report-check.json`, real verb and registry). On a PR of any length (`onPR`: the listed oldest 100, else the compare API: an ancestor of the head and not of the base; a failed read refuses).
Existing template verifier names remain supported; new templates may also name
`threads-resolved`, `ci-green`, `merged`, `deploy-ok`, or `review-proof`.
`deploy-ok` checks the latest `deploy` workflow run on the PR head.

**Doors on the guard itself:** `internal-gate rm|apply` run `checkWorkflowClose` before retiring a
gate; `internal-dep rm` refuses the edge from a target to the block plugged before it (`IsPluggedBlock`);
`internal-comment` refuses text starting `workflow-check@v1 `, which only a door writes (a proof stays
allowed); review independence resolves every `agent_runs` row for the exact task and project (`workflowTaskRoles`), including completed runs. Missing, unreadable or ambiguous provenance refuses; mutable owners and the caller do not determine independence.
Tests: `TestGateRmAndApply*`, `TestInternalDepRm*`, `TestInternalCommentRefusesEvidence`,
`TestWorkflowReviewOwnerMustHaveARun`, `TestRedefinedCheckNeverClosesAMergeGate`.

These guards are speed bumps: scripts or direct database access can bypass them.
They do not stop an outside `gh merge` or a merge on GitHub. Evidence records
checks and review reports; it does not prove the quality of review reasoning.


Browser MCP aliases must be bare ASCII letters/digits/underscores/hyphens:
Codex 0.164 override paths do not decode quoted segments. Unsupported discovered
browser aliases refuse before launch with a rename remedy, even when selected
or disabled. Synthetic coverage: `TestCodexMCPUnsafeBrowserAlias` and the
unsafe-alias recording eval.

Display cache freshness: `internal/beads/cache.go` fingerprints the Dolt journals
on a Dolt project (only them, see "Loads under churn"), else the SQLite/export files. A close made by another process must invalidate the
cached List before an export flush; `TestClosedGateDoltTransition` covers the
snapshot/diagram and the next stage's retained main-CI wait.
**Export vs journal:** cmdrd's board reads `ExportReads` first, and bd in server mode never writes
the export (only our flush). The Dolt journal therefore gets no lag allowance (`exportBehind`): after this client's
flush, any journal change since the stat taken before it means behind; without a flush, a journal newer than the
export does (beads.db keeps `exportLagMax` 2s). In an invented handoff exercise, a close right after its `--then-*` hold write (whose settle
flushes inline on a br/SQLite project; on Dolt the flush runs in the background and the load reads bd) arrives within the lag allowance; it must immediately read closed with the successor still held. Tests `TestExportJournal*`,
`TestCloseRightAfterItsHoldIsNotReadOpenFromTheExport`, eval `TestCloseAfterHoldScenario`; late exits after such a
close: `TestDelayedExitAfterCloseKeepsTheNextHolder`.
**Loads under churn (il9.202.25.1):** a busy Dolt project (a write every few seconds, `bd export` 5s for 5,600 beads)
made loads take 20-60s and closes reach the board 30-55s late. Four causes, four fixes: (1) on a warm client a behind Dolt export is
no longer waited for: a background flush (`startFlush`, at most one per `flushEvery`, 2 min budget) refreshes it for
later loads and this load reads bd now; a br/SQLite project still waits for its flush. **Cold client (il9.202.29):** a
client that has never flushed the project (every cmdrd start; the detail cache is cold too, and with no fresh export
the load sent every bead to bd show: 6 min for 5,600 beads) waits for the export (`coldFlush`, 90s budget). Every
flush is ONE per project in flight (`inflight`), run by the client, not the caller: loads beside it (Poll, health
loop, API) wait for that one, a waiter whose context ends leaves without ending it. A failed flush is never waited
for again: retries run in the background with backoff (3s doubling to 2 min, logged), and meanwhile a cold load
answers Details from the export on disk as a `PartialDetailsError` naming the failure (`staleExportError`; beads it
lacks stay unread, never bd show; the error names no bd show), so the project reads degraded rather than storming bd; List goes to bd. The backoff applies to every failed flush (warm Dolt and br/SQLite too) and a retry
starts only when a load arrives. A client that cannot flush (or a stopped one) reads bd as before. **Cache fill
(il9.202.29.5):** a load answered from the export fills an EMPTY detail cache from that file (`seedCache` ->
`CachedClient.seedFromExport`): one bd list keys each bead, kept only while the Dolt journals and the file stand where
they were at the flush (else nothing is seeded); entries are export-sourced, so the re-take/show rules above hold.
Each later export a load answers from re-stamps the filled cache's export-sourced entries whose key is unchanged (never one from bd show or owing a show; same standing check, one cached list, once per file; il9.202.29.7), or the SECOND write after a flush and 30s+ of quiet showed every bead (121 of 121). The seed never waits for the cache's reread slot: a busy cache is offered the file again by a later load. Without it, the first write after 30s+ of quiet sent every bead to bd show (bench 5,601 shows / 36.7s -> 1 / 0.2s;
`TestColdDoltLoadFillsTheDetailCacheSoTheFirstWriteShowsOneBead`, now through three quiet writes: 1, 2, 3 shows in all; `TestExportLoadDoesNotWaitOnABusyCacheToSeedIt`). The test fake models `exportDetails`. cmdrd's
board and invariant pass have separate ExportReads clients, so each makes its own first export at a start. Flushes
are owned by the client: `beads.StopBackground` cancels them and waits, called by `Daemon.Run` on exit. Tests
`TestColdDolt*`; measure with `COMMANDER_COLD_BENCH=1 go test ./internal/beads -run TestColdLoadBench -v` (5,600
beads, 1/10 scale: four start loads 36.7s and 5,601 bd shows before, 0.55s and none after, export working or failing). (2) On a Dolt project
`fingerprint` is the journals alone: `issues.jsonl` (rewritten by every flush, ours too) and the leftover SQLite files
no longer drop the cache or fail `ReadConsistent`, so a read is retried only for a real write. (3) `CachedClient.List`
answers every label filter of all statuses (`labelsOnly`) from one cached `List{Status:"all"}`, filtered in memory
(bd filters after it sorts: same beads, same order, checked against a live 5,600-bead db): one bd list a load, not four.
(4) `cached` misses at one fingerprint share one call (`flight`); a waiter whose leader was cancelled (its own context ended) or panicked reads on its own (`flight.finished`/`cancelled`, never the error text; `TestCachedLeaderPanicDoesNotCrashFollowers`). The churn eval scales its limits by `COMMANDER_EVAL_TIMEOUT_SCALE`.
Measured with the synthetic project in `internal/livedata/churn_test.go` (840 beads, live costs: unit 100ms): a close
visible p50 41.5s / max 55.6s -> 1.6s / 1.9s, slowest load 45.8s -> 2.1s, kept-last-good 2 -> 0
(`COMMANDER_CHURN_BENCH=1 go test ./internal/livedata -run TestLoadUnderChurnScenario -v`, about a minute). Tests:
`TestFingerprintOnDoltFollowsOnlyTheJournal`, `TestCachedLabelListsShareOneListCall`, `TestCachedMissesShareOneCall`,
`TestDoltLoadDoesNotWaitForTheExport`, eval `TestLoadUnderChurnScenario` (`scenarios/load-under-churn.json`, unit 20ms,
private to livedata so it runs there). Not changed: the `gates` phase (attachGates, up to 14s in the same logs) and
board derivation.

Closed-gate test fidelity: `tests/evals/scenarios/closed-gate.json` describes
input beads and a journal transition, not a prebuilt closed snapshot.
`TestBoardClosedGateScenario` warms Source's cache, externally closes a gate,
proves the stale read, then changes the journal and composes the board.
`TestClosedNeedsUserRowsHaveNoAsk` keeps the root open and feeds a closed child
with stale needs-user state directly into askLists; both pitched and missing-pitch
cases also assert the open gate remains actionable.

**Workstream registration is an action, not a display decision:** `wsmgr` checks
fresh ancestry and open objective kind (no `kind:task`/`kind:gate`/`gate`/`kind:track`)
before preparing/adopting a Manager. Display orphan promotion cannot authorize
registration. `PrepareAll` (cmdr start, soft restart) skips and collects a refused
or unreadable target and still returns every valid window, so one bad row never
costs the legitimate Managers theirs (`TestPrepareAllSkipsInvalidTargetsAndKeepsValidWindows`,
`TestSoftRestart_SkippedWorkstreamKeepsTheOthers`; EnsureNative/Adopt refusals
before side effects: `Test{EnsureNative,Adopt}ValidatesBeforeAnySideEffect`). Precise non-root repair:
`internal-unregister-workstream --project P --bead B` (diagnostic), then add
`--confirm-root R` to apply as owning Manager/Commander/user; actual roots and
Operators refuse. Only the exact Manager row is removed, with transactional audit
(schema 21); no process or underlying work is removed. See
`docs/testing/workstream-registration.md` and eval `TestManagerRegistrationScenario`.

**Watcher mail follows real ownership:** every cmdrd watcher's mail (settle, watch, waits, live gates, invariant, pitch, health) goes through `wsmgr.Manager.RoutedMail`: `OwnerOfRole` reads fresh parents (`StrictRoot` + `validateTarget`: 4 `br show` per mail for a root, plus one per ancestor and one per project asked before the bead's; a role naming no bead asks every project; uncached; no write, registration or start; `TestOwnerOfRoleReadCount`). The reads run on half of the time the send's context has left (`routeReadContext`), so slow reads cost the route, not the mail. Nested role: the root's Manager, with a note. No open workstream: Commander gets it as `[no Manager] <subject>` naming who it was for, so nothing is lost and no owner is guessed. A role naming no bead (project-level Manager) or a failed read: sent as addressed, the Waker's retained-mail alert still covers it. Invented routing regressions: an operational parent absent from objective rows must not make a bead its own Manager; a deferred child of a closed workstream must not fire a wait; a closed workstream must not raise a late plan alert. `checkPlans` now clears `plan:` issues for workstreams off the board (not on a degraded snapshot). The Waker re-reads the cursor when a delivery fails: mail a cursor shows read while a slow attempt ran raises nothing and is not retried; anything else keeps the failure, including no cursor at all, whatever the mail's age (`provenRead`). **No cursor = unread at any age:** the Waker counts mail as read only when a readable cursor proves it. Mail already older than `replayWindow` (2 min) at first sight of a role with no cursor (a cmdrd restart is a first sight: `seen` is in memory) is held: no wake, no read mark, reported through `Waker.OnHistory` as ONE health issue `mail-history` (`reportMailHistory`: at most `healthHistoryRoles` 10 roles named, one Commander mail per issue, re-raised once per cmdrd start while still held). Bound on a new role: the inbox read is 50 mails and history goes in as one `mailstate.HistoryNote` line, only on a delivery or hook injection the role's fresh mail already caused, whose cursor then passes it. A held role clears when a cursor exists: `cmdr internal-inbox --agent R --mark-read` (without `--unread`, which hides old no-cursor mail). Gap: an agent's own `internal-inbox --unread --mark-read` passes held mail with no note. **A decision answer is never history:** `mailstate.IsAnswer` (subject prefixes `[decision] `, `[workflow `, `the user approved:`, `the user deferred:`; `TestAnswerSubjectsPassIsAnswer` holds `needsResolutionMail` to it) keeps it out of `history` and out of the no-cursor age filter, so the hook and the Waker agree. It goes in the normal batch at any age and wakes the role, starting a stopped Manager; a role with no open workstream follows the route rule (`BlockedError`, no start, one `mail:<role>` alert). A very old answer is delivered once too: its stable answer id and the step's state make it safe to ignore. Its delivery marks held mail read as well, so the cursor passes history above the answer's id. The storm bound: only a role with no cursor can hit this, and a role's first delivery writes one (invented restart exercise: three cursorless roles have empty inboxes and cause no first alert; a fourth has an old answer and must receive it once). Subjects that only mention an answer (`MentionsAnswer`: a reply, `[no Manager] ...`) stay held and the alert names them with role, plus a one-line loop that clears the listed roles. An Operator back after a gap is no first sight (`forget` keeps `seen` and `history`): mail sent in the gap is news. While a retry backs off (`retries[role].next`) the inbox is not read, whatever signals arrive; the kept error is still reported each poll. A health alert goes from `healthBoard.alertDue` on its own 10s ticker, never from `list` (Source.Snapshot calls that); a failed send goes again after 1, 2, 4, 8, then 15 min per key (`healthAlertRetry`/`healthAlertRetryMax`), instead of waiting for the issue to clear and recur. A timed-out send may still have landed and am has no idempotency key, so duplicates are possible at that rate. `internal-mail --to` now validates Manager recipients through fresh `OwnerOfRole`, annotating redirected mail and skipping obsolete nested copies. Not routed: other agent/CLI senders (resolve-needs, proposals): mail to a dead role keeps alerting until it is read (`cmdr internal-inbox --agent R --mark-read`). Tests: `RoutedMail*`, `TestPoll_MailReadDuring*`, `TestPoll_PartlyRead*`, `TestCheckPlansDrops*`, `TestPoll_MailAged*`, `TestPoll_OldMailAtFirstSight*`, `TestPoll_FailedMailAcrossARestart*`, `TestPoll_HistoryRides*`, `TestPoll_AnswerSeenFirst*`, `TestPoll_HeldMailMentioning*`, `TestPoll_OperatorBackAfterAGap*`, `TestPoll_NoInboxRead*`, `TestHookMailShowsAnOldAnswer*`, `TestHookMailTells*`, `TestDaemon_RunSendsDueHealthAlerts`, `TestNew_WiresTheWakerToTheHealthBoard`, `TestHealthAlertRetries*`, `TestHealthListNeverSends`, `TestMailHistoryIsOneBoundedIssue`, eval `TestStaleManagerRouteScenario` (`scenarios/stale-manager-route.json`; its `plan_alerts` run in `internal/daemon:TestPlanAlertScenario`, since `checkPlans` and the health board are private to cmdrd).

Closed approval context compatibility: `internal-restore-approved-context --project P
--bead B --generation N --answer-id ID [--dry-run]` restores only exact retained
card Context to an empty Description on a closed user gate. Its schema-20 metadata
reader is read-only and never migrates state; the write uses the normal Beads
door. See `docs/cli/restore-approved-context.md` for provenance checks and the
backend's check/write concurrency limitation. Exact reads support native bd
1.3.1 (c1c4b642a) omitted descriptions via a version-gated `--readonly sql`
SELECT; an explicit matching string is required, never omission-as-empty.
Unknown versions/partial records refuse; see `TestExactDescriptionNativeOmission`
and eval `TestRestoreNativeDescriptionScenario`.

## Operators on the user's Mac (places, read-only first slice)

Design `docs/plans/2026-11-09-mac-operator.md`. A **place** is a paired device (`mac:<name>`, migration 0023: `places`,
`place_runs`, `place_comments`; `internal/place`). The Mac pulls; cmdrd never runs anything there: it sends ids, a stop
and a mail signal, the Mac's app decides what runs. **Pairing** is the user's: the app's App menu "Pair this Mac as a
place…" creates an Ed25519 key (sealed with Electron safeStorage in `<userData>/place-device.key`), `POST /api/places/pair`
(api token) returns a one-time code (10 min, `$COMMANDER_HOME/place-pairing/`), and `cmdr place pair <code> --name <slug>`
in the user's own terminal records it; `cmdr place revoke|list`. Both verbs refuse any `COMMANDER_ROLE`/
`COMMANDER_MANAGER_ROLE` (speed bump) and are not in `/api/cmd`. **Opt in:** `internal-set-objective --run-on mac:<name>|none`
(Operators refused; unknown/revoked place refused); `run-on:` is a reserved label and the write door refuses an agent's
`run-on:` naming a place that is not paired. **Spawn:** `internal-spawn-operator` on a `run-on:` step (`--place` must match)
makes the task bead as usual, then `place.RecordOffer`: an `agent_runs` row (running, no pane, no container) plus
`place_runs` offered; no worktree, tmux or launcher. Refused before the task: Codex, `--account`, `--writes-workflow`, a
mismatched `--place`, no closed user gate over the step (`placeGateCovers`, no warn-only); at the offer: unpaired/revoked,
a second active run on the place. The workstream cap counts it. **Channel** (`internal/api/places.go`):
`GET /api/places/connect` (api token, `?token=` from the app's main): challenge, hello = device public key + signature over
nonce and key + `cmdr place revision` (any other revision refused: "update cmdr on this Mac"), welcome names the place;
then offer/stop/mail out, fetch/accept/decline in. Accept re-checks the brief's sha256 against the offer (an edited brief
ends the run declined) and mints the **run token** (hash only in the store, dies with the run, 12h max). Run endpoints
take only `X-Commander-Run-Token`, never the api token: `POST /api/places/runs/<id>/comment` (write door as the run's
Operator role, idempotent by client id; `report:true` marks it reported), `mail` (to the run's Manager only), `GET mail`
(the role's unread, read from am only when signalled, then marked read), `heartbeat` (refusals become one bead comment,
usage events go to `chat/place-usage.jsonl`, `usage_limit` ends it stopped/usage_limit and sends stop), `exit`.
Accept also re-reads the step's `run-on:` label and, for a prod-gate run, the gate (closed `kind:gate`, approver user,
scope sha == the `scope_sha` the Mac's dialog showed), else the run ends declined (`stillAllowed`). Run comments go to bd
as `comments add <bead> -- <text>` (a leading dash stays text).
`POST /api/places/offer|stop` (api token) re-send a recorded offer / stop a run; `cmdr place revoke` stops through it
(store end as fallback), a run swept as died is sent a stop, and the Mac stops its process group on a heartbeat 401
(a dead token: revoked, died, expired); mail to a place Operator defers (`wake.ErrPlaceOperator`), never a local window; `internal-mail --interrupt` to a place
run stops it (Operators refused). **Lifecycle** (`place.Service.Sweep`, cmdrd's minute loop): live within 2 min of a beat,
unreachable 2-15 min (Manager mailed once, not Broken), died after 15 min (an offer nobody answered: declined); ended runs
settle through `runSettler.settlePlace`: exited with a report = finished (Manager to close), anything else back to the
Manager with `operator-died`, **never restarted**; no worktree to freeze. The quiet watch skips place runs. Board: snapshot
`live.places[]`, step `place_text` = the row's why and the workflow step's text ("Operator working on mac:laptop",
"Waiting for the user to start the run on mac:laptop", "Operator on mac:laptop unreachable 4 min"). **On the Mac**
(`app/electron/place-runner.ts`, `place-main.ts`; macOS only, only once paired, `COMMANDER_PLACE_RUNNER=off` disables;
needs `cmdr app install`): each offer is fetched, its brief hash checked, shown in a native dialog (Cancel default,
10 min = Cancel), then the user's own claude starts from a fixed template (`buildLaunch`: `-p` bootstrap from bead fields,
`--allowedTools Bash,Read,Grep,Glob,TodoWrite`, edit/web/Task tools disallowed, `--strict-mcp-config`, settings in
`runs/<id>.launch/` with the guard before every tool and the mail hook, cwd `runs/<id>/` 0700, no API key or config-dir
env, `cmdr place prompt` as the role prompt). One run at a time; heartbeats every 30s; stop, a usage limit or quitting
(asked first) ends its process group: a run never outlives the app (if the app dies, heartbeats stop and cmdrd settles it).
**Guard** `cmdr internal-operator-guard --role R --place read-only` (`operatorguard.ClassifyPlace[Tool]`), fails closed. It does not parse shell: `lexPlace` (`place_lex.go`) accepts only text bash/zsh can expand nothing in (plain chars and quotes; no braces, globs, `$`, backticks, escapes, `#`, parens, keywords, `~` other than a leading unquoted `~`/`~/`, unquoted here-doc end words, `<<<`, `&`), so its words are the shell's argv (`TestLexPlaceMatchesTheShell` diffs random lines against real bash, and zsh when installed); parent traversal (`..` path components) and `env` wrappers are refused before normalization/unwrapping; paths are resolved (tilde, joined to the run dir, cleaned, existing-prefix symlinks) before the dir check; a line naming `COMMANDER_PLACE_` is refused; curl/wget take an allow-list of read flags. Then:
pure reads (Manager guard read lists, no network; no cd/pushd, export/unset or variable assignments, no `~name`,
attached short-option paths judged, no `$` in text sent to the server; `cmdr place` verbs run bare and a report file
must be in the run dir, whose `<runs>/<run id>` private shape the report verb also checks) or a CLI + read subcommand from the user's `~/.commander/place-policy.toml`
(`read_dirs`, `[[cli]] name/read/production`; production only with `COMMANDER_PROD_GATE`), paths inside the run dir or a
read_dir, writes only into the run dir; refused: installs, sudo, launchd, defaults, Keychain, SSH, interpreters/shells,
claude/codex, osascript/open, writes, non-GET network, credential/browser/policy paths, edit/Task/web/MCP tools; refusals
in `logs/place-refusals.jsonl`, sent with the next beat. A connection is bound to its pairing (device key + paired_at): every frame but ping, offers and mail signals re-read the place and a revoked or re-paired one is retired (`Conn.Gone`, the socket ends; the minute sweep retires them too); test `TestRevokedConnectionTakesNothingAfterARepair`. The run verbs send the token only to the URL the app pinned in `<runs>/<id>.launch/place-url` (0600; `pinnedPlaceURL`, test `TestPlaceRunVerbsTalkOnlyToTheRecordedServer`). **Mac verbs:** `cmdr place report --file <run dir file>|comment
--text|mail --subject --body` and `cmdr internal-mail-hook --role R --place` use only the run env the app sets
(`COMMANDER_PLACE_URL/TOKEN/RUN/DIR`). Not built yet: the live first slice (needs install + pairing by the user), a
Settings > Places page, mail signals pushed over the channel (the hook asks at each tool call; cmdrd reads am only when
signalled). Tests: `internal/store` `TestPlace*`/`TestInsertPlaceOffer*`, `internal/place` (all), `internal/api`
`TestPlace*`/`TestDoorRefusesRunOn*`/`TestDoorComment*`, `cmd/cmdr` `TestPlace*`/`TestCheckRunOn`/`TestSpawnOperator_*Place*`/
`TestStopPlaceRuns`, `internal/operatorguard` `TestClassifyPlace*`, `internal/daemon` `TestSettlePlace*`, `internal/livedata`
`TestBoardPlaceOperator`, `app/electron/place-runner.test.ts`, eval `TestPlaceRunScenario` (`scenarios/place-run.json`,
real cmdr binary, real API, fake Mac `internal/place/placetest`).
The shared `applyDoor` also retains core intent custody and live-owner checks:
`TestPlaceIntegrationDoorIntentCustody` covers refusal for an agent and the
user's holder-first Manager receipt, response holders and pending board patches.

## Workstream Overview

The default workstream/track view is Overview, with Workflow as a second tab.
`internal/overview` is a tool-free, cached narrative reader; `/api/overview`
scopes evidence and never changes workflow state. Decisions and live activity
remain snapshot-derived. See `docs/features/workstream-overview.md` for the
reader's boundaries, stale-state behavior and Manager-result handoff semantics.
**Streaming and waits:** `POST /api/overview` never blocks on cmdrd's first snapshot (`overviewSnapshot` starts the load in the background): while the board loads or is degraded it answers 200 `state:waiting` with `waiting_for`; a failed reader run is `state:retrying` (30s retry), never `unavailable` (the app still accepts it from an old cmdrd). With `"stream":true` it writes NDJSON `Result`s, one per change while `updating`, each with `draft` (sentences finished so far, `overview.PartialRecap`, cited ids filtered to known evidence); `claude` runs with `--output-format stream-json --include-partial-messages` and reports through `overview.WithProgress`. The tab reads the stream and polls every 3s while waiting. Tests: `TestPartialRecap*`, `TestCacheShowsDraft*`, `TestClaudeStreamsDraftThenResult`, `TestOverviewWaitsPlainly*`, `TestOverviewStreamsDraft*`, `app/test/overview.test.tsx`; shot: `smoke.mjs --fake --fixture test/fixtures/snapshot-overview-streaming.json --query 'ws=1&flow=1'`.

Approval dispatch custody: `internal-resolve-needs` writes Manager ownership and
a `Manager dispatch` receipt on otherwise unheld released steps before closing
the gate. Handoff reminders retain the receipt after expiry and daemon restart;
mail delivery is not execution acceptance. See `docs/features/workstream-overview.md`.

**Close/create custody (never State missing; one model with the holding layer, il9.204.3.2):** the receipt
`until:...:Manager to start` (`beads.ManagerStartWait`, `beads.StartReceiptLabel`, 1h) is a holder like any wait
(`IsManagerHandoff`, so `WaitHolds` drops it 1h past its deadline). A write is refused only when an AGENT would leave
work with no holder at all (the refusal names the fix); the user's own writes (no role: CLI, the app's /api/cmd) are
never refused, they carry the receipt instead (`--leave-unheld '<reason>'` skips it, recorded). A close the door lets
through (the user, an Operator closing its own task, a Manager over the step) writes the receipt
(`cmd/cmdr/close_custody.go` `holdForManager`) on every step the fresh closure check says it leaves unheld, before
the close (a failed write closes nothing); a step a fresh read shows waiting (an Operator wait excepted: the
board already judged it), claimed by an Operator, the user's or (not started) behind another open blocker
gets none. `internal-create-objective` gives a new plain step (not gate, operational or nested workstream)
naming no holder the same receipt IN its create labels (atomic birth, `birthHolder`; result `custody`); one whose named
blocker is closed/parked or whose edge failed gets it right after the links. The board reads it "Manager to start the next
step", past its time stalled "Next step not started; Manager recovery needed"; a spawn under the step
(`acceptDispatchHandoff`, `beads.IsManagerDispatch`) or any real holder clears it; cmdrd reminds as for the
other handoff receipts. Every Manager custody receipt (`beads.IsManagerHandoff`: close, dispatch, start) is the
Manager's own move: it never refuses a needs-user card (`externalWait`) and is no previous target for the push
counter (`PrevTarget`/`UntilTarget`). A close bd refuses after the receipt landed takes it back (`releaseCustody`).
A start receipt on a step now behind an open blocker (a gate linked after the create) reads as nothing on the board
(`resultHandoff`), and at its deadline cmdrd clears it instead of mailing (`dropBlockedStart`). A start receipt's
line never hides a sibling nothing holds (`maskedBroken`: the row reads State missing); a reported Operator's
`Manager to close` still leads as before. An unplanned row reads `0% done` (app: empty ring), not 100%. A workstream or nested workstream with no step yet reads `idle` "Not started:
Manager to plan" (`hasPlan`), never "Nothing to do" (kept for finished work). `cmdr graph check` drops a
violation whose bead its own fresh br read shows closed or parked (`freshViolations`): the snapshot lags
that close. Not fixed: the board itself shows a close only once a load that began after it finishes (loads
of 10-40s under a slow project read). Tests `TestCloseDoor_*Custody*`, `TestCloseDoor_RefusedCloseTakesTheReceiptBack`, `TestHoldForManager*`,
`TestAskDoor_ManagerCustodyReceiptsHoldNothing`, `TestPushOnlyUntilAndParkedUntilCount`, `TestStartReceipt*` (livedata,
daemon), `TestCreateWhoseReceiptFails*`, `TestClearSettleMarkersKeeps*`, `TestCloseDoor_ClosingTheTaskAStepWaitedOn*`,
`TestCreateGivesAReadyStep*`, `TestStartReceipt*`, `TestGraphCheckDropsViolationsTheFreshReadClosed`, golden
`testdata/board/never-unheld.json`, eval `TestNeverUnheldScenario` (`scenarios/never-unheld.json`).

**Bounded review addenda:** `workflow add-review-addendum` records only the
registered owning Manager's current withdrawal of the extra failed-peer seal
policy, with an explicit independent assessor attestation or `--evidence-file`
current Manager transcription linked to exact retained assessor prose. The record
separates original assessor author/time/task roles from current Manager actor/time;
no new assessor output is implied. Both paths retain manifest checks. Optional `Proof.addendum{id,sha256}` keeps the original NOT CLEAN and
review attribution; ordinary close revalidates comments, artifacts, all task-run
roles and writer revision. This is the existing local role/hook trust boundary,
not malicious same-UID protection. See `docs/workflow-review-addendum.md`.
`AddendumVerdict` also runs the typed results (`resultVerdictRetaining`): a typed blocker/major finding or incomplete execution/coverage on the head refuses recording and close; only the exact retained report skips the legacy NOT CLEAN check.
Scenario `tests/evals/scenarios/review-addendum.json` runs in
`cmd/cmdr:TestWorkflowAddendumOrdinaryClose` so it exercises the real private
recording/completion doors without spawning an agent or duplicating their logic.

Explicit approval publication repair: `internal-gate publish --project P --bead G
--parent A --repair-ancestor-waits W1,W2 --reason R [--dry-run]` removes only named
strict non-root ancestors' redundant waits, retaining the guarded leaf wait and
original gate authority. Refused before any write when a listed waiter has other live
work (parked counts; only closed is not live) with no wait of its own on the gate and
not under a live, unlisted bead keeping one (`releasedDescendant`). Owning
Manager/Commander/user only; the full resulting graph is read once per run before the
first write, then only gate, leaf, chain and waiters between writes;
intent-before-write audit and exact-request retries. No general unlink exception. A
plain publish cycle refusal names the flag; `prompts/lead.md` teaches it. See
`docs/cli/publication-repair.md`; coverage `TestPublicationRepair*`,
`TestGatePublicationRepairCommand`, eval `TestPublicationRepairScenario`
(`scenarios/publication-repair.json`), real bd + real door handler + built cmdr with
lost answers: `TestPublicationRepairFiveWrappers_BDIntegration` (`COMMANDER_BD_INTEGRATION=1`, ~6 min).

**Workflow reporting:** the check-in and the app health badge count individual
workstream diagram findings (tracks are already included), matching scoped
`cmdr graph check`. `FindingKind` separates workflow errors from merge-review
blockers and advisories. A planned merge with no tracked PR is advisory on the board
and never suggests a fabricated PR URL; every close door still refuses it, since they
refuse on any `MergeRule` finding. `checkPlans` raises `plan:` health for errors only,
so the Manager's `[health]` mail no longer fires on merge-review blockers. **Check-in
headline** (`commanderPrompt`): health or degraded reads `NOT all on track, action
needed`; else a workflow error, suspect, stopped or stalled place or screen mismatch reads
`action needed`, and the instruction line names only those present. **`StalledWork`** (`livedata/stalled.go`)
exists because a row's line is its most urgent (Needs you, waiting and working outrank stalled) and holds no
rule of its own: it runs `workstreamLine` on copies with tracks and carded steps removed, so any line the board
reads stalled counts. A stalled track is one place with its steps. For the workstream and each other track,
every open step is read alone, deepest first (so a second stalled step is named, an open parent and child are
one place), then the whole level for what no step says (a stuck Manager, a pitch owed on the bead itself), then
the row's own stalled line unless a place already says it. In the copies a card's bead drops and the work under
it stays with a hidden wait; so does a step behind an open blocker outside the copy (a card, a track, a sibling, a
gate bead), under a step or track held by a wait or such a blocker, or only over a card (a card on a track itself holds nothing: the
track's own line reads as ever): none of it is the Manager's to move, though its facts (stopped Operator, review acted on, overdue
review; a plain time wait past its time reads Waiting and does not count) still count, since the board reads them before the wait.
A held step's own stopped Operator is named before the hold passes down (`stoppedOwner`; the board reads an Operator only on a leaf).
A blocker holds nothing when it waits on what it would hold (`holdsFrom`: its blockers and children, a gate's stage, guarded against
loops; closed, parked and superseded beads end the walk), so a stage's own gate holds neither the stage nor what is under it, and the
child a blocker waits on is listed. A person's approval (`verifier:user`) waits on nobody's work and always holds, its own stage too (a stage can wait on its own Greenlight). Not followed: a blocker's parents. A live Operator
holds its step and what is under it; a reacting Manager (`managerReacting`) holds everything. Read alone, a
ready Manager-held step with no wait reads `Stalled` even beside a sibling's wait, which the row hides. The
report lists each place (`Stopped or stalled`: bead, workstream (its bead), step, reason) and each `Screen
mismatches` line. Expected review blockers, advisories and needs-you items alone
keep the calm headline, listed beside it as counts (`all on track; 2 waiting on the user,
1 review blocker, 1 advisory`) with `Nothing is required`. A planned merge is advisory
only with no PR evidence at all (`TargetPR` empty, no `pr:` label on it or its stage);
else it blocks with a plug command naming the PR. **Badge:**
stays main's icon-only rail dot: counts are in its label and popover, `plan:` health
hides while the snapshot has `display` (its findings replace it), red for not working
or a workflow error, amber otherwise. Runtime health and screen mismatches stay
separate from workflow error counts. Tests: `TestCheckinFindingCountsIncludeTracksOnce`,
`TestCheckinHeadlineScenario` (`scenarios/checkin-headline.json`, private to cmdrd so it runs in `internal/daemon`),
`TestMergeRuleKnownPRIsNeverAdvisory`, `TestCommanderPrompt{HealthIssueIsActionNeeded,StoppedAgentIsActionNeeded}`,
`TestWorkflowCloseRefusesAPlannedMergeWithNoPR`, `TestTruthfulCheckinScenario`,
`TestCheckinCountsExpectedFindingsOnly`, `HealthBadge.test.tsx`.

Legacy stage demotion: internal-gate retire-legacy --project P --bead STAGE
--replacement GATE --reason R [--dry-run] removes only redundant gate and
approver:user on an ordinary open stage, preserving its blocking wait and the
sole genuine unapproved replacement/card/generation. Owning Manager/Commander/user
only; intent-before-write audit and exact-request recovery, no atomic backend
check/write guarantee. See docs/cli/retire-legacy.md; synthetic unit/refusal/
interruption coverage TestGateRetireLegacy*, private-handler eval
TestRetireLegacyScenario (scenarios/retire-legacy.json).

Capacity refusals: the chat engine retains actual `ErrNoUsage` Manager start
failures in `CapacityFailures` until a successful plan or a successful live turn
on the same process clears them. Snapshot
`manager_capacity` carries them to the board: affected parent and nested rows
read `Manager out of usage` instead of pickup/start custody, with the actual
cause in Why and no invented deadline. User decisions, external waits and live
Operator work keep their line. CLI Manager mail now validates exact bead ownership using
`OwnerOfRole` before resolving registration aliases for delivery; obsolete nested
copies are skipped.
Automatic Operator starts also move from exhausted Claude accounts to guarded
Codex; explicit Claude choices, saved Claude pins and local service requirements
refuse instead. Tests: `TestCapacityFailure*`, `TestRouteManagerRecipients*`,
`TestResolveProviderClaudeOutRespectsIntent`; evals `TestManagerCapacityScenario`
(`scenarios/manager-capacity.json`) and `TestSpawnUsageMoveScenario`.

## Unified Commander accounts

Commander starts and handles usage limits through `planChatStart`: prefer the chosen account, then the current account, then another connected Claude or Codex account with usage left. Provider choices are preferences for Commander, so a Claude choice still falls back to Codex. Runtime/account intent and the shared conversation log survive the move; a continuation reads the handover and mail. Managers retain their existing policy. `cmdr account use <role> codex` and the chat account endpoint accept Codex alongside named Claude profiles. `AutoSwitch` is always on for Commander and Managers.

Only `claudeAiOauth.accessToken` counts as a Claude file login: `.credentials.json` may contain MCP logins after Claude has signed out. Codex requires actual auth fields, not merely an auth file. No credentials are changed by listing accounts. The UI names a profile whose email is unavailable, offers either provider under Add account, and hides signed-out empty profiles. TopBar shows the reported model beside the chat title as regular muted text (Sol 6.1, Opus 5.5); the provider bubble is removed.

Verification: `TestCommanderFallsBackAcrossProviders`, `TestCommanderPlanSkipsDisconnectedAndExhaustedAccounts`, `TestCodexIsSelectableAsAnAccount`, `TestMCPOnlyCredentialsAreNotAConnectedClaudeAccount`; eval `TestUnifiedAccountsScenario` / `scenarios/unified-accounts.json`; app `top-bar.test.tsx`, `settings-accounts.test.tsx`, `providers.test.ts`, `ChatPane.test.tsx`; isolated cockpit and headless renderer smoke.

**Secret scan:** `.github/workflows/gitleaks.yml` and `internal-ship` scan historical changes plus a synthetic cumulative base..tip commit. Rules are vendored in `internal/ship/gitleaks.toml` (v8.21.2 defaults, no global/path allowlists), embedded in cmdr; the Action reads this tool asset from trusted main or BEFORE on a main push and fails closed if absent. Repository config/ignores and inline allows cannot suppress findings. Both scan a private source, use --text/--no-renames/--no-textconv/--no-ext-diff/--diff-merges=first-parent and reject incomplete/error scans. Missing local gitleaks still warns/skips. See `docs/secret-scanning.md` for bootstrap and limits. Tests: `internal/ship/gitleaks_test.go`, real detector via COMMANDER_TEST_GITLEAKS, eval `TestShipDryRunScenario`.

## PostgreSQL-selected live state

The pending orchestration replacement is defined in
[docs/architecture/commander-jurisdictions.md](docs/architecture/commander-jurisdictions.md).
Planning jurisdiction belongs to logical Commander conversations and inherits
through the shared bead tree. Generic execution responsibility is separate.
Workstream grouping does not require a permanent Manager process. Scope
handoffs preserve nested overrides, execution claims, holds and decisions;
planning writes and handoffs serialize through the same project graph lock.
Do not use a role-name guess or a stale registration to override jurisdiction.

See [docs/architecture/postgres-live-state.md](docs/architecture/postgres-live-state.md)
for the canonical bead/runtime schema, guarded interfaces, event stream,
subscription-backed classifier, migration verification and cutover/rollback
contract. A private `COMMANDER_HOME/state.postgres` selects that backend and
never falls back to SQLite/Dolt on failure. Use `cmdr bead --project-dir PATH`
for compatible reads/comments and the existing guarded cmdr doors for mutations.
Never test against the configured live database; integration fixtures use
`commander_trial` with isolated schemas. New Postgres Operators use the host
runtime and immutable run-aware state hooks. Old SQLite/Dolt remain legacy mode
only when the explicit Postgres selector is absent.

Versioned planning batches use `cmdr plan apply --file plan.json` through the
guarded `/api/plan` route backed by the existing write door. Include the read jurisdiction revision and
plan revision for every existing bead/parent/dependency endpoint; checks and
edits share one project graph lock and transaction. Missing service, stale
basis or foreign jurisdiction refuses the whole batch. Runtime executors cannot
submit planning batches. Dependency changes version both endpoints; child
structure changes version parents. See the architecture document above for the
JSON contract and remaining cutover requirements.

Planning batches support `create ... --ref name` followed by `$name` in bead
identity positions. References do not expand inside briefs or evidence. The
`created` response map and commit receipt preserve IDs on replay. Supply a
stable `basis.request_id` when retrying a request; identical retries return the
original result, while changed content under that key refuses. Without an
explicit key, the exact basis/operations/holding intent determine it. Guards
re-run against resolved beads inside the write transaction.

Cross-jurisdiction changes use `cmdr plan propose --file plan.json`, the durable
question inbox, then `cmdr plan apply --proposal ID`. Owners inspect the saved
payload with `cmdr plan get --id ID` before answering. Agreement is exact-payload,
requester, ownership and revision bound; it never substitutes for release gates.
In active mode, non-planning agents may only report current-run execution
phases/status/comments on their claimed bead; the shared PostgreSQL write path
refuses tree/planning/hold/gate/closure changes, including through legacy doors.

Active PG human-direction invariants live at the shared mutation boundary:
only a current typed human decision can release a hold or finish its gate.
Question scope and restriction epochs prevent stale answers or replayed journals
from approving changed work. UI decisions carry their displayed plan revision.
A deferred/retired approval remains an unpassed dependency; park abandoned work
instead of reporting it complete. Finish legacy saved answers in shadow and
audit historical unanswered cards before active cutover; do not synthesize scope
proof for an old answer. The shared `internal/workflowclose` engine now verifies active API closes inside
the same PostgreSQL transaction as their evidence and state writes. Typed CLI
checks use the same engine. Direct active closes without a verifier fail closed.
Typed no-PR pass/remove/replacement carry a definition-bound request; the
service verifies it under the write lock and never trusts an arbitrary waiver
comment. Workflow identity/snapshots cannot be erased, retirement verifies the
old requirement first, and parked workflows remain unpassed dependencies.
Ordinary automatic checks use the service-owned `/api/gate/check` path: evaluate
outside the graph lock, then compare definition and recorded versions before an
atomic result commit. CLI and background checks delegate once; unchanged
observations do not renew progress. Typed automatic facts cannot be forged via
comments, status writes or failure labels. Optional mail/display availability
cannot disable canonical checking. Before cutover, finish runtime integration and cutover recovery;
the shared engine does not by itself prove those remaining paths are integrated.

PostgreSQL review addendum recording uses current planning jurisdiction rather
than permanent Manager registration. The service stamps the recorder/revision
in existing bead-comment metadata inside the write transaction. Historical
resolution requires that exact stamp for Commander records and preserves the
legacy Manager path for older history; a handoff does not discard the record.
Original source bytes, artifact and independence checks still run at completion.
The sidebar preserves deferred/retired required checks as explicit blockers,
using a project-scoped dependency index and retained workflow-step metadata.
See `TestPostgresPlanReviewAddendumSurvivesCommanderHandoff` and
`TestRequiredWorkflowStepSurvivesDisplayProjection`.

Execution activity hooks are fenced by recorded run/session identity for generic
executor and Commander-named executions as well as historical Operator roles.
Logical planning chats without execution history retain their chat-presence
path; that is not a bead execution claim or work progress. Native starts now
require existing custody and fresh execution uses canonical capacity admission;
validated cross-provider checkpoint resumption remains cutover work.
`TestPostgresRunHooksFenceGenericAndCommanderExecutions` covers replacement on a
resumed native session, foreign sessions and late closed-run hooks.

Claimed execution now uses `prompts/executor.md` via the spawn request's claim
identity, including initial/resume goal and checkpoint context. It teaches local
implementation, assigned-bead evidence and durable questions; no executor tree
editing or permanent Manager delegation. A missing model on a scheduler request
resolves the configured execution default before launch validation. PostgreSQL
owner read failures refuse launch without historical Manager fallback.
Native scoped Commanders and active default Commander use
`prompts/planning-commander.md`; legacy default off/shadow recovery is retained.
Native resolver and cockpit refresh existing prompt files from canonical mode
without changing session identity. Active cockpit startup skips legacy Manager
creation. `TestClaimedExecutionLaunchCarriesCanonicalContractAndIdentity` uses
private PG, actual Worker/Spawner/HTTP and mocked detached tmux for code/knowledge
and Claude/Codex; `TestCommanderPromptMatchesActiveJurisdictionModel` and
`TestCommanderSessionRefreshesPlanningPromptAfterActivation` cover planning
entry points. No real model process starts in these tests; final live cockpit,
raw scheduler/native custody integration and validated cross-provider recovery
remain unverified.

`TestCommanderRuntimeContractInIsolatedPostgresCockpit` now verifies the rebuilt
CLI with `scripts/cockpit-test.sh up/text/down` on a private tmux server and
private PostgreSQL, real model binaries disabled. It proves canonical workstream
rendering, active planning prompt installation and no legacy Manager windows.
It does not prove the full live execution/UI flow. The text capture still shows
older cockpit status wording (`no agent yet`, no-gates hint and Managers tab
help), which remains part of the final active-UI audit.

Canonical publication and redundant-ancestor repair now delegate once through
`/api/gate/publish`. The service checks fresh planning versions/ownership under
the project lock; the gate library validates leaf coverage/cycles before private
exact edge/placement/audit authority is activated. Human approval, protected leaf
waits, criteria, prerequisites and history remain intact. Original/completion
audit, edges, parent and receipt commit together; late failure rolls all back.
Same request receipts replay without progress, dry run writes nothing, saved
human answers require their recovery first. Ordinary publication uses the same
path; scoped Commanders no longer hit a legacy Manager-only CLI precheck.
Cross-owner action coordination uses an exact typed publication proposal and
cannot borrow an unrelated plan grant. The existing planning receipt table is used with a typed
action hash namespace. `TestPostgresPlanPublicationRepairIsAtomicAndRetainsApproval`,
`TestPostgresPlanPublicationRepairRefusesUnsafeOrForeignState` and
`TestPostgresPlanPublicationCLIUsesCanonicalServiceOnce` cover real PG/HTTP/CLI.
A raw relationship-removal bug also fixed: --type only deletes that relationship,
so discovered-from cannot skip gate guards then delete a blocks edge. Parent
relationship removal is refused in favor of guarded SetParent.

Planning coordination uses its actual versioned bead dependencies, including
closed records, and an explicit project scope for top-level creation. It never
creates a placeholder task to hold a project decision. Planning questions have a
separate kind and do not advance execution progress or queue an execution resume.
Pending proposals are superseded by jurisdiction changes; execution questions
instead follow their current owner. Scopes are bounded by MaxQuestionScopes.
Durable planning delivery shares supervision's per-recipient dispatch fence and
combines execution obligations with planning notices. Shadow mode does not wake
agents. Notice delivery is neither an answer nor an approval; leases, current
ownership, retry backoff and answer checks survive worker restart. Repeated
failure routes a delivery-repair request to Commander without granting authority
to answer outside its scope. Exact cross-owner publication-action proposals use a separately namespaced hash
and the typed endpoint, shared durable questions, and current-owner agreement.
Use cmdr plan propose/apply with an exclusive publication object; never mix it
with ops or holding intent. All original approval and publication guards remain.

Obsolete saved human decisions now have a typed reconciliation interface:
`cmdr decision inspect --project-id ID --bead ID`, then (only when the recorded
scope cannot authorize current work) `cmdr decision reconcile --file PATH` via
`/api/decision/reconcile`. Current Commander, exact source answer/generation and
read versions are fenced under the graph lock. The service preserves original
payload/history, supersedes only the obsolete unapplied journal, records a fresh
unanswered question, and saves a replay receipt in
`human_decision_reconciliations`. It never edits holds, gates or execution facts,
renews approval, or counts administrative retirement as progress. Current valid
answers must resume; applied answers and answered historical pitches missing a
journal refuse replacement. Historical unanswered questions without scope proof
can be re-raised explicitly with current proof, never rebound to an old answer.
The planning prompt and supervisor name the actual tool. Populated migration,
shadow draining and live verification are still release requirements.

Imported planning boundaries now carry origin=legacy; all explicit transfers,
including same-owner ones, set origin=explicit. `cmdr jurisdiction inventory
--project-id ID` reads the exact imported boundaries and planning snapshot.
`cmdr jurisdiction consolidate --file PATH` delegates once to
`/api/jurisdiction/consolidate`: shadow-only, user/default-Commander authority,
exact jurisdiction/control/snapshot basis, atomic imported-boundary removal,
inheritance history, responsibility fencing and replay receipt. Explicit scopes,
claims, run/session identity, work, human direction and execution progress stay
intact. Set-based writes avoid per-bead database round trips. A returned retired
planning identity is only a cleanup candidate: runtime retirement still needs
cross-project ownership and execution checks. This transition does not stop
models or erase legacy sessions. Safe runtime cleanup and final activation
preflight remain required before live cutover.

Execution attribution now outranks planning registration. In active mode, any
run/session/inherited-Manager caller must prove its exact current run to report
phases/status/comments, even if its role is a registered Commander or imported
Manager. It cannot borrow planning authority, including through plan receipts,
proposals, typed publication/check/waiver/addendum routes or direct CLI readiness,
question-answer, human-decision, jurisdiction and orchestration commands. Normal
logical planning contexts keep their own scopes. DoorEnvKeep preserves Manager
attribution; explicit WithDoorEnv contexts (even empty) clear ambient attribution.
`commander-execution-context.json` drives actual PG/HTTP tests, including live
custody across handoff and registered/imported/dotted role spellings. Foreground
claims and safe retirement remain pending; do not infer them from these guards.

Active native StoreResolver now binds existing canonical execution runs before
legacy role-prefix resolution. Spec.ExecutionRunID, exact session/workspace,
execution prompt/current task, stamped host handshake and immutable child env
flow into native state writes via SetAgentStateForRun. Claims are rechecked
before process launch; held/blocked executions cannot start fresh processes but
can adopt their stamped surviving host. Late process frames/exits cannot update
a replacement chat/run. History without a live spec does not persist activity.
Execution Codex thread files use immutable-session keys, without role-level old
handoff reuse. Imported planning identities use planning-commander; unowned
consolidation-retired planners cannot restart. Fake native hosts + PG tests cover
these paths; legacy unstamped host migration, live retirement and complete
UI/provider flow remain pending. The reservation predicate's optional resume_existing defaults false;
only native existing-run resume permits in_progress, preserving dispatch checks.


Native bound executions now finish one turn, then close stdin and bound physical
process shutdown independently of the chat daemon. The host durably records the
actual exit with role/run/session identity before exposing it. Run-aware receipt
reconciliation releases canonical capacity under the graph lock, preserves the
first terminal verdict, and never closes the bead or invents a human decision.
Failed database commits retain receipts and prevent a fresh restart; heartbeat
and daemon recovery retry them. Invalid receipts are quarantined and an unrelated
unverifiable receipt does not prevent other commits. Socket disconnection alone
keeps the execution claim and permits adoption of the same surviving process.
Saved checkpoint answers resume only after the original process exit through
normal capacity claims. Native usage limits do not trigger legacy Manager-name
inline restart; native Codex checkpoint lookup follows its exact recorded run
and session thread. Scenario commander-native-exit-lifecycle.json drives real
fake-model host / isolated PostgreSQL tests, including daemon absence, failed
commit recovery, capacity reuse, checkpoint resume and live socket reconnection.
The actual fake Codex host lifecycle is also tested, including physical exit,
capacity closure and replacement-session fresh thread/start without thread/resume.
This does not establish raw scheduler/native process custody migration, validated
cross-provider recovery, or the complete UI flow.


Fresh PostgreSQL execution admission is now fenced installation-wide. In active
mode, InsertAgentRun requires the canonical startup claim; role names, registered
planning identities and absence of a competing reservation do not grant a bypass.
The mode row is held FOR SHARE through run binding and reservation, serializing
with disable/cutover updates. Off mode refuses fresh launches. Shadow retains
legacy preparation for migration; already-recorded runs remain readable and can
finish after activation. Spawner checks mode and matching enabled, unbound,
unexpired reservation before workspace/provider preparation; binding revalidates
current plan, authority, capabilities, readiness, holds and capacity atomically.
Native role-only start resumes an existing canonical run; it does not manufacture
ready intent or a new claim. New execution is admitted by the ready-bead worker.
The commander-execution-admission scenario covers the role matrix, actual mode
lock contention, preserved imported custody, capacity and pre-launch refusal.
This admission fence does not establish controlled legacy-host drain, native/raw
custody migration, activation readiness preflight or complete sidebar behavior.


Claimed execution now enters the daemon's run-bound native chat Engine after
normal guarded workspace preparation (Spawner.WithNativeExecution). Neither
claimed code nor knowledge work creates a tmux/container shell execution path.
Native-driver runs persist provider/account/model selection in PostgreSQL;
NativeExecution resolution ignores later role preference and usage-model changes.
The host owns and journals its initial instruction before delivering it, closing
the bind/start/send crash gap. Physical one-turn exit uses the existing durable
native receipt. Startup failure retains custody and workspace for same-identity
daemon recovery; canonical retry fields bound retries at one minute/five attempts,
without treating holds as failures or creating human decisions. Runtime/session
reset cannot replace a live bound execution. Off mode may adopt a bound surviving
host but refuses fresh process startup. The native-worker scenario uses actual
Worker/Spawner/HTTP/PostgreSQL/worktree/native-host/fake-Claude processes,
including adoption and one initial instruction. Actual Worker-to-Codex and native checkpoint continuation now have full-flow
proof below; ceiling enforcement remains to be verified;
legacy attestation/drain, plan revocation and activation remain release work.


Native checkpoint continuation now uses the DB source run binding through the
real worker and spawner. GetAgentRun returns the immutable driver/provider/account/
model fields; the worker pins those for continuation, and transaction binding
rejects provider/account changes without a validated handoff. Native source SID
comes from the source run, independently of derived role session files. The
question CLI uses the same native binding and pure session-keyed Codex cursor;
mutable role preferences cannot change its recorded checkpoint provider. Blocking
native Codex questions need their current thread, while nonblocking questions do
not require paused-context metadata.

Code continuation reuses the guarded existing worktree/branch, preserving prepared
files and commits. Claude requires a real transcript at engine launch. Codex may
restore a missing derived cursor from the exact DB-saved thread, never overwrite
a conflicting cursor, and must receive a successful same-thread resume from the
provider. require-resume crosses the actual bridge child argv; error/missing/
different-thread responses never silently call thread/start. The native-checkpoint
scenario names real worker/host/PG/private fake Claude+Codex flows for both project
kinds, plus missing-context/cursor/CLI/binding guards. Cross-provider translation,
legacy attestation/drain and full sidebar/cutover verification remain required.


Runtime presence and execution activity now use the same current-run evidence.
The daemon observes native role/run/session/PID identity, validates it against the
current bead claim, and records positive custody in PostgreSQL. A logical planning
chat, a newly inserted claim, stale role activity and an older session cannot
count as an executing agent. Evidence expires after 45 seconds; losing a local
engine connection leaves custody unknown rather than inventing process death.
A physical exit invalidates only its exact run/session. Both fleet/row counts and
nested frontend spinners use this verified execution set; unverified claims have
a separate checking state. Presence does not advance work progress.

The native-worker scenario now covers actual private Worker/Spawner/native-host
process evidence entering Source.Snapshot and disappearing on physical exit for
both code and knowledge tasks. Replacement-identity tests reject old observations
and exit receipts. Frontend tests reject stale nested working flags, planning
chat activity and wrong-project/non-run entries. New shared account ordering is
wired through PostgreSQL, settings, planning engine and fresh execution worker;
bound checkpoint continuations retain their recorded provider. Fresh executions
cannot escape a saved order to an unlisted account when its preferred provider is
unavailable or cannot support the task.

This is not cutover proof. The detached cockpit smoke renders an isolated empty
fleet; full populated PostgreSQL cockpit validation, old-process attestation and
retirement, run revocation/result delivery, migration rehearsal, release-wide
checks and the single authorized PR/install cutover still remain.


Native chat events now journal their captured immutable run ID. The status worker
consumes generic executions, including runtime permission/question/plan requests,
with the existing two-worker/token-reservation budget and normal account runner.
Classification validates current bead/run/session before updating its evidence
cursor, clears evidence across run changes, and validates the same identity before
publishing. Logical planning summaries require the current planning owner. The
UI accepts activity descriptions only for the matching observed run/session;
interpretation cannot turn a planning chat into an execution or advance progress.
Subscriptions retire after executions stop and are joined on worker shutdown.

The native-worker scenario drives this full path through the actual fake native
host and a deterministic classifier runner for code and knowledge work. Dedicated
store/UI tests cover late reused-session events, generic roles, old job results,
role-only observations and unchanged progress. Account-preference read failures
refuse a new planning launch rather than silently using default accounts.


Load verification now uses 1,000 synthetic native executions with explicit
bead/run bindings and run-fenced state updates, rather than legacy role-only
presence. It sustains the existing 100-updates/second workload while repeatedly
reading Source.Snapshot. A diagnosed recursive SQL plan rescanned project rows
for each ancestor edge. Current identity and the compact parent graph now come
from one read-only repeatable-read transaction, with ancestry resolved in linear
walks. Normalized agent-state masking uses a map, and read-only diagram/workflow
walks avoid temporary copies of the entire step tree.

The unchanged acceptance gate passed with 1,984 committed updates over 20 seconds,
124 reads, median 134.6 ms, p95 158.9 ms and maximum 179.9 ms on the private VM test
database. This verifies database/snapshot throughput with simulated processes;
it does not claim the VM can run 1,000 model processes. Optional test-only CPU
profiling and query-plan diagnostics are available for investigating regressions.


Native exit receipts now preserve the bounded terminal report together with the
physical run/session exit. A single database transaction closes the execution,
retains an attributed unaccepted exit note in bead history, and records a result
for the current planning Commander. Explicit attributed final-report comments
remain preferred evidence. Missing reports produce an explicit inspection
obligation. No exit report closes a bead or grants approval. Current blocking
questions instead preserve their checkpoint/resume source across answer/exit
ordering. Failed commits retain receipts for replay; duplicate exits cannot
replace the first terminal outcome or another run's custody.

The API projection reads current result/run/time facts in a batch and shows
“Result ready for Commander review” with zero execution spinners. Result waits
remain held work rather than stopped/overdue work; saved human answers and
planning checkpoints keep priority. Actual private Claude host/worker tests for
code and knowledge cover this projection, report recovery across a jurisdiction
transfer, history surviving a new work intent, and question continuations.

The populated detached PostgreSQL cockpit now renders the same canonical result
state, with no legacy Manager window. Source.Current supplies a load and its
canonical snapshot together; the live-data wire carries that frame for terminal
consumers. PostgreSQL terminal readers use this projection for scope status,
execution counts, spinners and fleet metrics. Database failures return an explicit
unavailable frame and clear the sidebar instead of retaining a healthy-looking
saved frame. Legacy SQLite fixtures retain their existing presentation behavior.
Dedicated tests cover wire transport, ignored stale Manager activity, verified
execution spinners and database failure after a successful read. This live check
and the private fake-host flows establish this interaction, not production
migration, retirement or cutover readiness.


Runtime integrity now upgrades shipped PostgreSQL schema 2 through additive
schema 3; never edit an applied migration to install missing fields. The upgrade
preserves work, claims, human decisions and progress; runtime retirement is
attested and fenced against all-project ownership and execution changes.
Active sidebar status and direct Board formatting use verified executions plus
recorded planning obligations, rather than permanent Manager presence. Pending
planning is not activity or progress. Existing planning chats receive current
interface guidance through their normal prompt hook, while run-bound/execution
contexts cannot borrow planning instructions.

Verification includes runtime_integrity_migration_test.go, active_board_test.go,
planning_retirement tests, ready_plan_test.go and planning_context_test.go. The
ordinary-leaf CLI test proves a missing brief/criteria refusal can be repaired
through cmdr plan apply, then claimed by the generic scheduler, preserving holds.
Use the explicit restored-production rehearsal against a private trial copy
before the final installed-state check. A merged PR or installed binary alone
does not establish active orchestration, successful retirement or accurate UI.

**Automation failures:** `stalled` / `stuck` are red failure classes, separate from
user asks and their flags. Manager activity or unread mail never recolors a
failed handoff as working. `livedata.recoveryLine` carries cause, accountable
Manager (Commander for its own move), actual bead target and next recovery
action in `why`; rows, workflows and diagrams expose that detail. Operational
beads participate in dependency projection even without `kind:objective` or
`kind:task`. Closed reports retain their history but never keep a handoff open.
The existing wait watcher reads task handoffs too, fresh-checks custody and
persists one notification receipt; Commander's persisted evidence watch escalates
otherwise unreported failures once per unchanged failure. Generic lack of
delivery keeps its existing bounded observation window. These paths mail through
the existing canonical delivery mechanism; they create no worker, review, gate
or user card and never restore an old actor. Existing guarded dispatch and crash
restart budgets remain authoritative. Ordinary action choices only count as
approval requests when an option explicitly sets `approves:true`.
Coverage: recovery/automation-failure unit tests, interaction scenario
`tests/evals/scenarios/automation-failure.json`, renderer overview/diagram tests
and isolated fake cockpit + renderer proof.

Scoped review bootstrap: `workflow own-run --project P --project-dir ROOT --task T`
verifies immutable launcher identity against the current PostgreSQL active claim,
returning only the own run; read-only connections, no schema initialization or legacy
fallback. `review-exec --context-file FILE` (repeatable) preserves independent context
hashes; `--private-git` redirects transient objects into sealed run scratch without
changing sandbox permissions. Finite controller acceptance remains INCOMPLETE/source
not assessed. See [docs/cli/scoped-review-bootstrap.md](docs/cli/scoped-review-bootstrap.md).

Explicit gate cancellation: `internal-gate withdraw --project P --bead G
--expected-state open --generation N --reason R --instruction CITATION --dry-run`
returns `expected_snapshot`; repeat without dry-run and with
`--expected-snapshot HASH`. Owning Manager/Commander/user only. Intent-before-write
exact retries retain scope/pitches/journal, defer with distinct `withdrawn`, and
remove only the active ask. Every nonclosed incoming waiter/child (parked and
superseded included), execution holder, answered/approved gate and stale snapshot
refuses. Withdrawn never grants workflow/production approval, including after a
later close. Fresh checks are not backend CAS. See `docs/cli/gate-withdrawal.md`;
`TestGateWithdraw*`, private-handler `TestGateWithdrawalScenario`
(`scenarios/gate-withdrawal.json`).

**Completion authority:** `internal-gate pass` checks the caller before workflow
or result writes on a user-verifier or user-approver gate (legacy aliases read
through ownercompat). Only roleless user and Commander pass; inherited Operator
context refuses, and `--by` supplies attribution only. Answered approvals still
use their retained answer path. `internal-close-objective` first reads fresh
actual child edges and statuses: ordinary, task, nested and deferred children
must finish, and a recorded review wait refuses completion. Closed children
remain folded history. Read failures refuse before custody writes. These CLI
checks share the existing role/environment trust boundary and are not an atomic
backend check-and-close transaction. Unit/eval:
`TestUserGatePassAuthority`, `TestCloseParent*`, `TestCompletionAuthorityScenario`
(`tests/evals/scenarios/completion-authority.json`, private command-handler eval).

Ordinary parent repair: `internal-place --ordinary --project P --bead B --parent W
[--dry-run]` changes only the parent of an ordinary nested step/existing gate.
It refuses ownership transfers, workstream/operational/Operator-task conversion,
incompatible placement, dependency cycles and newly unheld work; evidence failures
refuse. No wrappers, Manager changes, gate/card/journal or run-provenance edits.
See `docs/cli/ordinary-place.md` for limits and coverage (`TestOrdinaryPlace*`,
private-handler eval `scenarios/ordinary-place.json`).
Cross-project custody source boundary: CLI closure and HTTP intent planning refuse
`beads.CrossProjectError` while preserving local-read fail-open behavior. Optional
`CustodySnapshotReader` binds closure reads and receipt locality checks to one
snapshot; foreign receipts refuse before comments or writes. This is prerequisite
source boundary; the composed PG adapter supports qualified dependencies. See
[the boundary and remaining coordinated work](docs/architecture/cross-project-custody-boundary.md).
Tests: `TestCrossProjectCustodyCLI`, `TestCrossProjectCustodyForeignReceipt`,
private HTTP eval `TestCrossProjectCustodyScenario`
(`scenarios/cross-project-custody-boundary.json`).

Qualified PostgreSQL dependency SOURCE CANDIDATE: see
[qualified lifecycle](docs/architecture/qualified-postgres-lifecycle.md).
Canonical runtime migrations 1–3 remain unchanged. Additive migration 4 retains
reserved Mac places SQL (158f0064); 5 adds qualified graphs. Canonical 0–5
converge under one transaction; historical b6a345cb 2/3 refuse by lineage/shape.
See docs/architecture/mac-runtime-composition.md.
No live apply/install/ship is authorized by source tests. Explicit `internal-dep --project SOURCE --target-project TARGET`
passes expected endpoints to the transaction; ID-only clients remain unchanged.
Native coverage is `TestPostgresQualified*` in beads/store/livedata/cmdr and
`tests/evals/scenarios/qualified-postgres-lifecycle.json`. Foreign custody release
rolls back without foreign receipts, including writes lacking Intent.
Scoped review bootstrap: `workflow own-run --project P --project-dir ROOT --task T`
verifies immutable launcher identity against the current PostgreSQL active claim,
returning only the own run; read-only connections, no schema initialization or legacy
fallback. `review-exec --context-file FILE` (repeatable) preserves independent context
hashes; `--private-git` redirects transient objects into sealed run scratch without
changing sandbox permissions. Finite controller acceptance remains INCOMPLETE/source
not assessed. See [docs/cli/scoped-review-bootstrap.md](docs/cli/scoped-review-bootstrap.md).
Explicit gate cancellation: `internal-gate withdraw --project P --bead G
--expected-state open --generation N --reason R --instruction CITATION --dry-run`
returns `expected_snapshot`; repeat without dry-run and with
`--expected-snapshot HASH`. Owning Manager/Commander/user only. Intent-before-write
exact retries retain scope/pitches/journal, defer with distinct `withdrawn`, and
remove only the active ask. Every nonclosed incoming waiter/child (parked and
superseded included), execution holder, answered/approved gate and stale snapshot
refuses. Withdrawn never grants workflow/production approval, including after a
later close. Fresh checks are not backend CAS. See `docs/cli/gate-withdrawal.md`;
`TestGateWithdraw*`, private-handler `TestGateWithdrawalScenario`
(`scenarios/gate-withdrawal.json`).
