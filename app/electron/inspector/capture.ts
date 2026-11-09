// A picture and the text of the tab the user is looking at, for the agent he is
// messaging. Only the SHOWN web view is ever read, and never one whose URL
// matches the user's block list (checked here again, whatever the renderer said).

import { isBlocked, originPath } from '../../src/shared/captureBlock'

export const MAX_WIDTH = 1280
export const MAX_TEXT = 20_000

export type PageCapture = { url: string; title: string; png: string; text: string }
export type CaptureResult = { ok: true; capture: PageCapture } | { ok: false; reason: 'blocked' | 'not-shown' | 'failed' }

type Img = { isEmpty(): boolean; getSize(): { width: number; height: number }; resize(o: { width: number }): Img; toPNG(): Buffer }
export type CaptureSource = {
  getURL(): string
  getTitle(): string
  capturePage(): Promise<Img>
  executeJavaScript(code: string): Promise<unknown>
}

// `expect` is the origin+path the user saw when he pressed Send; a tab that has
// moved elsewhere since is not captured.
export async function capturePage(wc: CaptureSource, shown: boolean, block: string[], expect?: string): Promise<CaptureResult> {
  if (!shown) return { ok: false, reason: 'not-shown' }
  const url = wc.getURL()
  if (isBlocked(url, block)) return { ok: false, reason: 'blocked' }
  if (expect !== undefined && originPath(url) !== expect) return { ok: false, reason: 'failed' }
  try {
    let img = await wc.capturePage()
    if (img.isEmpty()) return { ok: false, reason: 'failed' }
    if (img.getSize().width > MAX_WIDTH) img = img.resize({ width: MAX_WIDTH })
    const raw = await wc.executeJavaScript('document.body ? document.body.innerText : ""')
    const text = (typeof raw === 'string' ? raw : '').slice(0, MAX_TEXT)
    // The page may have navigated while we read it: a blocked one is refused, any
    // other move would label page B's content with page A's address.
    const after = wc.getURL()
    if (isBlocked(after, block)) return { ok: false, reason: 'blocked' }
    if (after !== url) return { ok: false, reason: 'failed' }
    // The address goes out as origin + path: a query or fragment may hold ids or tokens.
    return { ok: true, capture: { url: originPath(after), title: '', png: img.toPNG().toString('base64'), text } }
  } catch {
    return { ok: false, reason: 'failed' }
  }
}
