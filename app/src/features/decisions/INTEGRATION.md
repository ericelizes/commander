# Decisions feature: integration

Decision cards as real forms, the NEEDS YOU queue, and a sandboxed host for
Manager-authored html tabs. Self-contained in `app/src/features/decisions/`;
it depends on the host only through `CommanderApi` (`types.ts`).

## npm deps (versions it was built and tested with)

Runtime: `react@19.3.1`, `react-dom@19.3.1` (any React 18.3+/19 works).

Dev/test: `vite@8.3.4`, `@vitejs/plugin-react@6.1.2`, `typescript@7.0.2`,
`vitest@5.0.5`, `jsdom@30.1.3`, `@types/jsdom@30.0.0`,
`@testing-library/react@16.3.3`, `@testing-library/user-event@14.6.8`,
`@testing-library/jest-dom@7.0.1`, `@types/react@19.3.0`, `@types/react-dom@19.3.0`.

Vitest config the tests expect: `environment: 'jsdom'`, a setup file with
`import '@testing-library/jest-dom/vitest'`, and Vite's `?raw` imports (the html
examples are loaded with `import x from './examples/x.html?raw'`). TS needs
`"types": ["vite/client", "@testing-library/jest-dom"]`, `"jsx": "react-jsx"`.
Components import `./decisions.css` themselves (Vite handles it).

## Host contract

```ts
interface CommanderApi {
  snapshot(): Promise<{ needs_you: NeedsYouItem[] }>  // GET /api/snapshot
  runCmd(argv: string[]): Promise<CmdResult>           // POST /api/cmd {argv}
  copy?(text: string): Promise<void>                   // default: navigator.clipboard
  openUrl?(url: string): void                          // default: window.open; Electron: shell.openExternal
}
```

* Commands sent: only `internal-resolve-needs --project P --bead B --action
  decide|approve|defer [--choice C] [--text T] [--resume-only]` (already on cmdrd's allow-list).
* Before every resolve the feature re-reads `snapshot()`. A missing flag runs
  `--resume-only` to finish any durable answer whose cleanup or mail failed.
  That command never creates a new answer and skips completed notifications.
  A failed submit stays retryable even if a later snapshot loses the flag.
* `needs_you[].since` (unix seconds) is new and additive in
  `internal/livedata` (commit "carry the ask's raise time"); it is optional here.
* Theme: Catppuccin Mocha fallbacks built in; the host can override
  `--ctp-base`, `--ctp-mantle`, `--ctp-mauve`, `--ctp-yellow`, `--font-sans`, ... .

## Exports (`index.ts`)

* `<DecisionCard needs api onDone? gone? autoFocus? />`: `onDone(outcome)`
  fires with `{kind:'answered', action, choice?, note?}` or `{kind:'gone'}`.
  Pass `gone` when a newer snapshot no longer lists the item.
* `<NeedsYouQueue items api onOpen now? autoFocus? />`, `<NeedsYouBadge count />`,
  `needsYouCount(snapshot)` for the sidebar badge.
* `<HtmlCardHost html title api allowScripts? needs? onDone? />`: sandboxed
  `srcdoc` iframe (`sandbox=""`, or `"allow-scripts"` when the tab asks; never
  same-origin/top-navigation/popups/forms) plus an injected CSP with no network.
  The page gets `window.cmdr.{submitDecision({choice,text}), copy(text),
  openUrl(url)}`; submitDecision only answers the `needs` flag passed in, and
  only with one of its option labels. http(s) link clicks route to `openUrl`.
  Without scripts a page can still navigate its own frame by a plain link
  (no sandbox flag blocks that); it cannot reach the network via fetch/XHR.

## Wiring (5 lines)

```tsx
import { DecisionCard, NeedsYouQueue, NeedsYouBadge, needsYouCount, HtmlCardHost } from './features/decisions'
const api = { snapshot: () => get('/api/snapshot'), runCmd: argv => post('/api/cmd', { argv }), openUrl: u => bridge.openExternal(u) }
<NeedsYouBadge count={needsYouCount(snap)} />                                   // sidebar
<NeedsYouQueue items={snap.needs_you} api={api} onOpen={item => openDecisionTab(item)} />
<DecisionCard needs={item} api={api} gone={!snap.needs_you.some(n => n.bead_id === item.bead_id && n.project === item.project)} onDone={() => {}} />
```

For an html tab: `<HtmlCardHost html={fileText} title={tab.title} api={api} allowScripts={tab.scripts} needs={flagForTab} />`.

## Keys

Card: `1`-`9` pick, arrows/`j` `k` move, Space select, `n`/`i` note, Esc leaves
the note, `c` copy all commands, Enter submit (Cmd/Ctrl+Enter from the note).
Queue: arrows/`j` `k`/Home/End move, Enter open, `a` approve and `d` defer
(plain flags only; cards must be opened).

## Screenshots

`DecisionsGallery` (`Gallery.tsx`) renders the fixtures (`view="cards"` or
`"queue"`). Build any page that mounts it, then shoot with
chrome-headless-shell (`--headless --hide-scrollbars --virtual-time-budget=2000
--screenshot=out.png --window-size=1720,770 <url>`). Keep generated captures local.
