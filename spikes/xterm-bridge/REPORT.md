# Spike A: one tmux pane in xterm.js (2026-10-20)

Question: can xterm.js (browser/Electron) show exactly one tmux pane (the chat,
`@cmdr_chat=1`), interactively and robustly? Code, fake cockpit, tests and evidence
are in this directory (Node only; private tmux server; the live `commander` session
was never touched). Reproduce: `npm install; sh fixture/up.sh; node server.js &`
then `node test/features.js | sizes.js | hooks.js | control.js | browser.js`.

## Short answer

* **Fidelity: yes.** A grouped-session pty bridge (node-pty 1.1.0 works on this Mac)
  gives input, keys, paste, 24-bit colour, unicode, wheel, reconnect-without-killing
  the chat, at 0.33 ms p50 (p95 0.57) through ws+pty+tmux, 0.7-4.6 ms in headless
  Chrome.
* **Sizing: not as the plan says.** `window-size latest` does NOT prevent the size
  fight: grouped sessions share the window, so a window has one size and one zoom
  state. If the TUI and the app both view the chat's window they fight (latest flips
  200x49 -> 100x30 -> ...; smallest pins 80x24; largest crops the app), zoom applies
  to the TUI too, and cmdr's `__relayout` (resize-pane on client-attached /
  client-resized) UNZOOMS the window. The bridge is only safe when the app is the sole
  viewer of that window.

## Three fixes the plan lacked (all implemented in server.js / index.html)

1. Key isolation: the app's session inherits global bindings (prefix C-a, cmdr's
   `-n C-g`, S-arrow window switching eat keys). Per app session: `prefix None`,
   `prefix2 None`, own key-table, `mouse on`, copy the root table's `*Pane` mouse
   bindings (else the wheel stops scrolling history).
2. Reply shim for terminal queries: tmux 3.7 queries XTVERSION (`CSI >q`) and
   colour scheme (`CSI ?996n`); xterm.js doesn't answer, and tmux then holds every lone
   Esc ~500 ms (Esc is Claude's interrupt). Two `registerCsiHandler` replies fix it
   (measured 501-504 ms -> 2 ms).
3. The page must forward xterm.js's own `onData` replies to tmux, not just keystrokes.

## Other findings

* Scrollback lives in tmux (xterm.js on the alternate screen has 0 rows): the wheel
  enters copy-mode; typing goes to copy-mode while scrolled; selection is tmux's.
  How this feels on long real Claude transcripts is the main UX unknown.
* Shift+Enter in CSI-u arrives as `\r` unless extended-keys is enabled.
* `app-*` sessions persist after disconnect (needed for reconnect): reap them.
* No auth in the spike (the plan's loopback + token applies).
* A solo window works: `break-pane` kept the same pid and sized the chat to the app
  (90x28) while the TUI panes stayed 133x50/66x50; `join-pane` restores it with the tag.
  cmdr's layout code assumes the chat lives in its window.

## Control mode (`tmux -C`) probe

Unescapes `%output`, seeds from `capture-pane -p -e`, sends `send-keys -H`: p50
0.21 ms, lone Esc 0 ms, native xterm.js scrollback (455 rows), selection and search,
does not take part in window sizing, no zoom/grouped session, one connection for all
panes. Costs: pane keeps the layout's size, seeding on reconnect (no tmux format for
bracketed paste), flow control (`pause-after`), split UTF-8, layout bookkeeping.
Estimate 2-3 days to be robust vs ~1 day for the pty bridge.

## Recommendation

Grouped-session pty bridge for Phase 2, with: key isolation + mouse table copy; the
XTVERSION/996n shim; one pty per chat window shared by all tabs; the app as the ONLY
viewer of a chat window (either Relayout skips zoomed windows, or, cleaner, the chat
moves to an app-owned window via `break-pane` and back via `join-pane` for the TUI);
reap `app-*` sessions. Keep control mode as the upgrade if copy-mode scrollback is poor
with real Claude. Decide after the user uses the pty version for a day.

## Risks

1. Size/zoom coupling with the TUI (choose zoom vs solo window before Phase 2).
2. Scrollback UX through copy-mode with real Claude transcripts.
3. Key/terminal semantics through a second tmux client (bindings, query timing,
   extended keys, focus events, OSC 52/8); keep features.js and browser.js as a
   regression suite.

## Not verified

Real Claude Code (Esc, Shift+Enter, inline vs alternate screen, mouse/focus
reporting); Electron (node-pty under Electron's ABI, `before-input-event` with xterm.js
focused); Ghostty as the TUI client; the real cockpit's pad/title panes; clipboard,
hyperlinks, IME, flow control under burst output.
