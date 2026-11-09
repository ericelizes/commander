import { useEffect, useMemo, useRef } from 'react'
import { buildSrcdoc, handleBridgeRequest, parseRequest } from './bridge'
import type { BridgeHandlers } from './bridge'
import { copyText, resolveNeeds } from './logic'
import type { CommanderApi, DecisionOutcome, NeedsYouItem } from './types'
import './decisions.css'

export interface HtmlCardHostProps {
  // The page's HTML (an 'html' inspector tab, from `cmdr tab add --html <file>`).
  html: string
  // Accessible name for the frame, e.g. the tab title.
  title: string
  api: CommanderApi
  // Run the page's inline scripts (and expose window.cmdr). Off by default.
  allowScripts?: boolean
  // The flag this page may answer. Without it, submitDecision is refused.
  needs?: NeedsYouItem
  onDone?: (outcome: DecisionOutcome) => void
}

// Renders a Manager-authored page in a sandboxed iframe: opaque origin (no
// same-origin, so no cookies, storage or parent DOM), no top navigation, no
// popups, no forms, no network (CSP), scripts only when the tab asks. The page
// reaches the app only through the {submitDecision, copy, openUrl} bridge.
export function HtmlCardHost({ html, title, api, allowScripts = false, needs, onDone }: HtmlCardHostProps) {
  const frame = useRef<HTMLIFrameElement>(null)
  const srcdoc = useMemo(() => buildSrcdoc(html, allowScripts), [html, allowScripts])

  // Latest props for the message handler without re-subscribing.
  const latest = useRef({ api, needs, onDone })
  latest.current = { api, needs, onDone }

  useEffect(() => {
    if (!allowScripts) return
    const handlers: BridgeHandlers = {
      async submitDecision({ choice, text }) {
        const { api, needs, onDone } = latest.current
        if (!needs) throw new Error('this page is not attached to a decision')
        const options = needs.decision?.options ?? []
        if (choice && options.length > 0 && !options.some(o => o.label === choice)) {
          throw new Error(`"${choice}" is not one of the card's options`)
        }
        const res = await resolveNeeds(api, needs, 'decide', choice || 'answered', text)
        if (res.status === 'gone') {
          onDone?.({ kind: 'gone' })
          throw new Error('already answered elsewhere')
        }
        if (res.status === 'error') throw new Error(res.message)
        onDone?.({ kind: 'answered', action: 'decide', choice: choice || 'answered', note: text || undefined })
      },
      copy: text => copyText(latest.current.api, text),
      openUrl: url => {
        const { api } = latest.current
        if (api.openUrl) api.openUrl(url)
        else window.open(url, '_blank', 'noopener,noreferrer')
      },
    }
    async function onMessage(e: MessageEvent) {
      const win = frame.current?.contentWindow
      if (!win || e.source !== win) return
      const req = parseRequest(e.data)
      if (!req) return
      const reply = await handleBridgeRequest(req, handlers)
      // The frame's origin is opaque ("null"), so '*' is the only usable target;
      // the reply carries no data beyond ok/error.
      frame.current?.contentWindow?.postMessage(reply, '*')
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [allowScripts])

  return (
    <div className="cmdr-html">
      <iframe
        ref={frame}
        title={title}
        sandbox={allowScripts ? 'allow-scripts' : ''}
        srcDoc={srcdoc}
        referrerPolicy="no-referrer"
        allow=""
      />
    </div>
  )
}
