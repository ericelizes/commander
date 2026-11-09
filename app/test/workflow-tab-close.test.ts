import { describe, expect, it } from 'vitest'
import { useStore } from '../src/renderer/src/store'

describe('Workflow tab', () => {
  it('opens on a header click and closes with its ×', () => {
    useStore.setState({ workflowOpen: null })
    useStore.getState().openWorkflow('p/ws-1')
    expect(useStore.getState().workflowOpen?.ws).toBe('p/ws-1')
    useStore.getState().closeWorkflow()
    expect(useStore.getState().workflowOpen).toBeNull()
  })
})
