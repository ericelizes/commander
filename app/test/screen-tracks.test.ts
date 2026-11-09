// The board reaches Commander through its turn hook, so the screen block says
// only what else is on screen and which board version the user is looking at.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { screenContext } from '../src/renderer/src/screen'
import { useStore } from '../src/renderer/src/store'
import { board } from '../src/renderer/src/viewmodel'

it('stamps the board version on screen and leaves the rows to the hook', () => {
  const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot-tracks.json'), 'utf8')) as Snapshot
  useStore.getState().setSnapshot({ ...snap, board_versions: { commander: 124 } })
  const w = board(useStore.getState().snapshot).workstreams.find((x) => x.tracks.length > 0)!
  const text = screenContext('commander')
  expect(text).toContain('- Board on screen: v124')
  expect(text).not.toContain(`${w.title}: ${w.text}`)
  expect(text).not.toMatch(/\bhis\b|\bhim\b|\bhe\b/)
  expect(screenContext('operator-x')).not.toContain('Board on screen')
})
