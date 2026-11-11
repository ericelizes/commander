import { useState } from 'react'
import { useStore } from '../store'
import { commanderName } from '../jurisdiction'

interface Jurisdiction {
  project_id: number
  bead_id: string
  commander_id: string
  revision: number
  explicit: boolean
}

// Conversations may change ownership; the goal tree and sidebar stay in place.
export function WorkstreamCommander({ projectId, beadId, workstreamKey, owner }: {
  projectId: number; beadId: string; workstreamKey: string; owner: string
}) {
  const config = useStore((s) => s.config)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [open, setOpen] = useState(false)
  if (!config) return null

  const run = async (action: 'open' | 'urgent' | 'return') => {
    if (busy) return
    setBusy(true)
    setError('')
    const scope = { projectId, beadId, workstreamKey }
    try {
      const request = async (path: string, body?: Record<string, unknown>): Promise<Jurisdiction> => {
        const response = await fetch(`${config.apiUrl}${path}`, {
          method: body ? 'POST' : 'GET',
          headers: { Authorization: `Bearer ${config.token}`, 'Content-Type': 'application/json' },
          ...(body ? { body: JSON.stringify(body) } : {}),
        })
        const data = await response.json()
        if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`)
        return data as Jurisdiction
      }
      let current = await request(`/api/jurisdiction?project_id=${scope.projectId}&bead_id=${encodeURIComponent(scope.beadId)}`)
      if (action !== 'open') {
        current = await request(action === 'urgent' ? '/api/commanders' : '/api/jurisdiction', {
          project_id: scope.projectId, bead_id: scope.beadId,
          expected_owner: current.commander_id, revision: current.revision,
          reason: action === 'urgent' ? 'User opened a focused urgent conversation' : 'User returned planning to the parent Commander',
          ...(action === 'urgent' ? { title: 'Urgent investigation' } : { inherit: true }),
        })
      }
      useStore.getState().select({ kind: 'agent', key: current.commander_id, ws: scope.workstreamKey })
      useStore.getState().setChatTab(true)
      setOpen(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not open the Commander conversation')
    } finally {
      setBusy(false)
    }
  }

  return <div className="workstream-commander">
    <button type="button" aria-label="Commander for this workstream" title={owner} aria-expanded={open} onClick={() => setOpen(!open)} disabled={busy}>
      {busy ? 'Updating…' : `${commanderName(owner)} ▾`}
    </button>
    {open && <div className="workstream-commander-menu">
      <button type="button" disabled={busy} onClick={() => void run('open')}>Open responsible Commander</button>
      <button type="button" disabled={busy} onClick={() => void run('urgent')} title="Assign this subtree to a separate Commander while other work continues">Open urgent thread</button>
      {owner.startsWith('commander-') && <button type="button" disabled={busy} onClick={() => void run('return')}>Return to parent Commander</button>}
    </div>}
    {error && <div className="workstream-commander-error" role="alert">{error}</div>}
  </div>
}
