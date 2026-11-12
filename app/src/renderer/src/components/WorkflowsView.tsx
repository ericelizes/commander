// WorkflowsView is the Workflows page: the catalog of workflows the agents
// follow, each with what it is for, when to use it and when to skip it, and its
// steps. It reads the same templates the planners read.

import { useEffect, useState } from 'react'
import { useStore } from '../store'

export type CatalogStep = { id: string; title: string; actor: string; optional?: boolean }
export type CatalogEntry = {
  id: string
  title: string
  applies_to?: string
  summary?: string
  use_when?: string[]
  skip_when?: string[]
  catalog_only?: boolean
  steps: CatalogStep[]
}

const actorNames: Record<string, string> = { user: 'You', owner: 'App owner', operator: 'Agent', manager: 'Agent', commander: 'Commander' }

export function WorkflowsView() {
  const cfg = useStore((s) => s.config)
  const [entries, setEntries] = useState<CatalogEntry[] | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!cfg) return
    let active = true
    fetch(`${cfg.apiUrl}/api/workflows`, { headers: { Authorization: `Bearer ${cfg.token}` } })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(String(r.status)))))
      .then((body: { workflows: CatalogEntry[] }) => { if (active) setEntries(body.workflows) }, () => { if (active) setFailed(true) })
    return () => { active = false }
  }, [cfg])
  return (
    <div className="progress-page">
      <div className="progress-col wf-col">
        <h1 className="progress-title">Workflows</h1>
        <p className="wf-intro">What the agents follow for each kind of work, and when. They read these same rules before using one.</p>
        {failed && <p role="alert" className="wf-note">The workflows could not be loaded.</p>}
        {!entries && !failed && <p className="wf-note">Loading…</p>}
        {entries?.map((w) => <WorkflowCard key={w.id} w={w} />)}
      </div>
    </div>
  )
}

export function WorkflowCard({ w }: { w: CatalogEntry }) {
  return (
    <section className="wf-card" aria-label={w.title}>
      <h2>{w.title}</h2>
      {w.applies_to && <p className="wf-applies">For {w.applies_to}</p>}
      {w.summary && <p className="wf-summary">{w.summary}</p>}
      <div className="wf-rules">
        <div>
          <h3>Use when</h3>
          <ul>{(w.use_when ?? []).map((r, i) => <li key={i}>{r}</li>)}</ul>
        </div>
        <div>
          <h3>Skip when</h3>
          <ul>{(w.skip_when ?? []).map((r, i) => <li key={i}>{r}</li>)}</ul>
        </div>
      </div>
      <h3>Steps</h3>
      <ol className="wf-steps">
        {w.steps.map((s) => (
          <li key={s.id}>
            <span>{s.title}</span>
            <span className="wf-who">{actorNames[s.actor] ?? 'Agent'}{s.optional ? ' · optional' : ''}</span>
          </li>
        ))}
      </ol>
      {w.catalog_only && <p className="wf-note">Agents follow these steps by hand; the board doesn't add them as steps yet.</p>}
    </section>
  )
}
