import { spawn, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, describe, expect, it } from 'vitest'

// The real smoke in a real browser on a tiny page that reaches every other pass
// condition (conn=live, a workstream), so only the page's own errors can fail it.
// Skipped without a headless browser, unless COMMANDER_LAYOUT_CHECK_REQUIRED=1.
const app = join(__dirname, '..')
const haveBrowser = spawnSync(process.execPath, ['scripts/smoke.mjs', '--which-browser'], { cwd: app }).status === 0
const required = process.env.COMMANDER_LAYOUT_CHECK_REQUIRED === '1'
if (!haveBrowser && !required) console.warn('smoke-page-errors: skipped: no headless browser (set CHROME_HEADLESS_SHELL)')

const root = mkdtempSync(join(tmpdir(), 'cmdr-smoke-errors-'))
afterAll(() => rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 }))

let pages = 0
function tinyPage(script: string, head = '') {
  if (!haveBrowser) throw new Error('COMMANDER_LAYOUT_CHECK_REQUIRED is set and there is no headless browser')
  const dist = join(root, `page-${++pages}`)
  mkdirSync(dist)
  writeFileSync(join(dist, 'index.html'), `<!doctype html><html><head>${head}</head><body><div class="ws">w</div><script>document.documentElement.dataset.conn = 'live';${script}</script></body></html>`)
  return dist
}
function run(script: string, args: string[], env: NodeJS.ProcessEnv = {}) {
  const r = spawnSync(process.execPath, [script, ...args], { cwd: app, encoding: 'utf8', timeout: 90_000, env: { ...process.env, ...env } })
  return { code: r.status, out: `${r.stdout}${r.stderr}` }
}
function smoke(script: string, head = '', args = ['--fake']) {
  const dist = tinyPage(script, head)
  return run('scripts/smoke.mjs', [...args, '--dist', dist, '--out', join(dist, 'shot.png'), '--scale', '1'])
}

// A private home whose api-url is a stand-in for cmdrd: the given page at /app/, its settings route, 404 for all else.
// A stranger (cmdrd = false) answers 404 to everything. In its own process: spawnSync above holds this one's event loop.
async function withStubCmdrd<T>(dist: string, f: (home: string) => T, cmdrd = true): Promise<T> {
  const home = join(root, `home-${++pages}`)
  mkdirSync(home)
  const stub = join(home, 'stub.mjs')
  writeFileSync(
    stub,
    `import { createServer } from 'node:http'
import { readFileSync } from 'node:fs'
const s = createServer((req, res) => {
  const path = new URL(req.url, 'http://x').pathname
  if (${JSON.stringify(cmdrd)} && path === '/api/settings') return res.writeHead(200, { 'Content-Type': 'application/json' }).end('{"ok":true}')
  if (!${JSON.stringify(cmdrd)} || path !== '/app/') return res.writeHead(404).end()
  res.writeHead(200, { 'Content-Type': 'text/html' }).end(readFileSync(${JSON.stringify(join(dist, 'index.html'))}))
})
s.listen(0, '127.0.0.1', () => console.log(s.address().port))`,
  )
  const child = spawn(process.execPath, [stub], { stdio: ['ignore', 'pipe', 'inherit'] })
  try {
    const port = await new Promise<string>((ok) => child.stdout.once('data', (d) => ok(String(d).trim())))
    writeFileSync(join(home, 'api-token'), 'stub')
    writeFileSync(join(home, 'api-url'), `http://127.0.0.1:${port}`)
    return f(home)
  } finally {
    child.kill('SIGKILL')
  }
}

describe.skipIf(!haveBrowser && !required)('smoke fails on page errors', () => {
  it('passes a clean page', () => {
    const r = smoke(`console.log('fine'); console.warn('fine too')`)
    expect(r.code, r.out).toBe(0)
    expect(r.out).toContain('smoke: pass')
  }, 90_000)

  it.each([
    ['a thrown error', `throw new Error('page-boom')`],
    ['a throw after load', `setTimeout(() => { throw new Error('page-boom') }, 50)`],
    ['an unhandled rejection', `Promise.reject(new Error('page-boom'))`],
    ['console.error', `console.error('page-boom')`],
  ])('fails on %s', (_name, script) => {
    const r = smoke(script)
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('smoke: FAIL')
    expect(r.out).toContain('page-boom')
    expect(r.out).not.toContain('smoke: pass')
  }, 90_000)

  it('fails on a script the page could not load', () => {
    const r = smoke('', `<script src="/assets/missing-page-boom.js"></script>`)
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('missing-page-boom.js')
  }, 90_000)

  // The allow-list names routes: a route the fake cmdrd is known not to serve passes, any other 404 fails.
  it('allows a named route and no other', () => {
    expect(smoke(`fetch('/api/chat')`).code).toBe(0)
    const r = smoke(`fetch('/api/not-a-route-page-boom')`)
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('/api/not-a-route-page-boom')
  }, 90_000)

  // The allow-list is for reads: the fake cmdrd answers a write's 404 just the same, so it is seen there.
  it('fails on a write to a named route', () => {
    const r = smoke(`fetch('/api/chat', { method: 'POST' })`)
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('POST /api/chat')
  }, 90_000)

  // One read travels as a POST: that method on that route passes, on any other route or by any other method it fails.
  it('allows only the POST that reads Overview', () => {
    const ok = smoke(`fetch('/api/overview', { method: 'POST' })`)
    expect(ok.code, ok.out).toBe(0)
    const r = smoke(`fetch('/api/overview', { method: 'DELETE' })`)
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('DELETE /api/overview')
  }, 90_000)

  it('fails on an error in the seconds after the shot', () => {
    const r = smoke(`setTimeout(() => { throw new Error('page-boom') }, 1500)`)
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('page-boom')
  }, 90_000)

  // A page that never answers: the way to make the browser stall on demand. The run ends by itself,
  // names the call and still reports what the page said first.
  it('names the call the browser left unanswered, with the page errors it has', () => {
    const r = smoke(`console.error('page-boom'); while (true) {}`, '', ['--fake', '--answer-ms', '3000'])
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('expected conn=live, got nothing (the browser did not answer Runtime.evaluate in 3s)')
    expect(r.out).toContain('page-boom')
  }, 90_000)

  // Once the page has connected the page's state is no news: only the call is named.
  it('names only the call when the browser stops answering a connected page', () => {
    const r = smoke(`console.log('fine')`, '', ['--fake', '--answer-ms', '3000', '--eval', 'while (true) {}'])
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('the browser did not answer Runtime.evaluate in 3s')
    expect(r.out).not.toContain('expected conn')
  }, 90_000)

  // --served: the page comes from cmdrd, which has no icon either.
  it('passes a clean page cmdrd serves, and still fails on its missing script', async () => {
    const clean = tinyPage(`console.log('fine')`)
    const ok = await withStubCmdrd(clean, (home) => run('scripts/smoke.mjs', ['--served', '--dist', clean, '--out', join(clean, 'shot.png'), '--scale', '1'], { COMMANDER_HOME: home }))
    expect(ok.code, ok.out).toBe(0)
    const broken = tinyPage('', `<script src="/app/missing-page-boom.js"></script>`)
    const bad = await withStubCmdrd(broken, (home) => run('scripts/smoke.mjs', ['--served', '--dist', broken, '--out', join(broken, 'shot.png'), '--scale', '1'], { COMMANDER_HOME: home }))
    expect(bad.code, bad.out).toBe(1)
    expect(bad.out).toContain('missing-page-boom.js')
  }, 180_000)

  // shots: a live page error is the answer, never swapped for a fixture shot that passes.
  const shots = (dist: string, home: string) => run('scripts/shots.mjs', ['--no-build', '--dist', dist, '--themes', 'dark', '--out-dir', join(dist, 'shots')], { COMMANDER_HOME: home })
  it('shots reports a page error from a cmdrd that answers', async () => {
    const dist = tinyPage(`throw new Error('page-boom')`)
    const r = await withStubCmdrd(dist, (home) => shots(dist, home))
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('page-boom')
    expect(r.out).toContain('dark failed (live cmdrd)')
  }, 90_000)

  // A stranger on a stale api-url answers too: that is no cmdrd, and it must not cost the fixture shot.
  it('shots falls back to the fixture when a stranger answers the api-url', async () => {
    const dist = tinyPage(`document.documentElement.dataset.conn = 'offline'; document.body.insertAdjacentHTML('beforeend', '<div class="start-screen on"></div>')`)
    const r = await withStubCmdrd(dist, (home) => shots(dist, home), false)
    expect(r.code, r.out).toBe(0)
    expect(r.out).toContain('fixture (cmdrd unreachable)')
  }, 90_000)

  it('shots falls back to the fixture only when cmdrd does not answer', () => {
    const dist = tinyPage(`document.documentElement.dataset.conn = 'offline'; document.body.insertAdjacentHTML('beforeend', '<div class="start-screen on"></div>')`)
    const home = join(root, 'home-down')
    mkdirSync(home)
    writeFileSync(join(home, 'api-token'), 'stub')
    writeFileSync(join(home, 'api-url'), 'http://127.0.0.1:1')
    const r = shots(dist, home)
    expect(r.code, r.out).toBe(0)
    expect(r.out).toContain('fixture (cmdrd unreachable)')
  }, 90_000)

  // Idle through the whole wait: the browser may leave the screenshot unanswered, and that must not hang.
  it('still names the page error when the page never connected', () => {
    const r = smoke(`document.documentElement.dataset.conn = 'nope'; throw new Error('page-boom')`)
    expect(r.code, r.out).toBe(1)
    expect(r.out).toContain('page-boom')
  }, 120_000)
})
