# Inspector: integration

The right-hand inspector: tab strip, real logged-in webviews (Electron) and
text cards. Renderer code is here; main-process code is in
`app/electron/inspector/`; the API is cmdrd's `GET /api/tabs`,
`GET /api/tabs/card` and the `tab` verbs of `POST /api/cmd` (`internal/api/tabs.go`).

## npm deps (exact versions it was built and tested with)

| package | version | kind |
|---|---|---|
| react, react-dom | 19.3.1 | dep |
| react-markdown | 10.1.0 | dep (card markdown; no raw HTML) |
| remark-gfm | 4.0.1 | dep (tables, task lists) |
| electron | 44.9.2 | dev |
| vite | 8.0.0 | dev |
| @vitejs/plugin-react | 6.1.2 | dev |
| vitest | 5.0.5 | dev |
| jsdom | 30.1.3 | dev (InspectorPane.test.tsx) |
| @testing-library/react | 16.3.3 | dev |
| esbuild | 0.27.7 | dev (smoke build only; vite 8 wants ^0.27) |
| typescript | 6.0.3, @types/react(-dom) 19.3.0, @types/node 24 | dev |

tsconfig needs `"jsx": "react-jsx"` and `vite/client` types (the pane imports
`./inspector.css`). vitest must include `src/**/*.test.{ts,tsx}` and
`electron/**/*.test.ts`.

## Host contract

Renderer: `<InspectorPane api={{ baseUrl, token }} window={{ ws, project }} />`

* `api`: cmdrd base URL (`""` = same origin, web mode at `/app/`) and the
  token from `$COMMANDER_HOME/api-token`.
* `window`: the cockpit window; omit (or `{}`) for Commander, `{ ws: bead, project }`
  for a workstream.
* optional `renderDecision(tab)`: the decision-card feature draws `kind=decision` tabs.
* `bridge`: defaults to `window.commanderHost?.webview`. **Absent = web mode**
  (plain page / Linux VM over SSH): web tabs become a link card with
  "Open in browser" plus the digest card for Notion/Linear/GitHub PR.
* The pane fills its parent (`height: 100%`); give it a sized container.

Preload must expose the bridge (only Electron has it):

```ts
import { contextBridge } from 'electron'
import { createInspectorWebviewBridge } from '../electron/inspector/preload'
contextBridge.exposeInMainWorld('commanderHost', { ...yourHostApi, webview: createInspectorWebviewBridge() })
```

Main: `registerInspectorIpc(mainWindow, { dev })` from `app/electron/inspector`.
It owns these IPC channels (do not reuse the names):

| channel | dir | payload |
|---|---|---|
| `inspector:sync` | renderer→main send | `{ tabs: {id,url}[], activeId, rect }`: declarative; views not listed are destroyed, `rect: null` hides all |
| `inspector:nav` | send | `{ id, action: back/forward/reload/stop/load/devtools, url? }` |
| `inspector:open-external` | send | url (http/https/mailto only) |
| `inspector:signin` | invoke | `{ notion, linear, github, datadog, slack }: boolean` from partition cookies |
| `inspector:state` | main→renderer | `{ id, url, title, loading, error, canGoBack, canGoForward }` |
| `inspector:shortcut` | main→renderer | `{ id, action: 'focus-url' }` (Cmd/Ctrl+L inside a webview) |

All webviews use the partition `persist:cockpit` (sign-ins survive restarts),
no preload, `contextIsolation`, `sandbox`, no node, permissions denied by
default (only `clipboard-sanitized-write`, `fullscreen`), a plain Chrome UA.
Navigation stays on the tab's site and sign-in hosts; other links and
`target=_blank` go to the default browser; OAuth popups open as child windows
on the same partition. Cross-platform (macOS/Linux/Windows); the modifier key
is Cmd on macOS, Ctrl elsewhere.

Rect math: the pane sends the body rect in CSS px; main multiplies by the host
page's zoom factor and clamps to the window. Menus hide the native view while
open (it would cover them). If the host draws anything over the inspector
body, it must make the pane re-render with a menu open, or unmount it.

## Wiring (5 lines)

```ts
// main.ts, after creating mainWindow:
registerInspectorIpc(mainWindow, { dev: !app.isPackaged })
// preload.ts:
contextBridge.exposeInMainWorld('commanderHost', { ...host, webview: createInspectorWebviewBridge() })
// right column of the layout:
<InspectorPane api={{ baseUrl, token }} window={{ ws: current?.bead_id, project: current?.project }} />
```

## Tests

* `npx vitest run src/features/inspector electron/inspector`: tab model, rect
  math, ANSI, navigation/popup/permission policy, sign-in cookie detection,
  view reconciliation (lazy create, keep hidden, destroy removed), pane
  behaviour in Electron and web mode.
* `npm run smoke:electron` → `node electron/inspector/smoke/run.mjs [out.png]`
  (opt-in, launches hidden Electron): two web tabs (local http page + data:
  URL), switches, asserts each native view's bounds equal the pane body and
  that the first view stays alive hidden, writes a composite PNG.
* Visual check without Electron: `vite build src/features/inspector/preview`,
  serve it, and screenshot `index.html?mode=electron` / `?mode=web` with
  chrome-headless-shell; keep generated captures local.
