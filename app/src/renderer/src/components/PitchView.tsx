// PitchView is an approval in the focus pane (which names what it belongs to):
// the workstream's (or track's) diagram cmdrd derives from beads
// (DerivedDiagram), filling the pane. The choices sit above the chat input
// (AskDock), so every answer goes in one place. A ```cmdr-graph the card carries is ignored; only a cmdrd without
// diagrams, or an older saved version, shows the drawn one (ExecutionFlow).
import { ChevronRight, Pencil } from 'lucide-react'
import { useEffect, useState } from 'react'
import { askWhere, resolveNeeds, type NeedsYouItem } from '../../../features/decisions'
import { commanderApi } from '../api'
import { chatBackend } from '../chatApi'
import { fetchPitches, type SavedPitch } from '../pitchesApi'
import { useStore } from '../store'
import { askKey, findTrack, trackTitle, wsKey } from '../viewmodel'
import { DerivedDiagram, diagramFor } from './DerivedDiagram'
import { WorkflowView } from './WorkflowView'
import type { FocusTarget } from './FocusHeader'
import type { BoardRow } from '../../../shared/snapshot'

/** usePitch is every saved version of item's pitch (oldest first) and the latest one's answer. */
export function usePitch(item: NeedsYouItem): { versions: SavedPitch[]; answer?: string } {
  const cfg = useStore((st) => st.config)
  const [versions, setVersions] = useState<SavedPitch[]>([])
  useEffect(() => {
    if (!cfg) return
    let live = true
    void fetchPitches(cfg, item.project, item.bead_id).then((all) => live && setVersions(all.filter((v) => v.bead_id === item.bead_id)))
    return () => {
      live = false
    }
  }, [cfg, item.project, item.bead_id, item.decision?.context])
  return { versions, answer: versions[versions.length - 1]?.answer }
}

/** focusOf is what an ask belongs to, as the sidebar names it: its workstream and track. */
export function focusOf(item: NeedsYouItem, row: BoardRow | undefined, letter: string): FocusTarget {
  const t = findTrack(row, item.track_id)
  const on = t ?? row
  return {
    n: row?.n ?? (item.rank || undefined),
    title: row?.title ?? (item.workstream || askWhere(item)),
    letter: t?.letter ?? (letter || undefined),
    track: item.track ? trackTitle(t?.title ?? item.track) : undefined,
    kind: on?.kind,
    text: on?.text,
    progress: on?.progress,
  }
}

export function PitchView({ item }: { item: NeedsYouItem; gone?: boolean }) {
  const unwritten = pitchUnwritten(item.decision, item.note)
  // By its workstream, not the row's title: a row standing in for its one child reads that child's.
  const row = useStore((st) => {
    const rows = st.snapshot?.display?.workstreams
    const ws = st.snapshot?.workstreams.find((w) => w.project === item.project && w.title === item.workstream)
    return rows?.find((r) => (ws ? r.key === wsKey(ws) : r.project === item.project && r.title === item.workstream))
  })
  const derived = diagramFor(row, item.track_id)
  return (
    <div className="pitch-pane">
      <div className="decision-scroll">
        <article className="fx">
          {item.stale_why && <p className="fx-note">{item.stale_why}</p>}
          {derived ? <DerivedDiagram diagram={derived} /> : row?.workflow && <WorkflowView workflow={row.workflow} focus={item.bead_id} />}
          {unwritten && <p className="fx-note">Its Manager has not written this pitch, so there is nothing to decide on yet. Ask Commander for it, or it comes back once written.</p>}
        </article>
      </div>
    </div>
  )
}


/** withoutDrawnGraphs drops every ```cmdr-graph block, parsed or not, from a pitch. */
export function withoutDrawnGraphs(md: string): string {
  return md.replace(/```cmdr-graph\n[\s\S]*?(```|$)/g, '').replace(/\n{3,}/g, '\n\n').trim()
}

// Each kind of section keeps one colour and icon, so a pitch reads at a glance.

// PitchChoices is the pitch's answer card, under the document.
export function PitchChoices({ item, question, where, version }: { item: NeedsYouItem; question: string; where: string; version: number }) {
  const cfg = useStore((s) => s.config)
  const [sent, setSent] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [gone, setGone] = useState(false)
  const options = item.decision?.options?.length ? item.decision.options : [{ label: 'Approve', recommended: true }, { label: 'Not yet' }]
  const choose = async (label: string) => {
    if (!cfg || busy || sent || gone) return
    setBusy(label)
    setError('')
    try {
      const note = [`Pitch: ${item.title}${version > 1 ? ` (v${version})` : ''}`, `Where: ${where}`, `Id: ${item.bead_id}`].join('\n')
      const res = await resolveNeeds(commanderApi(cfg), item, 'decide', label, note)
      if (res.status === 'error') {
        setError(`Could not record answer: ${res.message}`)
        return
      }
      if (res.status === 'gone') setGone(true)
      else setSent(label)
      // Remove only this revision after the durable command succeeds. The live
      // snapshot supplies the resulting gate/workstream state.
      useStore.setState((s) => {
        const snap = s.snapshot
        if (!snap?.needs_you.some((n) => askKey(n) === askKey(item) && n.plan_revision === item.plan_revision)) return {}
        return { snapshot: {
          ...snap,
          needs_you: snap.needs_you.filter((n) => askKey(n) !== askKey(item)),
          display: snap.display ? { ...snap.display, asks: snap.display.asks.filter((a) => a.key !== askKey(item)) } : undefined,
        } }
      })
      if (res.status === 'gone') return
      try {
        await chatBackend(cfg).transport.send('commander', {
          text: `FYI: Answer already recorded: ${label}\nProject: ${item.project}\n${note}`,
          clientId: `pitch-${item.bead_id}-${Date.now()}`,
        })
      } catch (e) {
        setError(`Answer recorded, but could not notify Commander: ${(e as Error).message ?? e}`)
      }
    } catch (e) {
      setError(`Could not record answer: ${(e as Error).message ?? e}`)
    } finally {
      setBusy('')
    }
  }
  const typeOwn = () => (document.querySelector('.cc-composer textarea') as HTMLTextAreaElement | null)?.focus()
  return (
    <section className="pitch-choices" aria-label={question}>
      {item.stale_why && <p className="fx-note">{item.stale_why}</p>}
      <div className="pitch-choices-q">{gone ? 'Already answered elsewhere.' : sent ? `Answer recorded: ${sent}` : question}</div>
      <ul>
        {options.map((o, i) => (
          <li key={o.label}>
            <button type="button" className={`pitch-choice${o.recommended ? ' is-rec' : ''}${sent === o.label ? ' is-sent' : ''}`} disabled={!!busy || !!sent || gone} onClick={() => void choose(o.label)}>
              <span className="pitch-choice-n">{sent === o.label ? '✓' : i + 1}</span>
              <span className="pitch-choice-text">
                <span className="pitch-choice-label">
                  {busy === o.label && !sent && !gone ? 'Recording…' : spaceNumbers(o.label)}
                  {o.recommended && <span className="pitch-choice-rec">Recommended</span>}
                </span>
                {o.implications && <span className="pitch-choice-sub">{spaceNumbers(o.implications)}</span>}
              </span>
            </button>
          </li>
        ))}
        {(sent || gone) && (
          <li>
            <NextAsk done={item} />
          </li>
        )}
        {!sent && !gone && (
          <li>
            <button type="button" className="pitch-choice" onClick={typeOwn}>
              <span className="pitch-choice-n">
                <Pencil size={12} />
              </span>
              <span className="pitch-choice-label muted">Something else: tell Commander in the chat</span>
            </button>
          </li>
        )}
      </ul>
      {error && (
        <p className="state-error" role="alert">
          {error}
        </p>
      )}
    </section>
  )
}

const SECTION_NAMES = "[Hh]igh level|[Ii]n short|[Aa]t a glance|[Ww]hat'?s wrong|[Ww]hat is wrong|[Tt]he plan|[Ee]xecution workflow|[Aa]gentic workflow"
// A name counts as a heading when it is bold, ends in a colon, stands alone, or a
// capitalised sentence follows it straight away ("What's wrong The CLI has...").
const SECTION_START = new RegExp(`^[ \\t]*(?:#{1,4}[ \\t]*)?(?:\\*\\*(${SECTION_NAMES}):?\\*\\*:?|(${SECTION_NAMES})(?::|(?=[ \\t]*$)|(?=[ \\t]+[A-Z])))[ \\t]*(.*)$`, 'gm')

function sectionName(name: string): string {
  const n = name.toLowerCase()
  if (n.startsWith('in short') || n.startsWith('high level')) return 'High level'
  if (n.startsWith('at a')) return 'At a glance'
  if (n.startsWith('what')) return "What's wrong"
  if (n.includes('plan')) return 'The plan'
  return 'Execution workflow'
}

// pitchUnwritten: a checkpoint whose Manager never wrote its pitch, nothing to decide on yet.
// cmdrd keeps these out of Needs you (livedata hasPitch), so only a direct link lands here.
export function pitchUnwritten(card: NeedsYouItem['decision'], note?: string): boolean {
  return !(card?.context ?? note ?? '').trim() && !card?.steps?.length && !card?.options?.length
}

function parseCard(json: string): NeedsYouItem['decision'] {
  try {
    return JSON.parse(json) as NeedsYouItem['decision']
  } catch {
    return undefined
  }
}


// NextAsk offers the next thing waiting on the user, in sidebar order after this one.
function NextAsk({ done }: { done: NeedsYouItem }) {
  // Select the stored list itself: a selector that builds a new array re-renders forever.
  const all = useStore((st) => st.snapshot?.needs_you)
  const rest = (all ?? [])
    .filter((n) => n.bead_id !== done.bead_id)
    .sort((a, b) => a.rank - b.rank)
  const next = rest.find((n) => n.rank >= done.rank) ?? rest[0]
  if (!next) return <p className="pitch-next is-none">Nothing else needs you.</p>
  return (
    <button type="button" className="pitch-choice pitch-next" onClick={() => useStore.getState().select({ kind: 'ask', key: askKey(next) })}>
      <span className="pitch-choice-n">
        <ChevronRight size={14} />
      </span>
      <span className="pitch-choice-label">Next: {next.title}</span>
    </button>
  )
}

const GRAPH = /```cmdr-graph\n([\s\S]*?)```/

/**
 * pitchMarkdown readies a pitch's Markdown for reading: a bold-only line is a
 * section heading; a plan that is only the graph, or an older "Agentic
 * workflow", is the "Execution workflow"; the
 * `Gate | Approving it means | Who` table moves into the graph's gates (the
 * graph then says it once); the repeated question line goes.
 */
export function pitchMarkdown(md: string, question = ''): string {
  let out = md.replace(/\r\n/g, '\n')
  if (question) out = out.split('\n').filter((l) => l.trim() !== question.trim()).join('\n')
  out = out.replace(/^\*\*([^*\n]+)\*\*[ \t]*$/gm, '### $1')
  // Agents also write a section name inline ("What's wrong The workbench CLI has...",
  // "**The plan:** ..."): a known name at a line start is a heading either way.
  out = out.replace(SECTION_START, (_m, bold: string | undefined, plain: string | undefined, rest: string) => `### ${sectionName(bold ?? plain ?? '')}\n\n${rest.trim()}`.trimEnd())
  out = spaceAfterPunctuation(out)
  // A sentence written straight under a table is not one of its rows.
  out = out.replace(/^(\|.*\|)[ \t]*\n(?=[ \t]*[^|\s])/gm, '$1\n\n')
  out = out.replace(/^###\s*The plan\s*\n+(?=```cmdr-graph)/m, '### Execution workflow\n\n')
  out = out.replace(/^###\s*Agentic workflow\s*$/gim, '### Execution workflow')

  const table = /^\|\s*Gate\s*\|\s*Approving[^\n]*\n\|[-|\s:]+\|\n((?:\|[^\n]*\|\n?)+)/m.exec(out)
  const graph = GRAPH.exec(out)
  if (table && graph) {
    const means = new Map<string, string>()
    for (const line of table[1].trim().split('\n')) {
      const [gate, meaning] = line.split('|').slice(1).map((c) => c.trim())
      if (gate && meaning) means.set(norm(gate), meaning)
    }
    try {
      const spec = JSON.parse(graph[1]) as { nodes?: unknown[] }
      let hit = 0
      const walk = (nodes: unknown[] = []) => {
        for (const n of nodes as { title?: string; gate?: boolean; means?: string; children?: unknown[] }[]) {
          const m = n.gate && n.title ? means.get(norm(n.title)) : undefined
          if (m && !n.means) {
            n.means = m
            hit++
          }
          walk(n.children)
        }
      }
      walk(spec.nodes)
      if (hit) {
        out = out.replace(graph[0], '```cmdr-graph\n' + JSON.stringify(spec) + '\n```')
        out = out.replace(table[0], '')
      }
    } catch {
      // a graph that does not parse is drawn (or reported) by ChatGraph as it is
    }
  }
  return out.replace(/\n{3,}/g, '\n\n').trim()
}


// Agents squeeze text ("Healthy;routine info", "behavior,rechecking"): put
// the space back after a comma or semicolon before a word, outside code and URLs.
// Short words agents glue to a number ("on20", "and18"); longer words are caught by length.
const GLUED_BEFORE = /\b(?=[a-z]*[g-z])([a-z]{5,}|on|at|in|of|to|by|and|or|for|per|top|all|the|than|with|next|last|over|only|from|took|ran|is|are|was|vs|up)(\d)/g
// A number glued to the word after it ("86seconds"); a word of hex letters only may be a hash.
const GLUED_AFTER = /(^|[\s(])(\d+(?:\.\d+)?)(?=[a-z]*[g-z])([a-z]{3,})\b/g

/** spaceNumbers puts back the spaces some models drop around numbers ("about86seconds"). */
export function spaceNumbers(text: string): string {
  // Paths, file names and handles keep their exact spelling.
  return text
    .split(/(\s+)/)
    .map((word) => (/[/_@\\]/.test(word) ? word : word.replace(GLUED_BEFORE, '$1 $2').replace(/([a-z])([:;,])(\d)/g, '$1$2 $3')))
    .join('')
    .replace(GLUED_AFTER, (m, pre: string, num: string, unit: string, at: number, all: string) => (/[/_@\\]/.test(all.slice(at).split(/\s/)[0]) ? m : `${pre}${num} ${unit}`))
}

function spaceAfterPunctuation(md: string): string {
  let fenced = false
  return md
    .split('\n')
    .map((line) => {
      if (line.startsWith('```')) fenced = !fenced
      if (fenced || line.startsWith('```')) return line
      return line
        .split(/(`[^`]*`|\S*:\/\/\S*)/)
        .map((part, i) => (i % 2 ? part : spaceNumbers(part.replace(/([a-z)])([;,])(?=[A-Za-z(])/g, '$1$2 '))))
        .join('')
    })
    .join('\n')
}

type SectionKind = 'summary' | 'glance' | 'wrong' | 'plan' | 'workflow' | 'other'

/** pitchSections splits a pitch at its ### headings, each kept with what it is for. */
export function pitchSections(md: string): { title: string; kind: SectionKind; body: string }[] {
  const out: { title: string; kind: SectionKind; body: string }[] = []
  for (const chunk of md.split(/^### /m)) {
    if (!chunk.trim()) continue
    const nl = chunk.indexOf('\n')
    const first = out.length === 0 && !md.startsWith('### ')
    const title = first ? '' : (nl < 0 ? chunk : chunk.slice(0, nl)).trim()
    const body = first ? chunk : nl < 0 ? '' : chunk.slice(nl + 1)
    const t = title.toLowerCase()
    const kind: SectionKind = /^(in short|high level)/.test(t) ? 'summary' : /glance|key numbers|in numbers/.test(t) ? 'glance' : /wrong|problem|what.s broken|why/.test(t) ? 'wrong' : /plan/.test(t) ? 'plan' : /workflow/.test(t) ? 'workflow' : 'other'
    out.push({ title, kind, body: body.trim() })
  }
  return out
}

/** glanceStats reads "At a glance" bullets ("- **151** commits master is behind") as big numbers. */
export function glanceStats(body: string): { value: string; label: string }[] {
  const out: { value: string; label: string }[] = []
  for (const line of body.split('\n')) {
    const m = /^\s*[-*]\s+\*\*([^*]+)\*\*\s*[:\u2013-]?\s*(.+)$/.exec(line)
    if (m) out.push({ value: m[1].trim(), label: m[2].trim() })
  }
  return out
}

const norm = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim()

/** SavedPitchView shows a pitch nobody is waiting on any more, from its saved versions. */
export function SavedPitchView({ project, bead }: { project: string; bead: string }) {
  const cfg = useStore((st) => st.config)
  const [latest, setLatest] = useState<SavedPitch | null | undefined>(undefined)
  useEffect(() => {
    if (!cfg) return
    void fetchPitches(cfg, project, bead).then((all) => setLatest(all.filter((v) => v.bead_id === bead).at(-1) ?? null))
  }, [cfg, project, bead])
  if (latest === undefined) return null
  if (latest === null) return <div className="insp-empty">No pitch was saved for {bead}.</div>
  const item = { project, bead_id: bead, title: latest.title, workstream: '', rank: 0, decision: parseCard(latest.decision) } as NeedsYouItem
  return <PitchView item={item} gone />
}
