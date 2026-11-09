# Native chat: integration notes

`app/src/features/chat/` is a self-contained native chat renderer for every
tier (Operators, Managers, Commander). It renders a normalized event stream
from cmdrd and sends user actions back. It has no Electron-only code: it runs
the same in the Electron renderer and as the plain web page cmdrd serves at
`/app/` (Linux VMs over an SSH port-forward). Shortcuts use Cmd on macOS and
Ctrl elsewhere.

## npm dependencies (exact versions the feature was built and tested with)

Runtime:

| package | version |
|---|---|
| react, react-dom | 19.3.1 (peer; 18.3 should work, untested) |
| react-markdown | 10.1.0 |
| remark-gfm | 4.0.1 |
| @tanstack/react-virtual | 3.14.21 |

Dev/test:

| package | version |
|---|---|
| vitest | 5.0.5 |
| jsdom | 30.1.3 |
| @testing-library/react | 16.3.3 |
| @testing-library/user-event | 14.6.8 |
| @testing-library/jest-dom | 7.0.1 |
| typescript | 7.0.2 (`tsc --noEmit` clean, `strict`, `moduleResolution: bundler`, `jsx: react-jsx`) |

Tests are self-contained: `ChatPane.test.tsx` declares `@vitest-environment
jsdom` and imports jest-dom itself, so no setup file is needed. The reducer
tests read the synthetic protocol examples from `app/src/features/chat/__tests__/testdata/*.jsonl`
(path relative to the test file). Regenerate them from the repository root with
`python3 scripts/generate-synthetic-chat.py`.

## Wiring (5 lines)

```tsx
import { ChatPane, WsChatTransport } from './features/chat'
const transport = new WsChatTransport({ baseUrl: cmdrdUrl /* '' = same origin */, token })
<ChatPane role="commander" title="Commander" transport={transport}
  onPopOut={() => host.popOutToTerminal('commander')}
  readonly={ownedByTerminal ? 'Owned by a terminal' : undefined} />
```

One transport can serve every pane (it is stateless per role). The pane needs a
parent with a definite height (it is `height: 100%`, flex column). CSS is
imported by `ChatPane.tsx` and scoped under `.cmdr-chat`; every color is a
`--cc-*` custom property (dark Catppuccin Mocha default, `data-theme="light"`
on `.cmdr-chat` switches to Latte).

## Host contract (cmdrd `internal/chat`)

Auth: `Authorization: Bearer <token>` on POSTs. The WebSocket carries the token
as `?token=` because browsers cannot set WS headers (cmdrd's `auth` already
accepts it).

| call | request | notes |
|---|---|---|
| `GET /api/chat/{role}/events?since=<seq>&token=` (WS) | frames: one event object, or an array of them | replay everything with `seq > since`, then live. Reconnects with backoff and the last applied seq. |
| `POST /api/chat/{role}/send` | `{text, clientId, images?: [{name, mediaType, dataUrl}], now?: bool}` | echo it back as a `user_message` carrying the same `clientId` (replaces the optimistic bubble). `now: true` = interrupt the running turn and send immediately; otherwise queue it as the next turn. |
| `POST /api/chat/{role}/interrupt` | `{}` | control_request `interrupt`. |
| `POST /api/chat/{role}/approve` | `{id, behavior: 'allow'\|'deny', updatedInput?, updatedPermissions?, message?}` | answers a `can_use_tool` control_request (permission or ExitPlanMode). `updatedPermissions` = the chosen `permission_suggestions` for "Always allow". For a plan, deny + `message` = "keep planning" feedback. Emit `request_resolved` afterwards so other viewers update. |
| `POST /api/chat/{role}/answer` | `{id, answers: {[questionText]: answer}}` | AskUserQuestion: respond allow with `updatedInput: {...input, answers}`. Multi-select answers are joined with `", "`. |

Non-2xx responses may return `{error}`; the pane shows it in an error bar.

## Event model

`events.ts` defines `ChatEvent` (every event has `seq`, `session`, optional
`ts` epoch ms and `parent` = parent tool_use id for sub-agent events) and a pure
reducer (`reduce`, `reduceAll`, `deriveStatus`). Kinds: `init`,
`user_message` (with `origin` mail/hook/system; `[mail]`-prefixed text is
also detected), `text_delta`/`text_final` (keyed by message id + block index),
`thinking`, `tool_start`/`tool_input_delta`/`tool_input`/`tool_result`
(with Claude's structured `tool_use_result` as `structured`),
`permission_request`, `question`, `plan`, `request_resolved`, `status`,
`task`, `compact`, `context`, `rate_limit`, `turn_result` (cumulative
`costUsd`; the reducer takes deltas), `notice`, `interrupted`, `error`, `exit`.

`adapter.ts` maps raw Claude stream-json (driver recording lines or bare
events) into this model; it is what the tests run the synthetic protocol examples through.

**cmdrd's wire format is mapped by `engine.ts` (`EngineMapper`).** cmdrd sends
`chat.Event` frames (`session`, `text`, `tool{...}`, `request{...}`,
`turn_result{result}`, `agent_state`, `replay_done`...), which the transport
runs through one `EngineMapper` per subscription (it remembers the tool id of
each `message_id`+`block`, since `tool_input_delta` frames carry none).
Transcript history has no turn results: transcript user messages are never
"queued", and the engine's spawn-time `session` event (`init` with `fresh`)
closes whatever turn the history left open. `send`/`approve` bodies use
cmdrd's names (`decision`, `updated_input`, `updated_permissions`).

Earlier note: the engine (internal/chat) was built in
parallel against the same spike data. `fromEngine(frame)` in `adapter.ts` is
the single place to adapt: it passes normalized frames through (accepting
`kind` or `type`, `session` or `session_id`, `ts` or ISO `time`) and also
accepts `{seq, session, raw: <stream-json line>}` frames, which it runs through
`RawAdapter`. If cmdrd emits different names, map them there.

Not observed in the spike recordings, mapped from Claude Code/T3 Code docs and
code: `task_started`/`task_progress`/`task_notification`, AskUserQuestion and
ExitPlanMode via `can_use_tool`, `slash_commands` in `init` (the recorded
`init` only has `n_slash`, so the composer falls back to a built-in list of
commands that work in `-p` mode).

## Dev gallery

`ChatGallery` renders a fixture (`fixtures.ts`: `longAnswer`, `toolHeavy`,
`permissionPrompt`, `subAgent`, `questionAndPlan`, `planOnly`) over a
`ReplayTransport`; pick one with `#<name>`. Generate screenshots locally from a Vite build of the gallery
with chrome-headless-shell; do not commit workspace captures.
