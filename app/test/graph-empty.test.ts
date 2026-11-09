import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { buildCanvas } from '../src/renderer/src/graph'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot

describe('buildCanvas', () => {
  it('has a finite size with no workstreams (cmdrd briefly reporting none must not crash the graph)', () => {
    const c = buildCanvas({ ...snap, workstreams: [] })
    expect(Number.isFinite(c.width) && Number.isFinite(c.height)).toBe(true)
  })
})
