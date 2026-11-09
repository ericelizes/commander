#!/usr/bin/env node
// What the app looks like right now, in every theme: builds the renderer, then
// shoots it headlessly (scripts/smoke.mjs, chrome-headless-shell, no window)
// against the real cmdrd, or the fixture snapshot when cmdrd does not answer. A failed
// shot of a cmdrd that answers is a failure (exit 1): the app has a problem to look at.
//
//   npm run shots                         -> out/shots/{dark,light,glass}.png
//   npm run shots -- --no-build           reuse dist/ (after a build you just ran)
//   npm run shots -- --themes light       one theme
//   npm run shots -- --query 'ws=2'       any page fixture: ws=n, ask=n, demo=web, fixture=chat
//   npm run shots -- --out-dir /tmp/x     somewhere else
//   npm run shots -- --no-build --dist d  another built page

import { spawn, spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join, resolve } from 'node:path'

const argv = process.argv.slice(2)
const opt = (name, def) => {
  const i = argv.indexOf(name)
  return i >= 0 ? argv[i + 1] : def
}
const themes = opt('--themes', 'dark,light,glass').split(',')
const outDir = resolve(opt('--out-dir', 'out/shots'))
const query = opt('--query', '')

if (!argv.includes('--no-build')) {
  const b = spawnSync('npx', ['electron-vite', 'build'], { encoding: 'utf8' })
  if (b.status !== 0) {
    console.error(b.stdout, b.stderr)
    process.exit(b.status ?? 1)
  }
}

// cmdrd answers this route in JSON whatever the token, 401 included; a stranger on a stale
// api-url is no cmdrd, so it gets the fixture. The smoke reads the token and connection itself.
async function cmdrdAnswers() {
  const home = process.env.COMMANDER_HOME || join(homedir(), '.commander')
  try {
    const token = readFileSync(join(home, 'api-token'), 'utf8').trim()
    if (!token) return false
    const api = readFileSync(join(home, 'api-url'), 'utf8').trim() || 'http://127.0.0.1:8787'
    const r = await fetch(`${api}/api/settings`, { headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(5000) })
    return r.status !== 404 && (r.headers.get('content-type') ?? '').includes('json')
  } catch {
    return false
  }
}

function shoot(theme, offline) {
  const args = ['scripts/smoke.mjs', '--theme', theme, '--out', `${outDir}/${theme}.png`, '--query', query]
  if (argv.includes('--dist')) args.push('--dist', opt('--dist', ''))
  if (argv.includes('--eval')) args.push('--eval', opt('--eval', ''))
  if (argv.includes('--fake')) args.push('--fake', '--fixture', opt('--fixture', 'test/fixtures/snapshot.json'))
  else if (offline) args.push('--offline')
  return new Promise((ok) => {
    const p = spawn(process.execPath, args, { stdio: ['ignore', 'pipe', 'pipe'] })
    let log = ''
    p.stdout.on('data', (d) => (log += d))
    p.stderr.on('data', (d) => (log += d))
    p.on('close', (code) => ok({ code, log }))
  })
}

const fake = argv.includes('--fake')
const live = fake || (await cmdrdAnswers())
const source = fake ? 'fixture' : live ? 'live cmdrd' : 'fixture (cmdrd unreachable)'
// One at a time: parallel headless shells can miss smoke's deadline.
const results = []
for (const theme of themes) results.push({ theme, source, ...(await shoot(theme, !live)) })
for (const r of results) {
  if (r.code === 0) console.log(`${outDir}/${r.theme}.png  (${r.source})`)
  else console.error(`shots: ${r.theme} failed (${r.source})\n${r.log}`)
}
process.exitCode = results.every((r) => r.code === 0) ? 0 : 1
