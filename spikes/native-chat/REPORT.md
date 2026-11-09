# Spike B: a native chat UI on Claude Code's stream-json mode

2026-10-20. Claude Code 2.1.305, Node 24. All runs
used `--model haiku`, a scratch cwd and throwaway session ids; no real session was
resumed. The original recordings and hook scripts are omitted from this tree.
`driver.mjs` is the zero-dependency scripted driver; tests use invented protocol
examples in `app/src/features/chat/__tests__/testdata/`.

## Recommendation: hybrid

1. **Commander and Managers: the real TUI in xterm.js** (Phase 2 as planned). Take
   agent state from the existing hooks, not footer scraping.
2. **Operators: native.** They already run `claude -p --output-format stream-json`
   (`agentrt.ClaudeHeadlessArgs`; `internal/spawn/host.go`, `internal/spawn/code.go`).
   Move them to stream-json input with `--permission-prompt-tool stdio`, owned by
   cmdrd: tool cards, a Stop button, exact state and cost, optional approvals, a
   message box that writes to stdin.
3. Later, maybe native Managers via a cmdrd-enforced single-owner handover (stop the
   TUI, then resume headless) with a "pop out to terminal" escape hatch.

Speak the protocol from Go, not through the Node SDK, so cmdrd owns the processes
and they survive Electron reloads. Pin the CLI version; check `init.capabilities`.

**Biggest finding:** two processes on one session id silently fork the history (no
lock, no error; last writer wins on resume). Handover only, never concurrent use.

**Bug found in passing:** `internal/ceilings/consumer.go:237` adds Claude's
`total_cost_usd` to a running total, but that field is cumulative per session and
persists across `--resume`. Fine for today's one-shot Operator runs; any multi-turn
or resumed stream would be overcounted. Take deltas.

## T3 Code (pingdotgg/t3code @ ca140c43, MIT), what it does

Code: `apps/server/src/orchestration-v2/Adapters/ClaudeAdapterV2.ts`,
`apps/server/src/provider/ClaudeProvider.ts`.

* Uses `@anthropic-ai/claude-agent-sdk` `query()` with the user's `claude` binary;
  one long-lived process per thread fed from a prompt queue; a Node WebSocket
  server owns the processes and serves web/desktop/mobile clients.
* Sessions: mints its own UUID; first open passes `sessionId`, later opens `resume`
  (reusing the id fails "already in use"); fork/rollback via the SDK; a fresh uuid
  per prompt; after a restart sends "Continue where you left off." (max 3 in 5 min).
* Permissions: `canUseTool` raises approval requests the UI answers (allow, allow
  for session, deny/cancel); `AskUserQuestion` becomes a user-input form;
  `ExitPlanMode` becomes a proposed-plan item. Interrupt = `query.interrupt()`,
  then close and resume from disk.
* Events: `assistant` snapshots become message items per content block; `tool_use`
  / `tool_result` become tool items; `system` status/compact/task events become
  nodes; `parent_tool_use_id` events go to child threads; `result` ends the turn.
* Auth: keychain OAuth; never overrides `HOME` (breaks the macOS keychain), isolates
  with `CLAUDE_CONFIG_DIR` only.
* Copy: one process per chat on a streaming-input queue with our own session ids
  (matches `agentrt`); approvals as a map of pending requests answered by buttons;
  small normalized event model; interrupt as a control request with kill-and-resume
  fallback. Do not copy: the Node SDK dependency.

## Evidence (real CLI)

Command: `claude -p --input-format stream-json --output-format stream-json --verbose
--include-partial-messages --session-id <uuid>|--resume <uuid> --append-system-prompt-file
p.md --disallowedTools mcp__mcp-agent-mail,mcp__mcp_agent_mail --permission-prompt-tool
stdio --settings '<hooks>'`. Stdin lines: `{"type":"user","message":{...}}`,
`{"type":"control_request","request_id":..,"request":{"subtype":"interrupt"}}`,
`{"type":"control_response",...}`.

* **Rendering:** append `text_delta` by block index; the `assistant` snapshot per
  block is final (first delta ~0.7-1 s after `status:requesting`); tool cards from
  `content_block_start tool_use` + `input_json_delta`; results from `user`
  `tool_result` (with structured `tool_use_result` and `permission_decision`);
  `thinking_tokens` for "thinking…".
* **Permissions** (no skip flag): `control_request can_use_tool` with
  `permission_suggestions` (e.g. `addRules ... localSettings`), answered with a
  `control_response` allow/deny. Maps to Allow once / Always / Deny buttons.
* **Interrupt:** control request acknowledged in 2 ms, `result error_during_execution
  terminal_reason=aborted_streaming`, process stays alive and the next turn works.
  SIGINT: same result, process exits in ~400 ms (hard stop, resumable). A mid-turn
  user message is queued and runs as the next turn.
* **Mail hooks work in `-p` stream mode:** UserPromptSubmit `additionalContext`
  reaches the model on every stdin message; Stop `{"decision":"block"}` continues the
  turn; hooks from `--settings` merge with global ones; `--include-hook-events` puts
  them in the stream. `cmdr internal-mail-hook` works unchanged; idle mail delivery
  becomes "write a user message to stdin".
* **Cost:** per-`result` `usage`, `modelUsage`, `num_turns`, `permission_denials`;
  `total_cost_usd` is cumulative and carried across resume (0.00505 -> 0.00714 over
  turns; the first result of a resumed run already reports 0.00714);
  `rate_limit_event` gives subscription utilisation.
* **Slash commands in stream mode:** `/context`, `/compact` (with `compact_boundary`
  pre/post tokens), `/cost` work; picker commands (`/model`, `/resume`, `/mcp`,
  `/agents`) have no UI in `-p`; 116 commands listed in `init`.
* **Resume:** headless -> headless OK; headless -> TUI OK; TUI-created -> headless OK;
  `-p --session-id` on an existing id fails "already in use".
* **Two processes, one session id:** the live TUI never sees the headless turn; the
  JSONL forks (both user turns share a parent); later `--resume` follows the latest
  leaf and silently orphans the other branch.
* Other: the appended system prompt is snapshotted on the first request (a resumed
  session keeps its original role prompt until compacted); `-p` loads all user MCP
  servers/skills/plugins.

## B (native) vs A (TUI in xterm.js)

| Criterion | A: xterm TUI | B: native stream-json |
|---|---|---|
| Slash commands, skills | exact, pickers included | prompt-style work; pickers must be rebuilt |
| Footer, esc-to-interrupt, bg agents, plan UI | free | ours to build; data is in the stream |
| Rich UI (cards, approval buttons, forms) | text only | structured, deep links into the inspector |
| Resume existing sessions | same pane | works both ways; one live process per id |
| Interrupt | Esc | control request, ack 2 ms |
| Permissions | skipped today | `can_use_tool` with suggestions |
| Mail hooks | work | work unchanged (verified) |
| Agent state | scrape or hooks | exact, push |
| Cost | parse transcript | `result` cumulative (deltas) + rate limits |
| Survives app quit | yes (tmux) | only if cmdrd owns the processes |
| Effort | ~4-5 days | ~2-3 weeks for a good chat; little for read-mostly Operators |
| Failure modes | pty/size fights, scrape drift | silent fork, protocol drift, feature treadmill, process ownership |

## Top risks

1. Silent history fork: cmdrd needs a hard single-owner lock per session id.
2. Raw-protocol drift: pin the CLI version; turn the driver scenarios into an eval.
3. The fidelity treadmill: new TUI features are missing natively until built.
4. The cost double-count bug in `consumer.go`.
5. Process ownership: reaping orphans, auto-resume after a cmdrd restart.

## Not verified

Subagent/Task nesting and `task_*` events, AskUserQuestion / ExitPlanMode via
`can_use_tool`, `/skill` in stream mode (T3's code only); MCP approval prompts;
long sessions, auto-compaction, rate-limit errors; resuming a Manager-sized
transcript; real Manager session ids; the xterm side (Spike A).
