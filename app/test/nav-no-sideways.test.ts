import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

// jsdom has no layout, so this runs the real check: scripts/layout-check.mjs
// builds the renderer into a temp dir and measures the navigator in a headless
// browser on the stress fixture (long unbroken titles, 18 ticket ids per row;
// it once measured 2245 > 340). It fails when the sidebar scrolls sideways or
// anything reaches past its right edge.
// Also `npm run test:layout`. Without a headless browser the check is skipped,
// unless COMMANDER_LAYOUT_CHECK_REQUIRED=1 (internal-ship sets it): then it fails.
const app = join(__dirname, '..')
const browser = spawnSync(process.execPath, ['scripts/smoke.mjs', '--which-browser'], { cwd: app, encoding: 'utf8' })
const plan = withoutBrowser(browser.status === 0, process.env)
if (plan === 'skip') console.warn('nav-no-sideways: layout check skipped: no headless browser (set CHROME_HEADLESS_SHELL)')

function withoutBrowser(haveBrowser: boolean, env: Record<string, string | undefined>): 'run' | 'skip' | 'fail' {
  if (haveBrowser) return 'run'
  return env.COMMANDER_LAYOUT_CHECK_REQUIRED === '1' ? 'fail' : 'skip'
}

describe('the sidebar never scrolls sideways', () => {
  it.skipIf(plan === 'skip')('keeps the navigator inside its column', () => {
    if (plan === 'fail') throw new Error('COMMANDER_LAYOUT_CHECK_REQUIRED is set and there is no headless browser: set CHROME_HEADLESS_SHELL or install one under ~/.commander/browsers')
    const r = spawnSync(process.execPath, ['scripts/layout-check.mjs'], { cwd: app, encoding: 'utf8' })
    expect(r.status, `${r.stdout}${r.stderr}`).toBe(0)
    expect(r.stdout.match(/^layout: .*: pass/gm)?.length).toBe(4)
  }, 180_000)

  it('fails instead of skipping when the check is required', () => {
    expect(withoutBrowser(true, {})).toBe('run')
    expect(withoutBrowser(true, { COMMANDER_LAYOUT_CHECK_REQUIRED: '1' })).toBe('run')
    expect(withoutBrowser(false, {})).toBe('skip')
    expect(withoutBrowser(false, { COMMANDER_LAYOUT_CHECK_REQUIRED: '1' })).toBe('fail')
  })

  it('has a stress fixture with long unbroken titles and many ticket ids', () => {
    const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot-overflow.json'), 'utf8'))
    const row = snap.display.workstreams[0]
    expect(row.title.split(' ')[0].length).toBeGreaterThan(100)
    expect(row.title.match(/TEAM-\d+/g).length).toBeGreaterThanOrEqual(18)
  })
})
