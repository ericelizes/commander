// `cmdr app shot` support: the renderer keeps a socket to cmdrd's
// /api/app/connect; when cmdrd asks for a capture it replies with a PNG and the
// visible text. In Electron the PNG is taken by the main process
// (webContents.capturePage, needs a `cmdr app install` build); everywhere else,
// and in an older installed app, the page renders its own DOM (html-to-image),
// so the live HMR renderer works without an install.

import { useEffect } from 'react'
import { shell } from './host'
import { useStore } from './store'

const MAX_TEXT = 30_000

/** visibleText is the page's rendered text, trimmed and truncated. */
export function visibleText(doc: Document = document): string {
  const text = (doc.body?.innerText ?? '').replace(/\n{3,}/g, '\n\n').trim()
  return text.length > MAX_TEXT ? `${text.slice(0, MAX_TEXT)}\n…(truncated)` : text
}

export interface CaptureReply {
  type: 'capture'
  id: string
  png?: string
  text?: string
  source?: string
  error?: string
}

export interface CaptureDeps {
  electronPng?: () => Promise<string>
  domPng: () => Promise<string>
  text: () => string
}

/** answerCapture builds the reply; the text survives a failed picture. */
export async function answerCapture(id: string, deps: CaptureDeps): Promise<CaptureReply> {
  const reply: CaptureReply = { type: 'capture', id }
  try {
    reply.text = deps.text()
  } catch (e) {
    reply.error = `text: ${String(e)}`
  }
  const errors: string[] = []
  if (deps.electronPng) {
    try {
      reply.png = await deps.electronPng()
      reply.source = 'electron-capturePage'
    } catch (e) {
      errors.push(`capturePage: ${String(e)}`)
    }
  }
  if (!reply.png) {
    try {
      reply.png = await deps.domPng()
      reply.source = 'dom'
    } catch (e) {
      errors.push(`dom render: ${String(e)}`)
    }
  }
  if (errors.length) reply.error = [reply.error, ...errors].filter(Boolean).join('; ')
  return reply
}

async function domPng(): Promise<string> {
  const { toPng } = await import('html-to-image')
  const url = await toPng(document.body, { pixelRatio: 1, cacheBust: false })
  return url.replace(/^data:image\/png;base64,/, '')
}

const realDeps = (): CaptureDeps => ({
  // An app installed before capturePage existed has a shell without it.
  electronPng: shell?.capturePage ? () => shell!.capturePage() : undefined,
  domPng,
  text: () => visibleText(),
})

/** useAppCapture keeps the capture socket open (reconnecting) while cmdrd is configured. */
export function useAppCapture(): void {
  const cfg = useStore((s) => s.config)
  useEffect(() => {
    if (!cfg || !cfg.token || cfg.demo) return
    let ws: WebSocket | null = null
    let timer: ReturnType<typeof setTimeout> | undefined
    let delay = 1000
    let stopped = false
    const open = () => {
      if (stopped) return
      const url = new URL('/api/app/connect', cfg.apiUrl)
      url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
      url.searchParams.set('token', cfg.token)
      url.searchParams.set('kind', shell?.capturePage ? 'electron' : 'web')
      const sock = new WebSocket(url)
      ws = sock
      sock.onopen = () => (delay = 1000)
      sock.onmessage = async (ev) => {
        try {
          const msg = JSON.parse(String(ev.data)) as { type: string; id: string }
          if (msg.type !== 'capture') return
          sock.send(JSON.stringify(await answerCapture(msg.id, realDeps())))
        } catch {
          // a malformed request is ignored; cmdrd times out
        }
      }
      sock.onclose = () => {
        if (stopped || ws !== sock) return
        timer = setTimeout(open, delay)
        delay = Math.min(delay * 2, 15_000)
      }
    }
    open()
    return () => {
      stopped = true
      clearTimeout(timer)
      ws?.close()
    }
  }, [cfg])
}
