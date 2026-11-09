# Desktop app packaging and shell: integration steps

> Applied (2026-10-20). The app uses electron-vite, not vite-plugin-electron:
> main/preload build to `out/main` and `out/preload` (`main: out/main/index.js`),
> the renderer to `dist/`; `electron-builder.yml` packs those. Wiring lives in
> `src/main/index.ts` and `src/preload/index.ts`. See CLAUDE.md, Desktop app.

This unit adds packaging, install commands, the dev loop and the main-process
"shell" behaviors (single instance, window state, menu, shortcuts, dock,
cmdrd health). It does not touch `package.json`, the lockfile, vite or
tsconfig, `electron/main.ts`, `electron/preload.ts` or `cmd/cmdr/app.go`.
Apply the steps below when merging with the app skeleton.

## Files in this unit

| Path | What |
|---|---|
| `app/build/electron-builder.yml` | builder config (mac arm64 dmg/zip/dir; linux AppImage+deb x64/arm64) |
| `app/build/icon.png`, `icon.icns`, `icons/NxN.png` | generated app icons (committed) |
| `app/build/scripts/gen-icon.swift`, `make-icons.sh` | icon generator (macOS: swift, sips, iconutil) |
| `app/electron/shortcuts.ts` | key event -> action (pure), `attachShortcuts` via `before-input-event` |
| `app/electron/menu.ts` | `buildMenuTemplate({platform,isDev,appName,dispatch})` |
| `app/electron/dock.ts` | NEEDS YOU badge, dock menu (first 5 workstreams); guarded per OS |
| `app/electron/window-state.ts` | saved bounds + maximized, off-screen safe |
| `app/electron/cmdrd.ts` | probe api-url, kick launchctl / systemctl --user / cmdrd, watch |
| `app/electron/app-shell.ts` | `startAppShell(electron, {isDev, createWindow})` wires all of the above |
| `app/electron/shell-preload.ts` | `shellApi(ipcRenderer)` for the preload's contextBridge |
| `app/electron/shell-channels.ts` | IPC channel names |
| `app/electron/*.test.ts` | vitest (36 tests) |
| `cmd/cmdr/app_install.go` (+test) | `cmdr app install|uninstall|status|open` |
| `scripts/app-install.sh`, `scripts/app-dev.sh` | build+install, dev loop |

## 1. package.json (app/package.json)

Fields electron-builder needs (the rest of its config is in the yml):

```jsonc
{
  "name": "commander-app",
  "version": "0.1.0",
  "description": "Desktop cockpit for Commander",
  "author": "Commander contributors",
  "main": "dist-electron/main.js",
  "scripts": {
    "dev": "vite",
    "dev:web": "COMMANDER_WEB=1 vite --host 127.0.0.1 --port 5173 --strictPort",
    "build": "tsc --noEmit && vite build",
    "test": "vitest run",
    "pack": "npm run build && electron-builder --config build/electron-builder.yml --dir",
    "dist": "npm run build && electron-builder --config build/electron-builder.yml",
    "install-app": "../scripts/app-install.sh"
  }
}
```

devDependencies (exact; all published more than two weeks before 2026-10-20,
and the first three are what this unit was tested with):

```json
"electron": "44.5.1",
"electron-builder": "26.15.7",
"vitest": "5.0.3",
"vite-plugin-electron": "1.1.2",
"@types/node": "22.20.5"
```

If the skeleton already pins a different Electron, keep theirs. Nothing here
depends on 44.x APIs.

`cmdr app install` runs `npm run dist -- --mac dir --arm64` (macOS) or
`npm run dist -- --linux AppImage --x64|--arm64` (Linux). npm appends those to
the end of the `dist` script, i.e. to the `electron-builder` command, so keep
`electron-builder` last in `dist`.

npm here blocks install scripts by default: `npm install` did not run
electron's `install.js`, so `node_modules/electron/dist` was missing. Packing
is unaffected (electron-builder downloads its own Electron), but `npm run dev`
needs the binary: run `npm approve-scripts electron` once (or
`node node_modules/electron/install.js`).

## 2. vite config

Use `vite-plugin-electron/simple` so main and preload build to
`dist-electron/` (matches the yml's `files`) and Electron restarts when main
or preload changes. The renderer builds to `dist/`. Skip the plugin for
`dev:web` (renderer only, for a headless VM or a browser):

```ts
import electron from 'vite-plugin-electron/simple'
const web = process.env.COMMANDER_WEB === '1'
export default defineConfig({
  plugins: [
    react(),
    ...(web ? [] : [electron({
      main: { entry: 'electron/main.ts' },
      preload: { input: 'electron/preload.ts' },
    })]),
  ],
})
```

Add `**/*.test.ts` under `electron/` to vitest's include (the default already
matches) and to tsconfig's include for the main/preload project.

## 3. electron/main.ts

Let the shell own app lifecycle; main only builds the window:

```ts
import * as electron from 'electron'
import { BrowserWindow, app } from 'electron'
import path from 'node:path'
import { startAppShell } from './app-shell'

startAppShell(electron, {
  isDev: !app.isPackaged,
  createWindow: (state) => {
    const win = new BrowserWindow({
      ...state, // x, y, width, height (x/y omitted -> centered)
      minWidth: 800,
      minHeight: 500,
      backgroundColor: '#1e1e2e',
      title: 'Commander',
      webPreferences: { preload: path.join(__dirname, 'preload.js') },
    })
    if (process.env.VITE_DEV_SERVER_URL) win.loadURL(process.env.VITE_DEV_SERVER_URL)
    else win.loadFile(path.join(__dirname, '../dist/index.html'))
    return win
  },
})
```

Do not also call `requestSingleInstanceLock`, `Menu.setApplicationMenu`, or
register `window-all-closed`/`activate`; the shell does. (The `maximized`
field in `state` is applied by the shell; spreading it into the
BrowserWindow options is harmless.)

What the shell does:
* Single instance: a second launch quits and focuses (restores) the first window.
* Window bounds/maximized saved to `<userData>/window-state.json`, dropped if off-screen.
* Menu: macOS App/Edit/View/Window/Help; Linux File/Edit/View/Window/Help with
  no macOS-only roles (about, services, hide*, front, zoom, help role).
* Shortcuts in `before-input-event` (work with a terminal/webview focused);
  menu items show them with `registerAccelerator: false` so nothing fires twice:

  | Action | macOS | Linux | IPC to renderer |
  |---|---|---|---|
  | Commander / workstream n | Cmd+0..9 (Ctrl+0..9 too) | Ctrl+0..9 | `nav:expand-or-go` (n) |
  | Quick switcher | Cmd+K | Ctrl+K | `nav:quick-switcher` |
  | Next / prev workstream | Cmd+Shift+] / [ | Ctrl+Shift+] / [ | `nav:next-workstream` / `nav:prev-workstream` |
  | Settings | Cmd+, | Ctrl+, | `nav:settings` |
  | Reload renderer (dev) | Cmd+R | Ctrl+Shift+R | handled in main |
  | DevTools (dev) | Cmd+Alt+I | Ctrl+Shift+I | handled in main |

  Linux keeps Ctrl+R for reverse-search in a terminal view; Ctrl+K is taken
  by the quick switcher (kill-line is lost in an embedded terminal).
* cmdrd: on launch probes `<COMMANDER_HOME>/api-url`; if down sends
  `{status:'starting', message:'offline: starting cmdrd…'}`, runs
  `launchctl kickstart gui/<uid>/org.commander` (macOS) or
  `systemctl --user start commander.service` (Linux), falling back to
  spawning `~/.local/bin/cmdrd`, then polls 15 s. It keeps probing every 5 s
  and repeats when cmdrd drops. States go out as `daemon:state`.
* Dock (macOS only): badge = NEEDS YOU count, dock menu = first 5 workstreams
  (click focuses and sends `nav:open-workstream` (id)). Linux: badge via
  `app.setBadgeCount` (Unity/KDE launchers), no dock menu; no `app.dock` use.

## 4. electron/preload.ts

```ts
import { contextBridge, ipcRenderer } from 'electron'
import { shellApi } from './shell-preload'
contextBridge.exposeInMainWorld('commanderShell', shellApi(ipcRenderer))
```

Renderer type: `declare global { interface Window { commanderShell?: import('../electron/shell-preload').ShellApi } }`
(optional so the same renderer runs in web mode without Electron).

Renderer obligations:
* `commanderShell.onNav(ev => ...)` for the nav events above.
* `commanderShell.getDaemonState()` on mount plus `onDaemonState(cb)`; show an
  offline banner for `starting`/`offline` and reconnect the WebSocket on `online`.
* Call `setNeedsYou(count)` whenever the NEEDS YOU count changes and
  `setDockWorkstreams([{id,name}])` when the workstream list changes.

## 5. cmd/cmdr/app.go: one-line registration

At the top of the `cmdr app` handler, before the dev-launch logic:

```go
if len(args) > 0 && isAppInstallSubcommand(args[0]) { return runAppInstallCmd(args, stdout, stderr) }
```

`args` is what follows `app` (so `args[0]` is `install|uninstall|status|open`).
`runAppInstallCmd` takes `io.Writer`s, so `*os.File` works. Until this line
exists, `scripts/app-install.sh` (which runs `cmdr app install`) has nothing
to call.

Commands:
* `cmdr app install [--skip-build] [--from <path>]`: `npm ci`, `npm run dist -- …`,
  then macOS: back up the old `/Applications/Commander.app` to
  `~/.commander/app-backup/`, `ditto` the new one in, `lsregister -f` it.
  Linux: `~/Applications/Commander.AppImage` (0755), symlink
  `~/.local/bin/commander-app` (`commander` is already cmdr's alias), icon to
  `~/.local/share/icons/hicolor/512x512/apps/commander.png`, `.desktop` to
  `~/.local/share/applications/commander.desktop`, `update-desktop-database`.
  Writes `~/.commander/app-install.json` (path, version).
* `cmdr app uninstall`: removes the app (and the Linux link/icon/desktop
  entry); keeps the backup.
* `cmdr app status`: api-url, daemon up/down, app path and version (Info.plist
  on macOS, the install record on Linux), and "display: none" on headless Linux.
* `cmdr app open [--web]`: ensures cmdrd (kick unit, fall back to `cmdrd`,
  wait 10 s), then `open /Applications/Commander.app` or exec the AppImage.
  With `--web`, or Linux without `DISPLAY`/`WAYLAND_DISPLAY`, it prints
  `<api-url>/app/?token=<api-token>` instead of launching Electron.
* `CMDR_APP_INSTALL_DIR` overrides the install dir; `COMMANDER_HOME` the
  state dir. Use both for dry runs.

**Dependency on cmdrd:** web mode needs cmdrd to serve the renderer build at
`/app/` and accept `?token=` (the API side owns that). Until then the
printed URL 404s.

## 6. Dev loop

`scripts/app-dev.sh`:
* ensures cmdrd answers (same kick/fallback as above; `--check` stops there),
* then `npm run dev`: Vite serves the renderer with HMR (UI edits apply with no
  restart); vite-plugin-electron rebuilds main/preload and restarts Electron
  on change. The renderer must keep its state across reloads (store outside
  React components, `import.meta.hot` where needed) and reconnect the cmdrd
  WebSocket with backoff; an Electron restart re-runs the daemon check.
* `--web`, or Linux without a display: `npm run dev:web` (renderer only on
  127.0.0.1:5173; `ssh -L 5173:127.0.0.1:5173` from the laptop). The
  renderer must read `?token=` and talk to cmdrd's URL (dev proxy or CORS) in
  this mode.

## 7. Building, verified and not

Verified on this Mac (throwaway scaffold, nothing launched):
* `electron-builder --config build/electron-builder.yml --mac dir --arm64`:
  `Commander.app` with `CFBundleIdentifier org.commander`,
  `LSUIElement false`, developer-tools category, `icon.icns`, asar with
  `dist/`, `dist-electron/`, `package.json`, ad-hoc signature.
* `--linux deb --arm64`: deb with Section devel, Homepage, Maintainer,
  `/opt/Commander/commander-app`, desktop entry (Categories=Development;
  StartupWMClass=Commander) and the full hicolor icon set.
* `--linux dir` for x64 and arm64.

Not verified here:
* AppImage cannot be built on this Apple-silicon Mac: electron-builder's
  x86_64 `mksquashfs` fails with EBADARCH (-86) without Rosetta. Build it on
  the Linux VM (`npm run dist -- --linux`) or install Rosetta.
* dmg/zip (same pipeline as dir; not run to save time).
* Running the packaged app (no launches allowed on this machine).

Linux runtime notes:
* AppImage needs FUSE 2: `sudo apt install libfuse2` (Ubuntu 22.04+:
  `libfuse2t64` on 24.04).
* Ubuntu 24.04 restricts unprivileged user namespaces; if the AppImage dies
  with a sandbox error, install the deb instead (chrome-sandbox is set up
  setuid there) or allow it with an AppArmor profile.

## 8. Suggested follow-ups (not done, to avoid collisions)

* CLAUDE.md: a short "Desktop app" section (`cmdr app` commands, the no-launch
  rule for tests, `CMDR_APP_INSTALL_DIR`/`COMMANDER_HOME` for dry runs, the
  AppImage-on-Mac limit).
* justfile: `app-install: scripts/app-install.sh`, `app-dev: scripts/app-dev.sh`.
