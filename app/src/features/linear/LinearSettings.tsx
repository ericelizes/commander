import { useRef, useState } from 'react'
import { useStore } from '../../renderer/src/store'
import { refreshLinearSprint, useSprint } from './sprint'

export function LinearSettings() {
  const config = useStore((s) => s.config)
  const saved = useSprint((s) => s.data?.key === 'saved')
  const input = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(false)
  async function send(remove: boolean) {
    if (!config || busy) return
    setBusy(true); setError(false)
    // Uncontrolled password field: no credential is kept in React or persisted.
    let body: string | undefined = remove ? undefined : JSON.stringify({ key: input.current?.value ?? '' })
    if (input.current) input.current.value = ''
    try {
      const pending = fetch(`${config.apiUrl}/api/linear/key`, {
        method: remove ? 'DELETE' : 'POST', headers: { Authorization: `Bearer ${config.token}`, 'Content-Type': 'application/json' }, body,
      })
      body = undefined
      const res = await pending
      if (!res.ok) throw new Error('save failed')
      useSprint.setState((s) => ({ data: remove ? { state: 'no_key', issues: [], key: 'not set' } : { ...(s.data ?? { state: 'no_key', issues: [] }), key: 'saved' } }))
      if (!remove) refreshLinearSprint(config)
    } catch { setError(true) } finally { setBusy(false) }
  }
  return <section className="linear-settings">
    <h4>Linear</h4>
    <p className="settings-note">Reads your active-cycle tickets, read only. Commander never changes tickets.</p>
    <p role="status">Key: {saved ? 'saved' : 'not set'}</p>
    <form onSubmit={(e) => { e.preventDefault(); void send(false) }}>
      <label>Linear API key <input ref={input} type="password" autoComplete="off" spellCheck={false} aria-label="Linear API key" disabled={busy} /></label>
      <button className="btn btn-primary" type="submit" disabled={busy || !config}>{saved ? 'Replace' : 'Save'}</button>
      {saved && <button className="btn" type="button" disabled={busy} onClick={() => void send(true)}>Remove</button>}
    </form>
    {error && <p role="alert">Could not update the Linear key. Try again.</p>}
  </section>
}
