import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ClipboardEvent, type KeyboardEvent, type ReactNode } from 'react'
import type { SlashCommand } from './events'
import type { Attachment } from './transport'
import { isImage, readAttachment } from './attach'
import { Icon } from './icons'
import { isMac } from './context'

const draftKey = (role: string) => `cmdr.chat.draft.${role}`
// How long a send of yours may take to show in the queue and still be followed.
const SENT_FRESH_MS = 10_000

function loadDraft(role: string): string {
  try {
    return localStorage.getItem(draftKey(role)) ?? ''
  } catch {
    return ''
  }
}

function saveDraft(role: string, text: string) {
  try {
    if (text) localStorage.setItem(draftKey(role), text)
    else localStorage.removeItem(draftKey(role))
  } catch {
    /* storage unavailable (private mode): drafts just don't persist */
  }
}

export interface ComposerProps {
  role: string
  running: boolean
  waiting: string[] // sent mid-turn, not read by the agent yet
  commands: SlashCommand[]
  readonly?: string // reason shown instead of the input, e.g. "Owned by a terminal"
  onSend: (text: string, opts: { attachments?: Attachment[]; now?: boolean; noCapture?: boolean }) => void
  // The focused page that will ride along with this message (null = none).
  capture?: { host: string; title: string; blocked: boolean; off?: boolean } | null
  onShare?: (on: boolean) => void
  onStop: () => void
  placeholder?: string
  aside?: ReactNode // next to the send button (the app's waiting-mail count)
  onTyping?: () => void // every keystroke; the host throttles
}

export function Composer({ role, running, waiting, commands, readonly, onSend, onStop, placeholder, aside, onTyping, capture, onShare }: ComposerProps) {
  const [text, setText] = useState(() => loadDraft(role))
  const [files, setFiles] = useState<Attachment[]>([])
  const [problem, setProblem] = useState<string>()
  const picker = useRef<HTMLInputElement>(null)
  const [menu, setMenu] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)
  const [sel, setSel] = useState(0)
  const [dismissed, setDismissed] = useState<string | null>(null)
  const ta = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    setText(loadDraft(role))
    setFiles([])
    setProblem(undefined)
  }, [role])

  useEffect(() => {
    const t = setTimeout(() => saveDraft(role, text), 150)
    return () => clearTimeout(t)
  }, [role, text])

  const addFiles = async (picked: File[]) => {
    const read = await Promise.all(picked.map(readAttachment))
    setFiles((cur) => [...cur, ...read.filter((r): r is Attachment => typeof r !== 'string')])
    setProblem(read.find((r): r is string => typeof r === 'string'))
  }
  const addRef = useRef(addFiles)
  addRef.current = addFiles

  // Drop files anywhere on the chat: the section around the composer is the target.
  useEffect(() => {
    const zone = ta.current?.closest<HTMLElement>('.cmdr-chat')
    if (!zone) return
    let depth = 0
    const hasFiles = (e: DragEvent) => !!e.dataTransfer?.types.includes('Files')
    const over = (e: DragEvent) => hasFiles(e) && e.preventDefault()
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth++
      zone.classList.add('cc-dragging')
    }
    const leave = (e: DragEvent) => {
      if (hasFiles(e) && --depth <= 0) zone.classList.remove('cc-dragging')
    }
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth = 0
      zone.classList.remove('cc-dragging')
      void addRef.current([...(e.dataTransfer?.files ?? [])])
    }
    zone.addEventListener('dragover', over)
    zone.addEventListener('dragenter', enter)
    zone.addEventListener('dragleave', leave)
    zone.addEventListener('drop', drop)
    return () => {
      zone.removeEventListener('dragover', over)
      zone.removeEventListener('dragenter', enter)
      zone.removeEventListener('dragleave', leave)
      zone.removeEventListener('drop', drop)
      zone.classList.remove('cc-dragging')
    }
  }, [])

  useEffect(() => {
    if (!menu) return
    const away = (e: MouseEvent) => !menuRef.current?.contains(e.target as Node) && setMenu(false)
    const esc = (e: globalThis.KeyboardEvent) => e.key === 'Escape' && setMenu(false)
    document.addEventListener('mousedown', away)
    document.addEventListener('keydown', esc)
    return () => {
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', esc)
    }
  }, [menu])

  // Auto-grow up to ~12 lines, then scroll.
  useLayoutEffect(() => {
    const el = ta.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 260)}px`
  }, [text])

  // The queue is oldest first and bounded: a new send scrolls it into view,
  // unless you scrolled the queue up to read it and the send was not yours.
  // A send shows as a new last entry, even when the oldest was read in the
  // same update (the length stays the same).
  const queue = useRef<HTMLUListElement>(null)
  const newest = waiting.length ? waiting[waiting.length - 1] : undefined
  const queueSeen = useRef({ count: waiting.length, newest })
  const queueAtEnd = useRef(true)
  // When you last sent while the agent was busy; a send that never shows up
  // in the queue stops counting as yours after a while.
  const sentAt = useRef(0)
  useLayoutEffect(() => {
    const seen = queueSeen.current
    queueSeen.current = { count: waiting.length, newest }
    if (!waiting.length) {
      queueAtEnd.current = true
      sentAt.current = 0
      return
    }
    if (waiting.length <= seen.count && newest === seen.newest) return
    const mine = sentAt.current > 0 && Date.now() - sentAt.current < SENT_FRESH_MS
    sentAt.current = 0
    const el = queue.current
    if (el && (queueAtEnd.current || mine)) {
      el.scrollTop = el.scrollHeight
      queueAtEnd.current = true
    }
  }, [waiting.length, newest])

  const slash = /^\/([\w:-]*)$/.exec(text)
  const matches = useMemo(() => {
    if (!slash || dismissed === text) return []
    const q = slash[1].toLowerCase()
    return commands
      .filter((c) => c.name.toLowerCase().includes(q))
      .sort((a, b) => Number(!a.name.toLowerCase().startsWith(q)) - Number(!b.name.toLowerCase().startsWith(q)))
      .slice(0, 8)
  }, [slash?.[1], commands, dismissed, text]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => setSel(0), [matches.length])

  const submit = (now = false) => {
    const t = text.trim()
    if (!t && !files.length) return
    sentAt.current = running && !now ? Date.now() : 0
    onSend(t, { attachments: files.length ? files : undefined, now: now && running })
    setText('')
    setFiles([])
    setProblem(undefined)
    saveDraft(role, '')
  }

  const pick = (c: SlashCommand) => {
    setText(`/${c.name} `)
    ta.current?.focus()
  }

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return
    if (matches.length) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        setSel((i) => (i + (e.key === 'ArrowDown' ? 1 : matches.length - 1)) % matches.length)
        return
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && matches[sel] && `/${matches[sel].name}` !== text)) {
        e.preventDefault()
        pick(matches[sel])
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setDismissed(text)
        return
      }
    }
    const mod = isMac ? e.metaKey : e.ctrlKey
    if (e.key === 'Enter' && mod) {
      e.preventDefault()
      submit(true)
    } else if (e.key === 'Enter' && !e.shiftKey && !e.altKey) {
      e.preventDefault()
      submit(false)
    } else if (e.key === 'Escape' && running && !text) {
      e.preventDefault()
      onStop()
    }
  }

  const onPaste = async (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const pasted = [...e.clipboardData.files]
    if (!pasted.length) return
    e.preventDefault()
    await addFiles(pasted)
  }

  if (readonly) {
    return (
      <div className="cc-composer cc-composer-readonly" aria-disabled="true">
        <Icon name="terminal" size={14} />
        <span>{readonly}</span>
      </div>
    )
  }

  return (
    <div className="cc-composer-wrap">
      {matches.length > 0 && (
        <ul className="cc-slash" role="listbox" aria-label="Slash commands">
          {matches.map((c, i) => (
            <li
              key={c.name}
              role="option"
              aria-selected={i === sel}
              className={i === sel ? 'cc-on' : ''}
              onMouseDown={(e) => {
                e.preventDefault()
                pick(c)
              }}
              onMouseEnter={() => setSel(i)}
            >
              <span className="cc-slash-name">/{c.name}</span>
              {c.description && <span className="cc-slash-desc">{c.description}</span>}
            </li>
          ))}
        </ul>
      )}
      <div
        className={`cc-composer${running ? ' cc-composer-running' : ''}`}
        onMouseDown={(e) => {
          // A click anywhere on the bar types into it; its buttons keep their own clicks.
          if ((e.target as HTMLElement).closest('button, textarea, a, .cc-waiting')) return
          e.preventDefault()
          ta.current?.focus()
        }}
      >
        {waiting.length > 0 && (
          <ul
            ref={queue}
            className="cc-waiting"
            aria-label="Sent, not read yet"
            onScroll={(e) => {
              const el = e.currentTarget
              queueAtEnd.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 2
            }}
          >
            {waiting.map((t, i) => (
              <li key={i} title="Sent. The agent reads it when its current step finishes.">
                <span className="cc-waiting-label">Reads next</span>
                <span className="cc-waiting-text">{t}</span>
              </li>
            ))}
          </ul>
        )}
        {files.length > 0 && (
          <div className="cc-attachments">
            {files.map((f, i) => (
              <span key={i} className="cc-attachment" title={f.name}>
                {isImage(f.mediaType) ? <img src={f.dataUrl} alt="" /> : <Icon name="file" size={16} />}
                <span className="cc-attachment-name">{f.name}</span>
                <button type="button" aria-label={`Remove ${f.name}`} onClick={() => setFiles((cur) => cur.filter((_, j) => j !== i))}>
                  <Icon name="x" size={10} />
                </button>
              </span>
            ))}
          </div>
        )}
        {problem && (
          <div className="cc-attach-problem" role="alert">
            {problem}
          </div>
        )}
        <textarea
          ref={ta}
          className="cc-textarea"
          rows={1}
          value={text}
          placeholder={placeholder ?? (running ? 'Add to what it is doing…' : 'Message the agent…  (/ for commands)')}
          aria-label="Message"
          onChange={(e) => {
            setText(e.target.value)
            setDismissed(null)
            onTyping?.()
          }}
          onKeyDown={onKey}
          onPaste={onPaste}
        />
        <div className="cc-composer-bar">
          <div className="cc-plus" ref={menuRef}>
            <button type="button" className="cc-attach" aria-label="Add" aria-haspopup="menu" aria-expanded={menu} onClick={() => setMenu((v) => !v)}>
              <Icon name="plus" size={16} />
            </button>
            {menu && (
              <div className="cc-menu" role="menu">
                <button
                  type="button"
                  role="menuitem"
                  className="cc-menu-item"
                  onClick={() => {
                    setMenu(false)
                    picker.current?.click()
                  }}
                >
                  <Icon name="paperclip" size={15} />
                  <span>Attach files</span>
                </button>
                {onShare && (
                  <button
                    type="button"
                    role="menuitemcheckbox"
                    aria-checked={!!capture && !capture.off}
                    className="cc-menu-item"
                    disabled={!capture}
                    title={capture ? undefined : 'Open a web tab to share it'}
                    onClick={() => onShare(!!capture?.off)}
                  >
                    <Icon name="monitor" size={15} />
                    <span>Share focused window</span>
                    <span className={`cc-switch${capture && !capture.off ? ' cc-switch-on' : ''}`} aria-hidden />
                  </button>
                )}
              </div>
            )}
          </div>
          {capture && !capture.off && (
            <span className={`cc-share${capture.blocked ? '' : ' cc-share-on'}`} title={capture.blocked ? `${capture.host} is on your block list, so it is never shared` : 'Each message includes what your focused window shows'}>
              <Icon name="monitor" size={13} />
              <span className="cc-share-label">{capture.blocked ? `${capture.host} not shared` : 'Sharing your window'}</span>
              {onShare && (
                <button type="button" className="cc-share-x" aria-label="Stop sharing your window" onClick={() => onShare(false)}>
                  <Icon name="x" size={10} />
                </button>
              )}
            </span>
          )}
          <span className="cc-bar-gap" />
          {aside}
          <input
            ref={picker}
            type="file"
            multiple
            hidden
            aria-label="Choose files"
            onChange={(e) => {
              void addFiles([...(e.target.files ?? [])])
              e.target.value = ''
            }}
          />
          {/* One round button: Stop while the agent works and the box is empty, else Send (which queues mid-turn). */}
          {running && !text.trim() && !files.length ? (
            <button type="button" className="cc-send cc-stop" aria-label="Stop" title="Stop (Esc)" onClick={onStop}>
              <span className="cc-stop-square" />
            </button>
          ) : (
            <button type="button" className="cc-send" aria-label="Send" disabled={!text.trim() && !files.length} onClick={() => submit(false)}>
              <Icon name="send" size={15} />
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
