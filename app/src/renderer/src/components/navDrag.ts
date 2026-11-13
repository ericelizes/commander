// Where a sidebar drag lands: the rows as drawn, flat, and the place a pointer
// position means among them. Pure, so the tree rules are tested without a mouse.
import type { BoardRow, BoardTrack } from '../../../shared/snapshot'

export interface NavNode {
  key: string
  title: string
  bead: string // the bead that moves
  holds: string // the bead a child goes under
  standIn?: string // the one child this row stands in for: a new child goes beside it
  project: string
  depth: number
  parent: NavNode | null
}

// parentOf finds the real parent of the child a row stands in for (a chain can sit under the row).
export function flatRows(rows: BoardRow[], folded: (key: string) => boolean, open: (key: string) => boolean, parentOf: (bead: string) => string | undefined = () => undefined): NavNode[] {
  const out: NavNode[] = []
  const walk = (ts: BoardTrack[] | undefined, parent: NavNode, depth: number) => {
    for (const t of ts ?? []) {
      const node: NavNode = { key: t.key, title: t.title, bead: t.bead_id, holds: t.bead_id, project: parent.project, depth, parent }
      out.push(node)
      if (open(t.key)) walk(t.children, node, depth + 1)
    }
  }
  for (const w of rows) {
    const node: NavNode = { key: w.key, title: w.title, bead: w.bead_id, holds: (w.scope && parentOf(w.scope)) || w.bead_id, standIn: w.scope, project: w.project, depth: 0, parent: null }
    out.push(node)
    if (!folded(w.key)) walk(w.tracks, node, 1)
  }
  return out
}

// without is the list with a row and everything drawn under it taken out.
export function without(flat: NavNode[], key: string): NavNode[] {
  const i = flat.findIndex((n) => n.key === key)
  if (i < 0) return flat
  let end = i + 1
  while (end < flat.length && flat[end].depth > flat[i].depth) end++
  return [...flat.slice(0, i), ...flat.slice(end)]
}

export interface Drop {
  gap: number // the row it goes before, in the list without it
  depth: number
  parent: NavNode | null
  after: NavNode | null // the sibling it follows
  to: number // its rank among its siblings
}

// dropAt is what a gap and a wanted indent mean: the indent the rows around the gap
// allow, nearest to the one wanted. Work cannot cross projects, so a row only nests
// under its own project's rows. Null when it would land where it already is.
export function dropAt(flat: NavNode[], key: string, gap: number, want: number): Drop | null {
  const dragged = flat.find((n) => n.key === key)
  if (!dragged) return null
  const list = without(flat, key)
  gap = Math.max(0, Math.min(gap, list.length))
  const lowest = list[gap]?.depth ?? 0
  const highest = gap > 0 ? list[gap - 1].depth + 1 : 0
  const parentAt = (depth: number): NavNode | null => {
    if (depth === 0) return null
    for (let i = gap - 1; i >= 0; i--) if (list[i].depth === depth - 1) return list[i]
    return null
  }
  const allowed = (depth: number) => depth === 0 || parentAt(depth)?.project === dragged.project
  let depth = -1
  for (let d = Math.min(Math.max(want, lowest), highest); d >= lowest; d--) {
    if (allowed(d)) { depth = d; break }
  }
  if (depth < 0) return null
  const parent = parentAt(depth)
  let after: NavNode | null = null
  let to = 1
  for (let i = gap - 1; i >= 0 && list[i].depth >= depth; i--) {
    if (list[i].depth === depth) {
      after = after ?? list[i]
      to++
    }
  }
  const was = flat.findIndex((n) => n.key === key)
  let before: NavNode | null = null
  for (let i = was - 1; i >= 0 && flat[i].depth >= dragged.depth; i--) {
    if (flat[i].depth === dragged.depth) { before = flat[i]; break }
  }
  if (parent?.key === dragged.parent?.key && after?.key === before?.key) return null
  return { gap, depth, parent, after, to }
}

// placeArgv is the command a drop runs.
export function placeArgv(dragged: NavNode, drop: Drop): string[] {
  const argv = ['internal-place', '--project', dragged.project, '--bead', dragged.bead, '--parent', drop.parent?.holds ?? '']
  // Among nested siblings the one before it is named; the top level spans projects, so it goes by rank.
  const after = drop.after?.bead ?? drop.parent?.standIn
  return drop.parent && after ? [...argv, '--after', after] : [...argv, '--to', String(drop.to)]
}

// Capture before the move; later board updates must not change the undo target.
export function previousPlace(flat: NavNode[], dragged: NavNode): Drop {
  const siblings = flat.filter((n) => n.parent?.key === dragged.parent?.key)
  const index = siblings.findIndex((n) => n.key === dragged.key)
  return { gap: flat.indexOf(dragged), depth: dragged.depth, parent: dragged.parent, after: siblings[index - 1] ?? null, to: index + 1 }
}

export function undoArgv(flat: NavNode[], dragged: NavNode): string[] {
  return placeArgv(dragged, previousPlace(flat, dragged))
}

const toTrack = (r: BoardRow): BoardTrack => ({
  key: r.key, bead_id: r.bead_id, letter: '', title: r.title, kind: r.kind, text: r.text, since: r.since, progress: r.progress,
  milestones: r.milestones, working: r.working, why: r.why, age_as: r.age_as, children: r.tracks,
})

// applyMove is the sidebar as it will read once the board shows a drop, so a row
// lands the moment it is let go. The numbers follow; the board has the last word.
export function applyMove(rows: BoardRow[], dragged: NavNode, drop: Drop): BoardRow[] {
  let moved: BoardTrack | null = null
  let movedRow: BoardRow | null = null
  const take = (ts: BoardTrack[]): BoardTrack[] =>
    ts.filter((t) => {
      if (t.key === dragged.key) { moved = t; return false }
      return true
    }).map((t) => (t.children ? { ...t, children: take(t.children) } : t))
  let next = rows.filter((r) => {
    if (r.key === dragged.key) { movedRow = r; return false }
    return true
  }).map((r) => ({ ...r, tracks: take(r.tracks) }))
  const row = movedRow as BoardRow | null
  const track = (moved ?? (row ? toTrack(row) : null)) as BoardTrack | null
  if (!track) return rows
  const place = (ts: BoardTrack[]): BoardTrack[] => {
    const at = drop.after ? ts.findIndex((t) => t.key === drop.after!.key) + 1 : 0
    return [...ts.slice(0, at), track, ...ts.slice(at)]
  }
  const under = (ts: BoardTrack[]): BoardTrack[] =>
    ts.map((t) => (t.key === drop.parent!.key ? { ...t, children: place(t.children ?? []) } : t.children ? { ...t, children: under(t.children) } : t))
  if (drop.parent) {
    // A row standing in for its one child opens up: that child and this one, side by side.
    const opened = (r: BoardRow): BoardTrack[] =>
      r.scope ? [{ ...toTrack(r), key: `${r.project}:${r.scope}`, bead_id: r.scope }, track] : place(r.tracks)
    next = next.map((r) => (r.key === drop.parent!.key ? { ...r, scope: undefined, tracks: opened(r) } : { ...r, tracks: under(r.tracks) }))
  } else {
    const template = rows.find((r) => r.project === dragged.project) ?? rows[0]
    const top: BoardRow = row ?? { ...template, key: track.key, bead_id: track.bead_id, title: track.title, kind: track.kind, text: track.text, since: track.since,
      progress: track.progress, milestones: track.milestones, working: track.working, why: track.why, age_as: track.age_as, tracks: track.children ?? [], scope: undefined, starred: false, flag: 0, unread: 0, gate: null, manager_role: '' }
    next = [...next.slice(0, drop.to - 1), top, ...next.slice(drop.to - 1)]
  }
  return renumbered(next)
}

const number = (ts: BoardTrack[], prefix: string): BoardTrack[] =>
  ts.map((t, i) => {
    const letter = `${prefix}.${i + 1}`
    return { ...t, letter, children: t.children && number(t.children, letter) }
  })

function renumbered(rows: BoardRow[]): BoardRow[] {
  return rows.map((r, i) => ({ ...r, n: i + 1, tracks: number(r.tracks, '') }))
}

// withoutGone takes rows the user just completed out of the list, numbers closing
// up at once rather than when the board catches up. Rows are untouched if none went.
export function withoutGone(rows: BoardRow[], gone: (key: string) => boolean): BoardRow[] {
  let changed = false
  const keep = <T extends { key: string }>(list: T[]): T[] => list.filter((x) => {
    if (gone(x.key)) changed = true
    return !gone(x.key)
  })
  const tracks = (ts: BoardTrack[]): BoardTrack[] => keep(ts).map((t) => (t.children ? { ...t, children: tracks(t.children) } : t))
  const next = keep(rows).map((r) => ({ ...r, tracks: tracks(r.tracks) }))
  return changed ? renumbered(next) : rows
}

// landed: the board now shows the row where it was dropped, so the sidebar can stop holding it there.
export function landed(rows: BoardRow[], dragged: NavNode, drop: Drop): boolean {
  const flat = flatRows(rows, () => false, () => true)
  const now = flat.find((n) => n.bead === dragged.bead)
  if (!now) return false
  if ((now.parent?.holds ?? '') !== (drop.parent?.holds ?? '')) return false
  if (!drop.parent) return flat.filter((n) => n.depth === 0).findIndex((n) => n.bead === dragged.bead) === drop.to - 1
  if (!drop.after) return true // a child of its own may have been made before it
  const siblings = flat.filter((n) => n.parent?.key === now.parent?.key)
  return siblings[siblings.findIndex((n) => n.bead === dragged.bead) - 1]?.bead === drop.after.bead
}
