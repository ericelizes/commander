// Opt-in Electron smoke for the inspector (npm run smoke:electron). Hidden
// window only: dock hidden before ready, show:false, never focused, hard exit
// after 45s. It serves the built preview page plus a fake cmdrd with two web
// tabs (a local http page and a data: URL), switches tabs, checks the native
// view sits exactly on the pane body, and writes a composite PNG.
//
// env: SMOKE_PAGE_DIR (built preview), SMOKE_OUT (png path), SMOKE_PRELOAD.

import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { readFile, writeFile } from 'node:fs/promises'
import { extname, join } from 'node:path'
import type { AddressInfo } from 'node:net'

import { app, BrowserWindow } from 'electron'

import { registerInspectorIpc } from '../ipc'

app.dock?.hide()
setTimeout(() => {
  console.error('smoke: timeout')
  app.exit(2)
}, 45_000).unref()

const pageDir = process.env.SMOKE_PAGE_DIR!
const out = process.env.SMOKE_OUT!
const preload = process.env.SMOKE_PRELOAD!

const localPage = `<!doctype html><html><body style="margin:0;font:16px system-ui;background:#fff;color:#111">
<div style="padding:24px"><h1>Local page</h1><p>Served by the smoke test.</p></div></body></html>`
const dataUrl = 'data:text/html,' + encodeURIComponent('<body style="margin:0;background:#e8f0ff;font:16px system-ui"><h1 style="padding:24px">data: tab</h1></body>')

function listen(handler: (req: IncomingMessage, res: ServerResponse) => void): Promise<string> {
  return new Promise((resolve) => {
    const srv = createServer(handler)
    srv.listen(0, '127.0.0.1', () => resolve(`http://127.0.0.1:${(srv.address() as AddressInfo).port}`))
  })
}

const types: Record<string, string> = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' }

async function main() {
  await app.whenReady()

  const pageBase = await listen(async (req, res) => {
    const path = new URL(req.url!, 'http://x').pathname
    if (path === '/local') {
      res.setHeader('Content-Type', 'text/html')
      res.end(localPage)
      return
    }
    try {
      const file = join(pageDir, path === '/' ? 'index.html' : path)
      const body = await readFile(file)
      res.setHeader('Content-Type', types[extname(file)] ?? 'application/octet-stream')
      res.end(body)
    } catch {
      res.statusCode = 404
      res.end()
    }
  })

  let active = 1
  const tabs = () => [
    { id: 1, position: 1, title: 'Local', kind: 'url', target: pageBase + '/local', renderer: '', detected: 'link', view: 'web', refresh_secs: 60, added_by: 'user', created_at: 0 },
    { id: 2, position: 2, title: 'Data', kind: 'url', target: dataUrl, renderer: '', detected: 'link', view: 'web', refresh_secs: 60, added_by: 'user', created_at: 0 },
  ]
  const apiBase = await listen((req, res) => {
    res.setHeader('Access-Control-Allow-Origin', '*')
    res.setHeader('Access-Control-Allow-Headers', 'Authorization, Content-Type')
    res.setHeader('Content-Type', 'application/json')
    if (req.method === 'OPTIONS') return res.end()
    const path = new URL(req.url!, 'http://x').pathname
    if (path === '/api/tabs') return res.end(JSON.stringify({ project_id: 0, ws: '', active_id: active, tabs: tabs() }))
    if (path === '/api/cmd') {
      let body = ''
      req.on('data', (c) => (body += c))
      req.on('end', () => {
        const argv: string[] = JSON.parse(body).argv
        if (argv[1] === 'focus') active = Number(argv[2].slice(1))
        res.end(JSON.stringify({ ok: true, stdout: '', stderr: '', exit: 0 }))
      })
      return
    }
    res.statusCode = 404
    res.end('{}')
  })

  const win = new BrowserWindow({
    width: 1400,
    height: 820,
    show: false,
    focusable: false,
    paintWhenInitiallyHidden: true,
    webPreferences: { preload, contextIsolation: true, sandbox: false, nodeIntegration: false },
  })
  const handle = registerInspectorIpc(win, { dev: false, openExternal: () => {} })
  await win.loadURL(`${pageBase}/index.html?bridge=host&api=${encodeURIComponent(apiBase)}&token=t`)

  const until = async (what: string, ok: () => boolean | Promise<boolean>) => {
    for (let i = 0; i < 100; i++) {
      if (await ok()) return
      await new Promise((r) => setTimeout(r, 100))
    }
    throw new Error('smoke: timed out waiting for ' + what)
  }
  const bodyRect = () =>
    win.webContents.executeJavaScript(
      `(() => { const r = document.querySelector('.insp-body').getBoundingClientRect(); return { x: Math.ceil(r.left), y: Math.ceil(r.top), width: Math.floor(r.right) - Math.ceil(r.left), height: Math.floor(r.bottom) - Math.ceil(r.top) } })()`,
    )
  const assertPlaced = async (id: number) => {
    const want = await bodyRect()
    await until(`view ${id} at the pane body`, () => {
      const v = handle.views.view(id)
      if (!v || !v.getVisible()) return false
      const b = v.getBounds()
      return b.x === want.x && b.y === want.y && b.width === want.width && b.height === want.height
    })
    console.log(`view ${id} bounds`, handle.views.view(id)!.getBounds(), 'body', want)
  }

  await until('view 1', () => handle.views.ids().includes(1))
  await assertPlaced(1)
  if (handle.views.ids().includes(2)) throw new Error('view 2 created before it was shown (not lazy)')

  await win.webContents.executeJavaScript(`[...document.querySelectorAll('.insp-tab')].find(e => e.textContent.includes('Data')).click()`)
  await until('view 2', () => handle.views.ids().includes(2))
  await assertPlaced(2)
  if (handle.views.view(1)!.getVisible()) throw new Error('view 1 still visible after switching')
  if (!handle.views.ids().includes(1)) throw new Error('view 1 destroyed on switch (should stay alive)')
  await until('data page painted', async () => !(await handle.views.view(2)!.webContents.isLoading()))

  // Composite: the host page plus the native view drawn at its real bounds.
  const host = await win.webContents.capturePage(undefined, { stayHidden: true })
  const view = handle.views.view(2)!
  const shot = await view.webContents.capturePage(undefined, { stayHidden: true })
  const b = view.getBounds()
  const comp = new BrowserWindow({ width: host.getSize().width, height: host.getSize().height, show: false, focusable: false })
  await comp.loadURL(
    'data:text/html,' +
      encodeURIComponent(`<body style="margin:0"><canvas id=c></canvas><script>
      const c = document.getElementById('c'); c.width = ${host.getSize().width}; c.height = ${host.getSize().height};
      const g = c.getContext('2d'); const a = new Image(), v = new Image(); let n = 0;
      const done = () => { if (++n < 2) return; g.drawImage(a, 0, 0, c.width, c.height); g.drawImage(v, ${b.x}, ${b.y}, ${b.width}, ${b.height}); document.title = 'ready' };
      a.onload = v.onload = done; a.src = '${host.toDataURL()}'; v.src = '${shot.toDataURL()}';
      </script></body>`),
  )
  await until('composite', () => comp.webContents.getTitle() === 'ready')
  const png = await comp.webContents.capturePage(undefined, { stayHidden: true })
  await writeFile(out, png.toPNG())
  console.log('smoke: ok, wrote', out)
  app.exit(0)
}

main().catch((e) => {
  console.error(e)
  app.exit(1)
})
