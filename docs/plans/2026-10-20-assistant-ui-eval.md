# assistant-ui for the app's chat: evaluation

Date: 2026-10-20. Question: should the native chat (`app/src/features/chat`)
move onto [assistant-ui](https://github.com/assistant-ui/assistant-ui)'s
`ExternalStoreRuntime`, or stay custom?

**Recommendation: keep our chat.** Borrow ideas, not the runtime. Revisit only
if we need message editing/branching or rich attachments, the two things
assistant-ui would give us for free.

## What we have

cmdrd streams `chat.Event` frames per role (`/api/chat/{role}/events?since=N`,
replayable from 0). `engine.ts` maps them, `events.ts` folds them into a
timeline of items, and the pane renders them in a virtual list. It already
covers: streaming text and thinking, collapsible tool cards with diffs,
sub-agent cards with nested child calls, approvals with Allow once / Always
(permission suggestions) / Deny, AskUserQuestion forms (single, multi, Other),
ExitPlanMode approve/keep planning, interrupt and send-now, queued follow-ups,
slash-command autocomplete, image paste, mail rows, compaction markers, rate
limit and context meters, real hydrated history, 10k-event replays, and now
terminal sessions whose sends are delivered by mail. About 140 feature tests plus
the app-level attach tests.

## How assistant-ui would fit

`useExternalStoreRuntime({ messages, isRunning, onNew, onCancel,
convertMessage, adapters })` lets us keep our store and hand assistant-ui a
message list. Feature by feature:

| Need | Fit | Notes |
| --- | --- | --- |
| Thread, streaming text | good | `convertMessage` from our items to `ThreadMessageLike` text parts |
| Thinking | good | reasoning parts |
| Tool calls | partial | tool-call parts + `makeAssistantToolUI`; our cards (diffs, Bash exit, durations) would be rewritten as tool UIs, same work as today |
| Sub-agents (nested calls) | poor | no parent/child tool nesting; we would flatten or keep a custom card |
| Approvals, questions, plan | partial | human-in-the-loop tool UIs can render them, but the decision goes to cmdrd's `/approve` `/answer`, not a tool result; still custom forms |
| System rows (mail, compaction, interrupted, delivered by mail) | poor | no role for them; custom message types or out-of-band rows |
| Composer | good | primitives for send/cancel; but our queue, send-now (Mod+Enter), slash list and per-role drafts are custom |
| Attachments | good | the adapter is nicer than our paste-only images (cmdrd does not forward images yet anyway) |
| Long histories | worse | we virtualize; a 10k-event replay renders every message there |
| Styling | churn | its styled layer assumes Tailwind/shadcn; we use CSS variables shared with the rest of the app |

## Effort vs value

About 1.5 to 2 weeks to reach parity, most of it re-creating what we have as
tool UIs and custom message renderers, plus the risk of regressing replay
performance and the approval flows the user relies on. What we would gain is
mostly composer polish, attachments and edit/branch, none of which the user has
asked for, and cmdrd's protocol does not support editing or branching a
Claude session today.

## Decision

Keep the custom chat. It is already shaped by cmdrd's event model, it is
tested, and it renders real history. Ideas worth borrowing when needed: the
attachment adapter shape, a message action bar (copy/retry), and a jump-to
latest affordance. Same call for Tailwind + shadcn: the app's shared CSS
tokens (`styles.css`, Catppuccin Mocha/Latte via `light-dark()`) already give
every surface one look; a framework migration would be churn without a payoff.
