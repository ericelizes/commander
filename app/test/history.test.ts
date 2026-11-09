// Back/Forward: every selection change is recorded; Back and Forward walk it;
// a new selection after going Back drops the Forward trail, like a browser.
import { beforeEach, describe, expect, it } from 'vitest'
import { useStore } from '../src/renderer/src/store'

beforeEach(() => {
  useStore.setState({ selected: { kind: 'commander' } })
  useStore.setState({ back: [], forward: [] }) // after: the reset itself is a selection change
})

describe('selection history', () => {
  it('goes back and forward through what was selected', () => {
    const st = () => useStore.getState()
    st().select({ kind: 'ws', key: 'p:a' })
    st().select({ kind: 'ask', key: 'ask:p:b' })
    st().goBack()
    expect(st().selected).toEqual({ kind: 'ws', key: 'p:a' })
    expect(st().cursor).toBe('p:a')
    st().goBack()
    expect(st().selected).toEqual({ kind: 'commander' })
    st().goBack() // nothing further back
    expect(st().selected).toEqual({ kind: 'commander' })
    st().goForward()
    expect(st().selected).toEqual({ kind: 'ws', key: 'p:a' })
    st().select({ kind: 'ws', key: 'p:c' })
    expect(st().forward).toEqual([])
    expect(st().back.at(-1)).toEqual({ kind: 'ws', key: 'p:a' })
  })

  it('does not record re-selecting what is already selected', () => {
    useStore.getState().select({ kind: 'commander' })
    expect(useStore.getState().back).toEqual([])
  })
})
