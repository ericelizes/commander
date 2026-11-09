import { describe, it, expect } from 'vitest'
import { normalizeOwnerSnapshot } from '../src/renderer/src/ownerCompat'

describe('legacy owner snapshots', () => {
  it('normalizes nested state and identity without rewriting evidence', () => {
    const old = { state: 'needs-user', steps: [{ approvers: ['user', 'alice'], owner: 'user', verifier: 'user', evidence: 'user approved' }] }
    expect(normalizeOwnerSnapshot(old)).toEqual({ state: 'needs-user', steps: [{ approvers: ['user', 'alice'], owner: 'user', verifier: 'user', evidence: 'user approved' }] })
    expect(old.state).toBe('needs-user')
  })
})
