import type { SyncMsg } from './contract'

export type ViewPlan = {
  create: number[] // lazily: only the active tab, the first time it is shown
  destroy: number[] // views whose tab is gone
  reload: Array<{ id: number; url: string }> // views whose tab now points elsewhere
  show: number | null // the one visible view; every other view is hidden
}

// planSync turns the renderer's declared state into view operations, given
// the views that exist (id -> the url they were created for).
export function planSync(existing: Map<number, string>, msg: SyncMsg): ViewPlan {
  const wanted = new Map(msg.tabs.map((t) => [t.id, t.url]))
  const plan: ViewPlan = { create: [], destroy: [], reload: [], show: null }
  for (const [id, url] of existing) {
    if (!wanted.has(id)) plan.destroy.push(id)
    else if (wanted.get(id) !== url) plan.reload.push({ id, url: wanted.get(id)! })
  }
  const active = msg.activeId
  if (active !== null && wanted.has(active) && msg.rect && msg.rect.width > 0 && msg.rect.height > 0) {
    if (!existing.has(active)) plan.create.push(active)
    plan.show = active
  }
  return plan
}
