import { createContext, memo, useContext, useState, type ReactNode } from 'react'
import ReactMarkdown, { defaultUrlTransform, type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { ChatHostContext } from './host'
import { completeMarkdown, rehypeFadeWords } from './thread/streaming'

// Whether the text being rendered is still streaming in.
const StreamingContext = createContext(false)

// GRAPH_LANG is the fence an agent opens to draw a graph in chat.
export const GRAPH_LANG = 'cmdr-graph'

/**
 * GraphBlock draws a ```cmdr-graph block: a loading skeleton while its JSON
 * streams in, the graph once the JSON is complete.
 */
function GraphBlock({ code }: { code: string }) {
  const { Graph } = useContext(ChatHostContext)
  const streaming = useContext(StreamingContext)
  let spec: unknown
  try {
    spec = JSON.parse(code)
  } catch {
    if (streaming) {
      return (
        <div className="cc-graph-loading" role="status">
          <span className="cc-shimmer">Loading graph…</span>
        </div>
      )
    }
    return <CodeBlock code={code} lang={GRAPH_LANG} />
  }
  return Graph ? <Graph spec={spec} /> : <CodeBlock code={code} lang={GRAPH_LANG} />
}

type LinkTab = 'overview' | 'workflow' | 'decision'

/** BeadLink is a `cmdr:<bead-id>[/tab]` link: it opens that workstream or track, on that tab. */
function BeadLink({ id, tab, children }: { id: string; tab?: LinkTab; children: ReactNode }) {
  const { openBead, describeBead } = useContext(ChatHostContext)
  if (!openBead) return <>{children}</>
  return (
    <button type="button" className="cc-bead-link" onClick={() => openBead(id, tab)} title={describeBead?.(id, tab)}>
      {children}
    </button>
  )
}

// Agents link beads as cmdr:<bead-id>[/tab]; every other link goes through the usual safety filter.
const BEAD_LINK = /^cmdr:([\w.-]+)(?:\/(overview|workflow|decision))?$/
const BEAD_CONTEXT = /^cmdr:([\w.-]+)$/

function urlTransform(url: string): string {
  return BEAD_LINK.test(url) ? url : defaultUrlTransform(url)
}

/** copyableMarkdown drops a link's hidden workstream context: what is copied is just the page. */
export function copyableMarkdown(text: string): string {
  return text.replace(/(\]\(\S+)\s+"cmdr:[\w.-]+"\)/g, '$1)')
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // Fallback for non-secure contexts (plain http web mode over a tunnel).
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand?.('copy') ?? false
    ta.remove()
    return ok
  }
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className="cc-copy"
      aria-label={label}
      onClick={async (e) => {
        e.stopPropagation()
        if (await copyText(text)) {
          setDone(true)
          setTimeout(() => setDone(false), 1200)
        }
      }}
    >
      {done ? 'Copied' : label}
    </button>
  )
}

function nodeText(n: ReactNode): string {
  if (n == null || typeof n === 'boolean') return ''
  if (typeof n === 'string' || typeof n === 'number') return String(n)
  if (Array.isArray(n)) return n.map(nodeText).join('')
  if (typeof n === 'object' && 'props' in n) return nodeText((n as { props: { children?: ReactNode } }).props.children)
  return ''
}

export function CodeBlock({ code, lang }: { code: string; lang?: string }) {
  return (
    <div className="cc-codeblock">
      <div className="cc-codeblock-bar">
        <span className="cc-codeblock-lang">{lang || 'text'}</span>
        <CopyButton text={code} />
      </div>
      <pre>
        <code>{code}</code>
      </pre>
    </div>
  )
}

// WebLink opens in the app's side panel when the host has one (a plain click);
// Cmd/Ctrl-click, or no panel, leaves it to the system browser.
function WebLink({ href, bead, children }: { href?: string; bead?: string; children: ReactNode }) {
  const host = useContext(ChatHostContext)
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      onClick={(e) => {
        if (!href || !/^https?:/.test(href) || e.metaKey || e.ctrlKey || e.shiftKey) return
        if ((bead && host.openBeadPage?.(bead, href, nodeText(children) || undefined)) || host.openUrl?.(href)) e.preventDefault()
      }}
    >
      {children}
    </a>
  )
}

const components: Components = {
  pre({ children }) {
    const child = Array.isArray(children) ? children[0] : children
    const props = (child as { props?: { className?: string; children?: ReactNode } })?.props ?? {}
    const lang = /language-([\w+-]+)/.exec(props.className ?? '')?.[1]
    if (lang === GRAPH_LANG) return <GraphBlock code={nodeText(props.children)} />
    return <CodeBlock code={nodeText(props.children).replace(/\n$/, '')} lang={lang} />
  },
  code({ children, className }) {
    return <code className={`cc-inline-code ${className ?? ''}`}>{children}</code>
  },
  a({ href, title, children }) {
    const m = href ? BEAD_LINK.exec(href) : null
    if (m) return <BeadLink id={m[1]} tab={m[2] as LinkTab | undefined}>{children}</BeadLink>
    return <WebLink href={href} bead={title ? BEAD_CONTEXT.exec(title)?.[1] : undefined}>{children}</WebLink>
  },
  table({ children }) {
    return (
      <div className="cc-table-wrap">
        <table>{children}</table>
      </div>
    )
  },
}

const plugins = [remarkGfm]

// Markdown renders assistant text. While streaming, an unclosed code fence is
// closed temporarily so the partial block renders as code instead of
// flashing as paragraphs (avoids layout jumps when the closing ``` arrives).
// fadeAfter (the thread's reply): words are wrapped in spans and those past
// that many characters fade in. A reply passes it for its whole life
// (Infinity once settled) so the tree never changes shape when streaming ends;
// half-typed markdown is held back only while streaming.
export const Markdown = memo(function Markdown({ text, streaming, fadeAfter }: { text: string; streaming?: boolean; fadeAfter?: number }) {
  let src = text
  if (fadeAfter !== undefined) {
    if (streaming) src = completeMarkdown(src)
  } else if (streaming && (src.match(/^```/gm)?.length ?? 0) % 2 === 1) src += '\n```'
  return (
    <div className={`cc-md${streaming ? ' cc-streaming' : ''}`}>
      <StreamingContext.Provider value={!!streaming}>
        <ReactMarkdown remarkPlugins={plugins} rehypePlugins={fadeAfter !== undefined ? [[rehypeFadeWords, { settledChars: fadeAfter }]] : undefined} components={components} urlTransform={urlTransform}>
          {src}
        </ReactMarkdown>
      </StreamingContext.Provider>
    </div>
  )
})
