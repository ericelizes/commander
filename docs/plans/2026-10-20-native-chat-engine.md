# Native chat engine (`internal/chat`) in cmdrd

2026-10-20. Follows the user's decision in `2026-10-20-electron-cockpit.md` (full cutover
to native chat, T3 Code style) and the protocol facts in `spikes/native-chat/REPORT.md`.
Claude Code 2.1.305 (pinned, `chat.PinnedCLIVersion`).

This is the engine the Electron app renders every chat from. **The event model
below is the contract: change it only additively.**

## What it does

* **Owns the processes.** One `claude` per role (`commander`, `manager-<bead>`,
  `operator-<flat>`), started by cmdrd, never by the app, so chats survive app
  reloads. Argv (`chat.Args`):

  ```
  claude -p --input-format stream-json --output-format stream-json --verbose
    --include-partial-messages --include-hook-events --permission-prompt-tool stdio
    -n <agent> --append-system-prompt-file <~/.commander/claude-agents/<agent>.md>
    --model <tier model> --disallowedTools mcp__mcp-agent-mail,mcp__mcp_agent_mail
    --allowedTools 'Bash(cmdr:*),Bash(br:*),Bash(bd:*),Bash(git:*)'
    --settings <agentrt.ClaudeMailHookSettings(agent)>
    --session-id <id> | --resume <id>
  ```

  `--resume` when Claude already has a transcript for the id (else `--session-id`;
  reusing `--session-id` on an existing id fails "already in use"). Env: cmdrd's
  minus `ANTHROPIC_API_KEY`, plus `DISABLE_AUTOUPDATER=1`. Cwd: commander = the
  workspace root (`~/code`), Manager = its project dir, Operator = its worktree.
  Roles resolve through `chat.StoreResolver` (the existing `*.claude.session-id`
  files and the `workstream_managers` table; it never mints Manager/Operator ids).
  The CLI version is recorded (`claude --version`, init) and a mismatch with the
  pinned version emits a `notice` (`cli_version`); `init.capabilities` is exposed
  in status (we rely on `interrupt_receipt_v1`).
* **Hard single owner per session id.** Two processes on one id silently fork the
  history. `Start` refuses (`*chat.OwnedError`) when any of these hold:
  1. another role in this engine runs the id (`by: "engine"`),
  2. the flock `$COMMANDER_HOME/chat-locks/<id>.lock` is held (`by: "lock"`, holder
     line in `detail`). The claude child inherits the lock fd, so an orphan that
     outlives a crashed cmdrd keeps the lock until it exits,
  3. `ps -e -ww -o pid=,args=` shows another process with `--session-id <id>` or
     `--resume <id>` (`by: "terminal"` for the tmux TUI, `"headless"` for `-p`).
* **Normalizes** stream-json into `chat.Event` with a per-role seq (below).
* **Persists** every non-transient event to `$COMMANDER_HOME/chat/<role>.jsonl`
  (+ `<role>.meta.json`: session id, cumulative cost, running flag) and keeps the
  last 20k events (deltas included) in memory.
* **Hydrates** history the first time it runs a session that already has a Claude
  transcript (`~/.claude/projects/*/<id>.jsonl`, or `$CLAUDE_CONFIG_DIR/projects`):
  user/assistant/tool entries become the same Event kinds with `source:"transcript"`.
  The transcript's last `cost-state.totalCostUSD` seeds the cost baseline.
* **Derives agent state** exactly from events and writes it to `agent_state`
  (sidebar/livedata unchanged; hook rows and the pane scrape become the fallback
  for TUI chats). A heartbeat rewrites live roles every minute so long approval
  waits never look stale.

## Commands (Go API, `*chat.Engine`)

| Call | Behaviour |
|---|---|
| `Start(ctx, role, {Model})` | resolve, lock, hydrate, spawn. No-op if running. |
| `Send(ctx, role, text)` | `user_message` (origin `user`) then a stdin user line. |
| `DeliverMail(ctx, role, text)` | same, origin `mail`, only when idle (no turn, nothing pending); `ErrBusy` otherwise (the Stop mail hook covers mid-turn mail). |
| `Interrupt(ctx, role)` | `control_request {subtype: interrupt}`; no ack within 3 s -> SIGINT (ends the process; Start resumes). |
| `Approve(ctx, role, id, Decision)` | answers a `permission_request` or `plan`: `allow` / `allow_session` / `deny` (+`message`, `interrupt`, `updated_input`, `updated_permissions`). `allow_session` defaults to the CLI's suggestions rewritten to `destination:"session"`. |
| `Answer(ctx, role, id, answers)` | answers a `question` (AskUserQuestion): `{question text: chosen label}`; sent as `allow` with `updatedInput = input + {answers}`. |
| `Stop(ctx, role)` | deny pending, interrupt a running turn, close stdin, then SIGTERM/SIGKILL after 5 s each. Marks the role not-running. |
| `Status(role)`, `List()` | see `chat.Status`. |
| `Subscribe(role, since)` | history with seq > since (log, then ring) + a live channel, no gap/overlap. |
| `Handover(ctx, role, {Confirm, Force})` | TUI -> native (below). |
| `PopOut(ctx, role, {Confirm, Force})` | native -> TUI (below). |
| `Close()` | cmdrd shutdown: deny pending, close stdin; processes finish their turn and exit; roles stay marked running. |
| `ResumeRunning(ctx)` | cmdrd startup: restart roles marked running (retries for 2 min while an orphan still holds the lock). |

Unsupported CLI control requests (e.g. `hook_callback`) get an error
`control_response` so the CLI never hangs; `control_cancel_request` resolves the
pending request as `cancelled`; a turn's `result` cancels whatever it left pending.

## Handover and pop-out (built, tested with fakes, NOT performed)

Off unless cmdrd runs with `COMMANDER_CHAT_HANDOVER=1` **and** the call passes
`confirm: true`. The live cutover of the user's Commander/Managers is his decision.

* **Handover**: find the role's `@cmdr_chat` pane (`-n <agent>` in its start
  command, else the `@cmdr_ws` window, else window `commander`); refuse if the pane
  footer shows a running turn (`livedata.ClassifyPane`) unless `force`; replace the
  TUI with a placeholder via `tmux respawn-pane -k` (the same way soft restart stops
  chats); wait until `ps` shows no other owner; `Start` (resume + hydrate).
* **Placeholder** command starts with `CMDR_NATIVE_CHAT=<role>`; `layout.SoftRestart`
  now skips such panes, so a cockpit reload can never restart the TUI on a session
  the engine owns.
* **PopOut**: `Stop` (waits for exit and lock release), then `respawn-pane -k` the
  same pane with `agentrt.ClaudeInteractiveCmd` (it resumes the id).

## HTTP API (cmdrd, same token as `/api/*`)

`/api/cmd`'s allow-list is untouched; chat endpoints are typed, not argv.

| Method + path | Body | Reply |
|---|---|---|
| `GET /api/chat` | | `{"ok":true,"sessions":[Status...]}` |
| `GET /api/chat/{role}` | | `{"ok":true,"status":Status}` |
| `GET /api/chat/{role}/events?since=N` | WebSocket | every Event with seq > N, one `{"kind":"replay_done","seq":<last>}`, then live Events. Close code 4000 = too slow, reconnect with `since=<last seq>`. |
| `POST /api/chat/{role}/start` | `{"model"?}` | `{"ok","status"}` |
| `POST /api/chat/{role}/send` | `{"text", "start"?: bool, "model"?}` | |
| `POST /api/chat/{role}/interrupt` | `{}` | |
| `POST /api/chat/{role}/approve` | `{"id","decision":"allow"\|"allow_session"\|"deny","message"?,"interrupt"?,"updated_input"?,"updated_permissions"?}` | |
| `POST /api/chat/{role}/answer` | `{"id","answers":{"<question>":"<label>"}}` | |
| `POST /api/chat/{role}/stop` | `{}` | |
| `POST /api/chat/{role}/handover` | `{"confirm":true,"force"?}` | 403 unless enabled |
| `POST /api/chat/{role}/popout` | `{"confirm":true,"force"?}` | 403 unless enabled |

Errors: `{"ok":false,"error":"..."}` with 400 (bad role/body), 404 (no such pending
request), 409 (not running, busy, or owned: plus `"owned_by"` and `"pid"`), 403
(handover disabled), 503 (no engine).

`Status`:
```json
{"role":"manager-x","running":true,"session_id":"…","pid":123,"resumed":true,
 "state":"waiting","detail":"needs you: approve Bash","cwd":"/…","model":"claude-haiku-5-5",
 "cli_version":"2.1.305","capabilities":["interrupt_receipt_v1","…"],"last_seq":57,
 "total_cost_usd":0.0068,"started_at":"…","pending":[{"id":"…","tool_name":"Bash","input":{…},"suggestions":[…]}]}
```

## Event model (`chat.Event`)

Common fields: `seq` (strictly increasing per role, across restarts; gaps where
live-only kinds were not persisted), `role`, `session_id`, `kind`, `time`,
`source` (`live` from the process, `transcript` hydrated, `engine` made by cmdrd),
`parent_tool_use_id` (set inside a sub-agent: the Task tool_use id that owns it),
`message_id` + `block` (assistant content block), `uuid` (CLI uuid if any).

| kind | payload | persisted |
|---|---|---|
| `session` | `session{model,cwd,cli_version,permission_mode,capabilities,tools,slash_commands,mcp_servers,pid,resumed}`. Emitted twice per process: at spawn (`source:engine`) and at the first CLI init (`source:live`, full details). | yes |
| `user_message` | `text`, `origin` (`user`, `mail`, `transcript`, `subagent`, `replay`), `subtype:"slash_command"` for `/cmd` in transcripts | yes |
| `text_delta` / `text` | `text`; the final `text` per (`message_id`,`block`) replaces its deltas | delta no / final yes |
| `thinking_delta` / `thinking` | `text` (often empty: redacted) | delta no / final yes |
| `tool_start` | `tool{id,name}` | yes |
| `tool_input_delta` | `tool{partial_json}` | no |
| `tool_input` | `tool{id,name,input}` (final args) | yes |
| `tool_result` | `tool{id,name,content,is_error,structured,permission,truncated}`; content/structured capped at 32 KiB | yes |
| `permission_request` | `request{id,tool_name,tool_use_id,input,suggestions,description,blocked_path}` | yes |
| `question` | same `request`, tool AskUserQuestion (`input.questions[]`) | yes |
| `plan` | same `request`, tool ExitPlanMode (`input.plan`) | yes |
| `request_resolved` | `request{id,tool_name,decision: allow\|allow_session\|deny\|answered\|cancelled,message}` | yes |
| `status` | `status` (`requesting`, `thinking` + `tokens`, `interrupting`, `idle`, …) | no |
| `agent_state` | `state{state: working\|idle\|waiting\|stuck, detail}` | yes |
| `task` | `subtype` (`task_started`, `task_progress`, `task_notification`, …), `task{task_id,tool_use_id,description,status,summary,raw}` | yes |
| `compaction` | `compaction{trigger,pre_tokens,post_tokens}` | yes |
| `rate_limit` | `rate_limit` (CLI `rate_limit_info`, raw) | yes |
| `hook` | `hook{name,event,outcome,exit_code,output}` (e.g. the mail hook's injected mail) | yes |
| `notice` | `subtype` + `text` (`interrupted`, `compact_summary`, `local_command`, `cli_version`, `interrupt_fallback`, `popped_out`, CLI notifications, `unknown:<type>` with `raw`) | yes |
| `turn_result` | `result{subtype,is_error,terminal_reason,stop_reason,text,num_turns,duration_ms,duration_api_ms,cost_delta_usd,total_cost_usd,usage,model_usage,permission_denials}` | yes |
| `error` | `error` (stderr tail on a crash, failed control requests) | yes |
| `process_exit` | `exit{code,signal,expected}` | yes |
| `replay_done` | `seq` = last replayed (WebSocket only) | no |

Agent state (exact, from events): `stuck` if the process died mid-turn on its own;
`waiting` "needs you: approve <Tool>" / "needs you: question" / "needs you: plan"
while a request is pending; `working` (detail = current tool, e.g. `Bash: go test
./...`) during a turn; `waiting` "N sub-agents" between turns while tasks run;
else `idle`.

Cost: `cost_delta_usd` = this result's `total_cost_usd` minus the previous one
(Claude's total is cumulative per session and carried across `--resume`). The
baseline comes from meta, else the transcript's `cost-state`; unknown baseline
counts the first total once.

## Illustrative protocol exchange (synthetic identifiers and values)

The opt-in integration test is `COMMANDER_CHAT_REAL=1 go test -run TestRealClaude ./internal/chat/`.
The example below illustrates its event shapes; it is not a session recording.

```
run1 status: running=true pid=1234 resumed=false cli=2.1.305
{"seq":5,"kind":"hook","source":"live","hook":{"name":"UserPromptSubmit","outcome":"success","exit_code":0,
  "output":"{\"hookSpecificOutput\":{…\"additionalContext\":\"MAIL from commander: the code word is KIWI-7.\"}}\n"}}
{"seq":25,"kind":"permission_request","request":{"id":"request-sample-1","tool_name":"Bash","tool_use_id":"tool-sample-1",
  "input":{"command":"touch made-by-chat.txt && echo hi","description":"Create made-by-chat.txt and echo hi"},
  "suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"touch made-by-chat.txt && echo hi"}],
  "behavior":"allow","destination":"localSettings"},{"type":"addDirectories",…,"destination":"session"},
  {"type":"setMode","mode":"acceptEdits","destination":"session"}],"blocked_path":"<scratch>/made-by-chat.txt"}}
{"seq":27,"kind":"request_resolved","source":"engine","request":{"id":"request-sample-1","tool_name":"Bash","decision":"allow"}}
{"seq":30,"kind":"tool_result","tool":{"id":"tool-sample-1","name":"Bash","content":"hi",
  "structured":{"stdout":"hi","stderr":"","interrupted":false,…},"permission":{"decision":"accept","source":"user_temporary"}}}
{"seq":42,"kind":"turn_result","result":{"subtype":"success","terminal_reason":"completed","text":"hi\nKIWI-7",
  "num_turns":2,"cost_delta_usd":0.01,"total_cost_usd":0.01,…}}
interrupt: result error_during_execution/aborted_streaming after 5ms; still running=true
--- fresh engine, empty log (= cmdrd restart / Manager handed over) ---
run2: resumed=true hydrated=11 events; base cost 0.01
  1 user_message transcript "Run exactly `touch made-by-chat.txt && echo hi` …"
  3 tool_start Bash / 4 tool_input Bash / 5 tool_result "hi" / 7 text "hi\nKIWI-7"
  8 user_message "Count from 1 to 300 …" / 10 text "1\n2\n…23\n" / 11 notice interrupted
run2 turn: text "Created `made-by-chat.txt`, the command printed `hi`, and the code word is KIWI-7."
  turn_result cost_delta_usd 0.01 total 0.02
question: {"questions":[{"question":"Do you prefer red or blue?","header":"Color",
  "options":[{"label":"Red",…},{"label":"Blue",…}],"multiSelect":false}]}
ask turn: Answer({"Do you prefer red or blue?":"blue"}) -> text "blue"; cost_delta 0.01
```

A first attempt with `echo hi` got no prompt: Claude auto-allows read-only Bash.
After both runs no process referenced the throwaway ids (`ps` check).

## Verified vs not

Verified against the real CLI: spawn/argv, init/capabilities/version, hook events
(mail-style `additionalContext` reaches the model in `-p` mode), Bash
`can_use_tool` -> approve -> structured tool result, control-request interrupt
(ack in ms, process survives), resume in a fresh process with transcript
hydration and cost baseline, **AskUserQuestion via `can_use_tool` with
`updatedInput.answers`** (new; unverified in the spike), `ps` owner detection
against a real process. Verified with the fake (`chattest`): every command, deny,
plan approval, SIGINT fallback, crash -> stuck, unsupported control request,
tasks/sub-agent nesting, compaction, lock refusals (engine / flock / terminal /
headless), log replay across an engine restart, Close + ResumeRunning, handover
and pop-out against a fake tmux, the HTTP API + WebSocket replay.

Not verified: ExitPlanMode and `task_*`/sub-agent events from the real CLI (field
names follow T3 Code), MCP tool approvals, `control_cancel_request` from the real
CLI, auto-compaction and rate-limit errors in a long session, hydration of a real
Manager-sized transcript (only small ones), Handover/PopOut against real tmux
panes (deliberately not done), the cmdrd wiring live (cmdrd was not restarted).
No Electron UI exists yet, so there is no cockpit-level live check.

## Platform

POSIX only: `syscall.Flock`, `Setpgid`, `ps -e -ww -o pid=,args=` (same on BSD ps
and procps), SIGINT/SIGTERM. `GOOS=linux GOARCH=amd64 go build ./... && go vet ./...`
is clean. Claude's transcript root honours `$CLAUDE_CONFIG_DIR`. Nothing
macOS-specific (no launchctl, no Keychain handling: auth is the user's normal
Claude login, which `claude` resolves itself on either OS).

## Risks and open questions

1. **Permissions policy.** The tmux cockpit runs `--dangerously-skip-permissions`;
   native chats prompt for everything outside the commander CLIs. A handed-over
   Manager would start asking for approvals. `Spec.SkipPermissions` exists; the user
   should pick the default per tier before cutover.
2. **Protocol drift.** Pinned to 2.1.305 with a notice on mismatch; keep
   `TestRealClaude` (and the spike scenarios) as the drift eval after CLI upgrades.
3. **Interrupted turns cost nothing in Claude's totals**: the aborted turn's
   result repeats the previous `total_cost_usd`, so partial-turn spend is invisible.
4. **Orphans across a cmdrd crash**: the child keeps the lock until its turn ends;
   ResumeRunning retries for 2 minutes. A child stuck on a permission prompt when
   cmdrd crashes may wait forever (no stdin to answer); it is visible to `ps` and
   blocks a restart with `by:"headless"` until killed.
5. **Forked transcripts** are hydrated in file order (all branches); the spike's
   fork case would show both branches.
6. The appended system prompt is snapshotted at session creation, so a resumed
   session keeps its original role prompt (spike finding; unchanged).
7. `-p` loads all user MCP servers/skills/plugins (156 tools in the test run),
   adding start-up time and context; consider `--strict-mcp-config` for Operators.
8. The ring buffer is per cmdrd process; a client that reconnects after a cmdrd
   restart only gets persisted events (finals, not deltas) for turns in flight.

## Files

`internal/chat/{event,normalize,state,log,lock,transcript,spec,engine,handover}.go`,
`internal/chat/chattest/fakeclaude.go`, tests + `testdata/` (synthetic protocol stream
from the spike, trimmed real transcript), `internal/api/chat.go`,
`internal/daemon/chat.go`, small changes in `internal/api/api.go`,
`internal/daemon/daemon.go`, `internal/agentrt/claude.go` (exported tool lists),
`internal/layout/soft_restart.go` (placeholder skip).
