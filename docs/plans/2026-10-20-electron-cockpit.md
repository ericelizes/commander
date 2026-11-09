# Electron cockpit on the existing engine

Status: Phase 0 built (2026-10-20): `internal/livedata` (UI-agnostic data layer,
TUI aliases it) + `internal/api` in cmdrd (`/api/snapshot`, `/api/events`
WebSocket, allow-listed `/api/cmd`, token auth); see CLAUDE.md. App milestone 1
(2026-10-20): `app/` Electron + React shell with the live navigator (chat and
inspector are placeholders), the same bundle served by cmdrd at `/app/` for
browsers/SSH, `cmdr app [--web]`; screenshots are generated locally. Author: Manager of the Commander workstream. Date: 2026-10-20.

## Why

The TUI cockpit is bounded by what a terminal can draw. The inspector wants to
embed arbitrary logged-in sites (Notion, Linear, GitHub, Datadog); we now fake
that with headless-Chrome screenshots over kitty graphics, which are static,
slow to refresh and not clickable. An HTML cockpit gives real webviews, real
forms for decision cards, real buttons and hot reload, and keeps the engine.

## What stays

Everything below the UI: `cmdr`, `cmdrd`, `state.db`, beads, agent-mail, the
tmux session that hosts the Claude chats, the `cmdr internal-*` commands, the
role prompts. Chats keep running when the app quits or reloads (they live in
tmux, not in the app). The TUI stays as a fallback and for SSH.

## Shape

```
┌ Electron main ────────────────────────────────────────────────────────────┐
│  window: left navigator │ middle: header + chat (xterm.js) │ right: tabs   │
│  renderer (Vite + React/Solid, HMR)   WebContentsView per inspector tab    │
└───────┬───────────────────────────┬───────────────────────────────────────┘
        │ WebSocket + JSON (state)  │ pty per visible chat (tmux attach)
   ┌────▼───────────┐          ┌────▼────────────┐
   │ cmdrd  /api    │          │ tmux  (commander)│  chats, Operators
   │ state.db, mail │          └──────────────────┘
   └────────────────┘
```

* Layout is the TUI's: left navigator (0 Commander, NEEDS YOU, WORKSTREAMS with
  one open dropdown, FLEET), middle title bar + the real Claude chat, right
  inspector (tab strip, cards, decision forms).
* The same renderer bundle is also served by `cmdrd` on `127.0.0.1:<port>` as a
  plain page: zero-install dev and a quick way to look at the cockpit from any
  browser. Features that need Electron (embedded logged-in webviews) degrade to
  "open in browser" there.

## Data API (cmdrd)

Today the TUI columns read `state.db` and beads directly and mutate through
`cmdr internal-*`. The app must not do that from a renderer, so cmdrd grows a
small local API; the TUI can adopt it later but need not.

* `GET /api/snapshot`: the same `CommanderLiveData` the sidebar builds today
  (projects, workstreams in rank order, steps tree, NEEDS YOU with decision
  cards, agent states, fleet metrics, mail unread). One struct, already exists
  in Go (`internal/tui/commander_data.go`): move it to a UI-agnostic package.
* `GET /api/events` (WebSocket): pushes a new snapshot when something changes
  (store write, bead change, hook state, mail). Replaces every polling tick.
* Commands: `POST /api/cmd` with `{argv: ["internal-set-objective", ...]}`
  restricted to an allow-list of `internal-*` verbs; the server runs the same
  code paths the CLI does. No new business logic in JS.
* Inspector: tabs table (`inspector_tabs`) over the same API; decision cards are
  already JSON in `needs_you.decision`.
* Auth: bind to loopback and require a random token written to
  `$COMMANDER_HOME/api-token` (0600); Electron reads the file, the localhost page
  gets the token via a one-time URL.

## Chat: tmux to xterm.js

The chat must be the real Claude Code, not a re-implementation. Options:

| | node-pty + `tmux attach` per pane | tmux control mode (`-CC`) |
|---|---|---|
| Fidelity | exact (it is a terminal) | exact output, we render it ourselves |
| Multiple chats | one pty per attached window | one control connection, many panes |
| Resize/scroll | tmux sizes to the pty; scrollback via tmux | we get pane sizes/history events |
| Complexity | low | high (parse `%output`, octal escapes, flow control) |
| Mouse/clipboard | works (xterm.js mouse reporting + tmux) | we implement |

Choice: **node-pty + `tmux attach-session -t commander \; select-window -t N`**,
one pty per open workstream chat, created lazily and kept while the tab is
alive. To show only the chat pane, attach a *grouped session*
(`new-session -t commander -s app-N`) with a hidden status line and
`select-pane -t <chat>` + `resize-pane -Z` (zoom) so xterm.js shows exactly the
chat pane, while the TUI sidebar/inspector panes stay in the real session. The
chat pane is found by its `@cmdr_chat` option, as today. Control mode remains
the fallback if grouped sessions fight over size (the known tmux "smallest
client wins" issue is avoided by `window-size latest` on the grouped sessions).

Typing, paste, Ctrl-keys and Claude's own UI all work unchanged because it is the
same pane.

## Inspector: real webviews

* Each tab is a `WebContentsView` with `session.fromPartition('persist:cockpit')`:
  one persistent partition, so the user signs into Notion, Linear, GitHub, Datadog
  and Slack once inside the app and stays signed in. Embedding in iframes does
  not work (X-Frame-Options/CSP frame-ancestors), which is why Electron over
  Tauri's webview here.
* Tab kinds: url (webview), cmd (re-run output as text), decision (React form,
  not a webview), html (a file served by cmdrd, so Managers can write rich pages
  such as dashboards), and the existing text renderers as a fallback.
* Decision cards: real buttons, keyboard 1-9, the recommended option marked, a
  text field, Submit calling `internal-resolve-needs --action decide`.
* Security: webviews get no node integration, `contextIsolation` on, navigation
  limited per tab (open external links in the default browser), permissions
  denied by default. The renderer talks to main only through a narrow preload
  API.

## Navigator and keys

* Same model as the TUI: one open dropdown, shared across windows, click or key
  to expand, again to go. Ctrl+0..9 (and Cmd on macOS) via Electron
  `globalShortcut`-free `before-input-event`, so it works even when xterm.js has
  focus; they are never sent to the chat.
* Focus rule: clicks in the navigator never keep the keyboard; focus returns to
  the chat terminal.
* Live agent state, working spinner, NEEDS YOU queue all come from the snapshot.

## Dev loop and packaging

* Vite + HMR for the renderer: UI edits show instantly, no restart; Electron
  main reloads on change; the app reconnects to cmdrd and re-attaches ptys.
* `cmdr app` starts cmdrd if needed and launches the app (dev: `npm run dev`
  proxying to cmdrd; prod: the packaged `.app`).
* Packaging: electron-builder, macOS first (arm64 dmg), unsigned/ad-hoc signed
  for the user's machine; no auto-update needed at first. The app is a thin client,
  so a rebuild is only needed for main/preload changes.
* Live dev mode (built 2026-10-20): an always-on renderer dev server
  (`npm run dev:renderer`, Vite on 127.0.0.1:5173 under `/app/`, kept up by the
  `org.commander-ui` launchd agent / `commander-ui.service`, installed
  with `cmdr app dev enable`). The installed app loads it instead of its bundle
  while it answers (checked every 3s, falls back when it dies; View menu and a
  title-bar badge say Live or Bundled), and cmdrd proxies `/app/` to it (HMR
  WebSocket included). So an agent editing `app/src` changes the user's UI within
  seconds, no rebuild, reinstall or restart. Main/preload edits still need a
  relaunch: `cmdr app dev` runs `electron-vite dev --watch` for that, only
  when asked.

## Migration

1. Phase 0 (done 2026-10-20): extract `CommanderLiveData` and friends into a UI-agnostic package,
   add `/api/snapshot` + `/api/events` + token. TUI unchanged (can switch to the
   API later).
2. Phase 1: localhost page: navigator + decision cards + text inspector (no chat
   terminal yet). Useful on its own.
3. Phase 2: Electron shell, xterm.js chat via grouped-session pty, Ctrl+n keys.
4. Phase 3: webview inspector tabs on the persistent partition, html tabs.
5. Phase 4: polish, packaging, make it the default (`cmdr` opens the app; `cmdr tui`
   keeps today's cockpit).

## Effort (one engineer, with Claude doing most of the typing)

| Phase | Estimate |
|---|---|
| 0 API + extraction | 2-3 days |
| 1 localhost page | 3-4 days |
| 2 Electron + xterm bridge | 4-5 days (bridge is the risk) |
| 3 webview tabs + html tabs | 3-4 days |
| 4 packaging/polish | 2-3 days |

About 3-4 weeks end to end; each phase ships something usable.

## Risks

* tmux bridge: size negotiation, scrollback, mouse and bracketed paste through a
  second client. Mitigation: grouped sessions with `window-size latest`; keep
  control mode in reserve; spike this first (1 day) before committing.
* Two UIs to keep in sync. Mitigation: all state/commands via one API; the TUI is
  frozen except fixes once the app is the default.
* Webview sign-in friction (Google SSO blocks embedded browsers). Mitigation:
  a "sign in via system browser" fallback is not possible for session cookies, so
  prefer email/token logins; verify Notion, Linear, GitHub and Datadog early.
* Electron weight (200 MB, memory). Accepted; the app is local-only.
* Security of a local API that can run `internal-*`: loopback + token + allow-list.
* Chats and the app both attached at once: two clients on the same window change
  its size; grouped sessions avoid this.

## Decision for the user

Go / no-go on starting with a 1-day tmux-bridge spike plus Phase 0, with the TUI
work continuing in parallel (no new TUI-only big features meanwhile).

## Spike results (2026-10-20): plan changes

Both spikes are in `spikes/xterm-bridge/REPORT.md` and `spikes/native-chat/REPORT.md`.

* **Chat rendering: hybrid.** Commander and Managers stay the real TUI in xterm.js
  (full fidelity, existing sessions, hooks). Operators render natively from
  `claude -p` stream-json (cards, Stop button, exact state/cost). Native Managers
  only later, via a single-owner handover: two processes on one session id silently
  fork the history (verified), so cmdrd must hold a per-session lock.
* **The "grouped session + window-size latest" claim in the bridge section is wrong.**
  A window has one size and one zoom; a second viewer (the TUI) fights it and cmdr's
  `__relayout` unzooms it. Phase 2 must make the app the only viewer of a chat window:
  skip zoomed windows in Relayout, or move the chat into an app-owned window
  (`break-pane`/`join-pane`). Also needed: key isolation, the XTVERSION/996n reply
  shim (else every lone Esc lags 500 ms), one pty per chat window, reaping `app-*`.
* Phase 0 (cmdrd `/api/snapshot` + WebSocket) is unaffected.
* Found: `internal/ceilings/consumer.go` double-counts Claude's cumulative
  `total_cost_usd` on multi-turn/resumed runs.

## Decision (the user, 2026-10-20): full cutover to native chat

the user chose native rendering for ALL chats, like T3 Code: the hybrid above is
superseded. Consequences for the plan:

* cmdrd owns every Claude process (`claude -p --input-format stream-json
  --output-format stream-json --verbose --include-partial-messages`, own session ids,
  `--resume`, `--permission-prompt-tool stdio`), speaking the protocol from Go. A
  per-session lock guarantees one live process per session id (two silently fork).
* Order: Phase 0 API; then Operators (already headless) -> Commander -> Managers, each
  by handover (stop the tmux TUI, resume headless on the same id, history preserved).
  Keep "pop out to terminal" (reverse handover) and the TUI cockpit as the fallback.
* Rebuild natively: message bubbles + streaming text, collapsible tool cards,
  approvals (Allow once / Always / Deny from `can_use_tool` suggestions), AskUserQuestion
  and ExitPlanMode forms, interrupt (control request), slash commands (prompt-style work;
  pickers /model /resume /mcp /agents need our own UI), status/background-agent/todo
  rows from `system`/`task_*` events, compaction markers, mail delivery as stdin user
  messages (hooks keep working), exact agent state and cost (take deltas).
* The xterm bridge shrinks to an optional "terminal view" of a session and is no longer
  on the critical path (its sizing issues are avoided).
* Effort: native chat core ~2-3 weeks on top of Phases 0-1; cutover per tier behind a
  flag. Pin the CLI version and keep the driver scenarios as an eval.
