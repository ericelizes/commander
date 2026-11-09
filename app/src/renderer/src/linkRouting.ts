// Web links open in the app's own browser (the right panel), signed in there,
// wherever they are: a pitch, a card, a report. Cmd/Ctrl/Shift-click still
// opens the system browser on purpose.

import { hasConnections, openInPanel } from '../../features/inspector'

export function routeLinksToPanel(doc: Document = document): () => void {
  const onClick = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const a = (e.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null
    if (!a || !/^https?:/.test(a.href) || !hasConnections()) return
    e.preventDefault()
    openInPanel(a.href, a.textContent?.trim() || undefined)
  }
  doc.addEventListener('click', onClick)
  return () => doc.removeEventListener('click', onClick)
}
