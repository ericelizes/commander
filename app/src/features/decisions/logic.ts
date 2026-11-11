import type { CmdResult, CommanderApi, Decision, NeedsYouItem, ResolveAction, SnapshotLike } from './types'

// How a card is answered: pick an option, write a note, or confirm the steps
// are done. Mirrors decisionState.ready in internal/tui/column_inspector_decision.go.
export type AnswerMode = 'options' | 'steps' | 'input'

export function answerMode(d: Decision): AnswerMode {
  if (d.options && d.options.length > 0) return 'options'
  if (d.steps && d.steps.length > 0) return 'steps'
  return 'input'
}

// The --choice a submit sends, or null while the form is not valid yet.
export function readyChoice(d: Decision, selected: number, note: string): string | null {
  switch (answerMode(d)) {
    case 'options':
      return selected >= 0 && selected < d.options!.length ? d.options![selected].label : null
    case 'steps':
      return 'done'
    case 'input':
      return note.trim() !== '' ? 'answered' : null
  }
}

// The hint under Submit while it is disabled.
export function validationHint(d: Decision): string {
  switch (answerMode(d)) {
    case 'options':
      return d.options!.length === 1 ? 'Pick the option (1)' : `Pick an option (1–${d.options!.length})`
    case 'input':
      return 'Write an answer first'
    case 'steps':
      return ''
  }
}

// The argv for POST /api/cmd that resolves a flag.
export function resolveArgv(item: NeedsYouItem, action: ResolveAction, choice?: string, note?: string): string[] {
  const argv = ['internal-resolve-needs', '--project', item.project, '--bead', item.bead_id, '--action', action]
 if (item.plan_revision) argv.push('--revision', String(item.plan_revision))
  if (action === 'decide') {
    if (choice) argv.push('--choice', choice)
    const text = (note ?? '').trim()
    if (text) argv.push('--text', text)
  }
  return argv
}

// A failed cmdr run as one readable line. cmdr's internal-* verbs print
// {"error": "..."} on stderr.
export function cmdError(res: CmdResult): string {
  const raw = (res.stderr || '').trim()
  for (const line of raw.split('\n')) {
    try {
      const parsed = JSON.parse(line)
      if (parsed && typeof parsed.error === 'string' && parsed.error) return parsed.error
    } catch {
      // not JSON: fall through to the raw text
    }
  }
  if (raw) return raw
  if (res.error) return res.error
  return `cmdr exited with status ${res.exit}`
}

export function itemKey(item: Pick<NeedsYouItem, 'project' | 'bead_id'>): string {
  return `${item.project}/${item.bead_id}`
}

// Whether the flag is still open in a snapshot (false = answered elsewhere).
export function stillPending(snap: SnapshotLike, item: NeedsYouItem): boolean {
  const key = itemKey(item)
  return (snap.needs_you ?? []).some(n => itemKey(n) === key)
}

// A missing snapshot card can still have an unfinished durable answer.
// Resume only completes that answer; it never creates a new one.
export type ResolveResult = { status: 'ok' } | { status: 'gone' } | { status: 'error', message: string }

export async function resolveNeeds(api: CommanderApi, item: NeedsYouItem, action: ResolveAction, choice?: string, note?: string): Promise<ResolveResult> {
  let gone = false
  try {
    gone = !stillPending(await api.snapshot(), item)
  } catch {
    // The snapshot is only a guard; if it is unavailable, try the command anyway.
  }
  try {
    const argv = resolveArgv(item, action, choice, note)
    if (gone) argv.push('--resume-only')
    const res = await api.runCmd(argv)
    return res.ok ? { status: gone ? 'gone' : 'ok' } : { status: 'error', message: cmdError(res) }
  } catch (err) {
    return { status: 'error', message: err instanceof Error ? err.message : String(err) }
  }
}

// Compact age: "now", "12m", "3h", "4d". Empty when the time is unknown.
export function formatAge(sinceUnix: number | undefined, nowMs: number): string {
  if (!sinceUnix) return ''
  const secs = Math.max(0, Math.floor(nowMs / 1000) - sinceUnix)
  if (secs < 60) return 'now'
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`
  return `${Math.floor(secs / 86400)}d`
}

// What the queue shows for an item: the decision question, else the ask, else the step title.
export function itemQuestion(item: NeedsYouItem): string {
  return item.decision?.question || item.ask || item.title || item.bead_id
}

export function needsYouCount(snap: SnapshotLike | null | undefined): number {
  return snap?.needs_you?.length ?? 0
}

export async function copyText(api: CommanderApi, text: string): Promise<void> {
  if (api.copy) return api.copy(text)
  if (typeof navigator !== 'undefined' && navigator.clipboard) return navigator.clipboard.writeText(text)
  throw new Error('clipboard unavailable')
}

// Context paragraphs: blank lines separate paragraphs; single newlines stay.
export function paragraphs(text: string | undefined): string[] {
  return (text ?? '').split(/\n\s*\n/).map(p => p.trim()).filter(Boolean)
}

/** askWhere is where an item lives: "Workstream › Nested workstream", or just the workstream. */
export function askWhere(item: Pick<NeedsYouItem, 'workstream' | 'project' | 'track' | 'track_letter'>): string {
  const ws = item.workstream || item.project
  if (!item.track) return ws
  // A title may carry an old letter ("A. Script audits"); its number shows beside the rank.
  return `${ws} › ${item.track.replace(/^[A-Z]\.\s+/, '')}`
}
