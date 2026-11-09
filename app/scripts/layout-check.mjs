#!/usr/bin/env node
// Layout check: build the renderer into a temp dir and measure the navigator in
// a headless browser (scripts/smoke.mjs --fake) on a stress fixture: long
// unbroken titles and 18 ticket ids per row. It fails when
//   - the sidebar scrolls sideways (.nav-scroll scrollWidth > clientWidth),
//   - anything in the sidebar reaches past its right edge.
// jsdom has no layout, so this is the real check; test/nav-no-sideways.test.ts runs it.
//
//   node scripts/layout-check.mjs [--keep]   (--keep leaves the build and shots in the temp dir)

import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const app = resolve(import.meta.dirname, '..')
const keep = process.argv.includes('--keep')
const fixture = 'test/fixtures/snapshot-overflow.json'
const views = ['', 'ws=1', 'ws=3', 'ask=1']

// Runs in the page once animations settle; leaves the list of problems in window.__layout.
const probe = `new Promise((r) => setTimeout(r, 400)).then(() => {
  const out = []
  const nav = document.querySelector('.nav-scroll')
  if (!nav) return (window.__layout = ['no .nav-scroll'])
  if (nav.scrollWidth > nav.clientWidth) out.push('nav scrolls sideways: ' + nav.scrollWidth + ' > ' + nav.clientWidth)
  const right = nav.getBoundingClientRect().right
  const past = [...nav.querySelectorAll('*')].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.right > right + 0.5 })
  if (past.length) out.push(past.length + ' elements past the sidebar edge, first ' + past[0].className)
  if (!nav.querySelector('.row, .nyq-item')) out.push('no rows rendered')
  window.__layout = out
})`

function run(cmd, args) {
  const r = spawnSync(cmd, args, { cwd: app, encoding: 'utf8' })
  return { ok: r.status === 0, out: `${r.stdout ?? ''}${r.stderr ?? ''}${r.error ? r.error.message : ''}` }
}

const dir = mkdtempSync(join(tmpdir(), 'cmdr-layout-'))
let failed = false
try {
  const dist = join(dir, 'dist')
  const b = run(process.execPath, ['node_modules/vite/bin/vite.js', 'build', '--config', 'vite.renderer.config.mts', '--outDir', dist, '--emptyOutDir', '--logLevel', 'error'])
  if (!b.ok) {
    console.error(`layout: FAIL: renderer build\n${b.out}`)
    process.exit(1)
  }
  for (const [i, query] of views.entries()) {
    const args = ['scripts/smoke.mjs', '--fake', '--fixture', fixture, '--dist', dist, '--out', join(dir, `view-${i}.png`), '--scale', '1', '--eval', probe, '--expect', 'JSON.stringify(window.__layout)', '--expect', 'window.__layout.length === 0']
    if (query) args.push('--query', query)
    const r = run(process.execPath, args)
    const said = r.out.split('\n').find((l) => l.startsWith('smoke: expect -> "')) ?? ''
    console.log(`layout: ${query || 'all'}: ${r.ok ? 'pass' : 'FAIL'} ${said.replace('smoke: expect -> ', '')}`)
    if (!r.ok) {
      failed = true
      console.error(r.out.split('\n').filter((l) => !l.startsWith('smoke: expect')).join('\n'))
    }
  }
} finally {
  if (keep) console.log(`layout: kept ${dir}`)
  else rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 })
}
process.exitCode = failed ? 1 : 0
