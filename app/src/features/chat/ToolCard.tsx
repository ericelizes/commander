import { memo, useEffect, useState, type ReactNode } from 'react'
import { fmtDuration, toolHeadline, toolLabel, type ToolItem } from './events'
import { diffStats, fromStructuredPatch, lineDiff, type DiffLine } from './diff'
import { Icon, Spinner, type IconName } from './icons'
import { CopyButton, Markdown } from './Markdown'
import { useItems } from './context'
import { ItemView } from './rows'

type Obj = Record<string, unknown>
const obj = (v: unknown): Obj => (v && typeof v === 'object' ? (v as Obj) : {})
const s = (v: unknown): string => (typeof v === 'string' ? v : v == null ? '' : String(v))

function iconFor(name: string): IconName {
  if (name === 'Bash' || name === 'BashOutput' || name === 'KillShell') return 'terminal'
  if (name === 'Read') return 'file'
  if (name === 'Edit' || name === 'Write' || name === 'MultiEdit' || name === 'NotebookEdit') return 'edit'
  if (name === 'Grep' || name === 'Glob') return 'search'
  if (name === 'WebFetch' || name === 'WebSearch') return 'globe'
  if (name === 'Task' || name === 'Agent') return 'bot'
  if (name === 'TodoWrite') return 'list'
  if (name.startsWith('mcp__')) return 'plug'
  return 'zap'
}

// parsePartial best-effort parses streaming tool input JSON so the card can
// show e.g. the command while it is still being generated.
function parsePartial(p: string): Obj {
  if (!p) return {}
  try {
    return JSON.parse(p)
  } catch {
    for (const close of ['"}', '}', '"]}', ']}']) {
      try {
        return JSON.parse(p + close)
      } catch {
        /* keep trying */
      }
    }
    return {}
  }
}

export function Elapsed({ from, to }: { from?: number; to?: number }) {
  const [, tick] = useState(0)
  useEffect(() => {
    if (to || !from) return
    const t = setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(t)
  }, [from, to])
  if (!from || (to && to - from < 50)) return null
  return <span className="cc-dur">{fmtDuration(Math.max(0, (to ?? Date.now()) - from))}</span>
}

export function DiffView({ lines, max = 400 }: { lines: DiffLine[]; max?: number }) {
  const shown = lines.slice(0, max)
  // Line numbers are only real when they came from a structuredPatch hunk.
  const numbered = lines.some((l) => l.kind === 'hunk')
  return (
    <div className={`cc-diff${numbered ? '' : ' cc-diff-plain'}`} role="table" aria-label="diff">
      {shown.map((l, i) => (
        <div key={i} className={`cc-diff-line cc-diff-${l.kind}`} role="row">
          {numbered && <span className="cc-diff-no">{l.kind === 'hunk' ? '' : (l.oldNo ?? '')}</span>}
          {numbered && <span className="cc-diff-no">{l.kind === 'hunk' ? '' : (l.newNo ?? '')}</span>}
          <span className="cc-diff-sign">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ''}</span>
          <span className="cc-diff-text">{l.text || ' '}</span>
        </div>
      ))}
      {lines.length > max && <div className="cc-more">… {lines.length - max} more lines</div>}
    </div>
  )
}

function Output({ text, error, max = 4000 }: { text: string; error?: boolean; max?: number }) {
  if (!text) return null
  const clipped = text.length > max ? text.slice(0, max) : text
  return (
    <div className={`cc-output${error ? ' cc-output-error' : ''}`}>
      <CopyButton text={text} />
      <pre>{clipped}</pre>
      {text.length > max && <div className="cc-more">… {(text.length - max).toLocaleString()} more characters</div>}
    </div>
  )
}

export function editDiff(name: string, input: Obj, structured: Obj): DiffLine[] {
  const patch = structured.structuredPatch
  if (Array.isArray(patch) && patch.length) return fromStructuredPatch(patch as never)
  if (name === 'Write') return lineDiff(s(structured.originalFile ?? ''), s(input.content))
  if (name === 'MultiEdit' && Array.isArray(input.edits)) return (input.edits as Obj[]).flatMap((e) => lineDiff(s(e.old_string), s(e.new_string)))
  return lineDiff(s(input.old_string), s(input.new_string))
}

function exitCode(item: ToolItem): number | undefined {
  const m = /exit code (\d+)/i.exec(item.output ?? '')
  if (m) return Number(m[1])
  if (item.status === 'done') return 0
  return undefined
}

function Body({ item, input }: { item: ToolItem; input: Obj }) {
  const st = obj(item.structured)
  const err = item.status === 'error' || item.status === 'denied'
  switch (item.name) {
    case 'Bash': {
      const stdout = s(st.stdout) || (err ? '' : s(item.output))
      const stderr = s(st.stderr) || (err && !stdout ? s(item.output) : '')
      return (
        <>
          <div className="cc-cmd">
            <span className="cc-prompt">$</span>
            <code>{s(input.command)}</code>
            <CopyButton text={s(input.command)} />
          </div>
          <Output text={stdout} />
          <Output text={stderr} error />
          {item.output != null && !stdout && !stderr && <div className="cc-muted">(no output)</div>}
        </>
      )
    }
    case 'Edit':
    case 'MultiEdit':
    case 'Write': {
      const lines = editDiff(item.name, input, st)
      return (
        <>
          {lines.length > 0 && <DiffView lines={lines} />}
          {err && <Output text={s(item.output)} error />}
        </>
      )
    }
    case 'Read': {
      const file = obj(st.file)
      return <Output text={s(file.content) || s(item.output)} error={err} />
    }
    case 'Grep':
    case 'Glob': {
      const files = Array.isArray(st.filenames) ? (st.filenames as string[]) : []
      if (files.length && !st.content) {
        return (
          <ul className="cc-filelist">
            {files.slice(0, 200).map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        )
      }
      return <Output text={s(st.content) || s(item.output)} error={err} />
    }
    case 'WebFetch':
      return (
        <>
          <a className="cc-link" href={s(input.url)} target="_blank" rel="noreferrer noopener">
            {s(input.url)}
          </a>
          {s(input.prompt) && <div className="cc-muted">{s(input.prompt)}</div>}
          <div className="cc-md-small">
            <Markdown text={s(st.result) || s(item.output)} />
          </div>
        </>
      )
    case 'WebSearch': {
      const results = (Array.isArray(st.results) ? st.results : []).flatMap((r) => (Array.isArray(obj(r).content) ? (obj(r).content as Obj[]) : [obj(r)]))
      if (results.some((r) => r.url)) {
        return (
          <ul className="cc-results">
            {results
              .filter((r) => r.url)
              .slice(0, 12)
              .map((r, i) => (
                <li key={i}>
                  <a href={s(r.url)} target="_blank" rel="noreferrer noopener">
                    {s(r.title) || s(r.url)}
                  </a>
                  <span className="cc-muted">{s(r.url).replace(/^https?:\/\//, '').split('/')[0]}</span>
                </li>
              ))}
          </ul>
        )
      }
      return <Output text={s(item.output)} error={err} />
    }
    case 'TodoWrite': {
      const todos = Array.isArray(input.todos) ? (input.todos as Obj[]) : []
      return (
        <ul className="cc-todos">
          {todos.map((t, i) => (
            <li key={i} className={`cc-todo cc-todo-${s(t.status)}`}>
              <span className="cc-todo-box">{t.status === 'completed' ? <Icon name="check" size={11} /> : null}</span>
              {t.status === 'in_progress' ? s(t.activeForm) || s(t.content) : s(t.content)}
            </li>
          ))}
        </ul>
      )
    }
    default:
      return (
        <>
          <div className="cc-section-label">Input</div>
          <Output text={JSON.stringify(input, null, 2)} />
          {item.output != null && (
            <>
              <div className="cc-section-label">Result</div>
              <Output text={item.output} error={err} />
            </>
          )}
        </>
      )
  }
}

function headline(item: ToolItem, input: Obj): ReactNode {
  if (item.name === 'Bash') return <code>{s(input.command) || s(input.description)}</code>
  return toolHeadline(item.name, input)
}

function meta(item: ToolItem, input: Obj): ReactNode {
  const st = obj(item.structured)
  if (item.name === 'Edit' || item.name === 'MultiEdit' || item.name === 'Write') {
    if (item.status === 'input') return null
    const { add, del } = diffStats(editDiff(item.name, input, st))
    return (
      <span className="cc-diffstat">
        <span className="cc-add">+{add}</span> <span className="cc-del">−{del}</span>
      </span>
    )
  }
  if (item.name === 'Read' && obj(st.file).numLines) return <span className="cc-muted">{String(obj(st.file).numLines)} lines</span>
  if ((item.name === 'Grep' || item.name === 'Glob') && st.numFiles != null) return <span className="cc-muted">{String(st.numFiles)} files</span>
  if (item.name === 'Bash' && item.status !== 'running' && item.status !== 'input') {
    const code = exitCode(item)
    if (st.interrupted) return <span className="cc-badge cc-badge-warn">interrupted</span>
    if (code != null) return <span className={`cc-badge ${code === 0 ? 'cc-badge-ok' : 'cc-badge-err'}`}>exit {code}</span>
  }
  return null
}

function StatusIcon({ status, awaiting }: { status: ToolItem['status']; awaiting?: boolean }) {
  if (awaiting) return <Icon name="shield" size={12} className="cc-warn" />
  if (status === 'input' || status === 'running') return <Spinner size={13} />
  if (status === 'done') return <Icon name="check" size={13} className="cc-ok" />
  if (status === 'interrupted') return <Icon name="stop" size={12} className="cc-warn" />
  return <Icon name="x" size={13} className="cc-err" />
}

function SubAgent({ item, input }: { item: ToolItem; input: Obj }) {
  const items = useItems()
  const st = obj(item.structured)
  const resultText = Array.isArray(st.content) ? (st.content as Obj[]).map((c) => s(c.text)).join('\n') : s(item.output)
  const childTools = item.children.filter((id) => items[id]?.type === 'tool').length
  const running = item.status === 'running' || item.status === 'input'
  const shown = item.children.slice(-12)
  return (
    <div className="cc-subagent">
      <div className="cc-subagent-meta">
        {s(input.subagent_type) && <span className="cc-chip">{s(input.subagent_type)}</span>}
        <span className="cc-muted">
          {childTools} tool call{childTools === 1 ? '' : 's'}
          {item.task?.lastTool && running ? ` · now ${item.task.lastTool}` : ''}
          {st.totalTokens ? ` · ${Number(st.totalTokens).toLocaleString()} tokens` : ''}
        </span>
      </div>
      {s(input.prompt) && (
        <details className="cc-prompt-details">
          <summary>Prompt</summary>
          <div className="cc-md-small">
            <Markdown text={s(input.prompt)} />
          </div>
        </details>
      )}
      {item.children.length > 0 && (
        <div className="cc-children">
          {item.children.length > shown.length && <div className="cc-more">… {item.children.length - shown.length} earlier events</div>}
          {shown.map((id) => (
            <ItemView key={id} id={id} nested />
          ))}
        </div>
      )}
      {!running && resultText && (
        <div className="cc-subagent-result">
          <div className="cc-section-label">Result</div>
          <div className="cc-md-small">
            <Markdown text={resultText} />
          </div>
        </div>
      )}
    </div>
  )
}

export const ToolCard = memo(function ToolCard({ item, nested }: { item: ToolItem; nested?: boolean }) {
  const isAgent = item.name === 'Task' || item.name === 'Agent'
  const input = item.status === 'input' && item.inputPartial ? parsePartial(item.inputPartial) : obj(item.input)
  const isError = item.status === 'error' || item.status === 'denied'
  const [open, setOpen] = useState<boolean | null>(null)
  const autoOpen = isAgent || isError || item.name === 'TodoWrite' || ((item.name === 'Edit' || item.name === 'MultiEdit') && !nested)
  const expanded = open ?? autoOpen
  return (
    <div className={`cc-tool cc-tool-${item.status}${nested ? ' cc-tool-nested' : ''}${isAgent ? ' cc-tool-agent' : ''}`} data-tool={item.name}>
      <button type="button" className="cc-tool-head" aria-expanded={expanded} onClick={() => setOpen(!expanded)}>
        <span className="cc-tool-status">
          <StatusIcon status={item.status} awaiting={item.awaiting} />
        </span>
        <Icon name={iconFor(item.name)} size={13} className="cc-tool-icon" />
        <span className="cc-tool-name">{isAgent ? 'Agent' : toolLabel(item.name)}</span>
        <span className="cc-tool-headline">{headline(item, input)}</span>
        <span className="cc-tool-meta">
          {item.approval === 'deny' || item.status === 'denied' ? (
            <span className="cc-badge cc-badge-err">denied</span>
          ) : item.approval ? (
            <span className="cc-badge" title="Approved by you">
              <Icon name="shield" size={10} /> {item.approval === 'always' ? 'always' : 'approved'}
            </span>
          ) : null}
          {meta(item, input)}
          {item.awaiting ? <span className="cc-badge cc-badge-warn">awaiting approval</span> : item.status === 'running' || item.status === 'input' ? <Elapsed from={item.ts} /> : item.endedAt && item.ts ? <Elapsed from={item.ts} to={item.endedAt} /> : null}
          <Icon name="chevron" size={12} className={`cc-chevron${expanded ? ' cc-open' : ''}`} />
        </span>
      </button>
      {expanded && <div className="cc-tool-body">{isAgent ? <SubAgent item={item} input={input} /> : <Body item={item} input={input} />}</div>}
    </div>
  )
})

/** ToolDetail is a tool call's expanded body (output, diff, sub-agent), without the card around it. */
export function ToolDetail({ item }: { item: ToolItem }) {
  const input = item.status === 'input' && item.inputPartial ? parsePartial(item.inputPartial) : obj(item.input)
  return item.name === 'Task' || item.name === 'Agent' ? <SubAgent item={item} input={input} /> : <Body item={item} input={input} />
}

// ToolGroup collapses a run of consecutive tool calls into one row.
export function ToolGroup({ ids }: { ids: string[] }) {
  const items = useItems()
  const tools = ids.map((id) => items[id]).filter((t): t is ToolItem => t?.type === 'tool')
  const [open, setOpen] = useState(false)
  const counts = new Map<string, number>()
  for (const t of tools) counts.set(toolLabel(t.name), (counts.get(toolLabel(t.name)) ?? 0) + 1)
  const live = tools.filter((t) => t.status === 'running' || t.status === 'input' || t.status === 'error' || t.status === 'denied')
  const errors = tools.filter((t) => t.status === 'error' || t.status === 'denied').length
  const files = new Set(tools.map((t) => s(obj(t.input).file_path)).filter(Boolean))
  return (
    <div className="cc-group">
      <button type="button" className="cc-group-head" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name="layers" size={13} />
        <span className="cc-group-title">{tools.length} tool calls</span>
        <span className="cc-group-summary">
          {[...counts].map(([n, c]) => `${n}${c > 1 ? ` ×${c}` : ''}`).join(' · ')}
          {files.size > 0 && ` · ${files.size} file${files.size > 1 ? 's' : ''}`}
        </span>
        {errors > 0 && <span className="cc-badge cc-badge-err">{errors} failed</span>}
        <Icon name="chevron" size={12} className={`cc-chevron${open ? ' cc-open' : ''}`} />
      </button>
      <div className="cc-group-body">
        {(open ? tools : live).map((t) => (
          <ToolCard key={t.id} item={t} />
        ))}
      </div>
    </div>
  )
}
