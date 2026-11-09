// Smooth streaming for the reply's text. Claude Code delivers text in bursts
// (a word, then a whole sentence), so the shown text is paced toward what has
// arrived; new words fade in once; half-typed markdown is held back so raw
// syntax never flashes.

import { useEffect, useRef, useState } from 'react'

// How much of each streaming text is already on screen, by its opening: when
// a landed step adds a part ahead of it, the text remounts, and what was
// visible must not reveal or fade again.
const shownByText = new Map<string, number>()
const textKey = (text: string) => text.slice(0, 120)

const MIN_CPS = 90 // the calm pace when the stream keeps up
const CATCH_UP_MS = 400 // a burst is drained over about this long

/** useSmoothText paces `text` toward its full length; it returns what to show and where fading starts. */
export function useSmoothText(text: string, streaming: boolean): { shown: string; settled: number; animating: boolean } {
  const [settled] = useState(() => shownByText.get(textKey(text)) ?? (streaming ? 0 : text.length))
  const [count, setCount] = useState(settled)
  const target = useRef(text)
  target.current = text
  const shown = useRef(count)
  shown.current = count

  useEffect(() => {
    if (shown.current >= text.length) return
    // A hidden window gets no animation frames; show it all.
    if (typeof document !== 'undefined' && document.hidden) {
      setCount(text.length)
      return
    }
    let raf = 0
    let last = performance.now()
    const tick = (now: number) => {
      const dt = Math.min(64, now - last)
      last = now
      const full = target.current
      const backlog = full.length - shown.current
      if (backlog <= 0) return
      const step = Math.max(1, Math.round(Math.max((MIN_CPS * dt) / 1000, (backlog * dt) / CATCH_UP_MS)))
      let next = Math.min(full.length, shown.current + step)
      // End on a word boundary when one is close, so a word never appears in pieces.
      const space = full.slice(next).search(/\s/)
      if (space >= 0 && space < 12) next += space
      else if (space < 0 && full.length - next < 12) next = full.length
      shown.current = next
      setCount(next)
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [text])

  const visible = Math.min(count, text.length)
  if (streaming || visible < text.length) shownByText.set(textKey(text), visible)
  return { shown: text.slice(0, visible), settled, animating: streaming || visible < text.length }
}

type HastNode = {
  type: string
  value?: string
  tagName?: string
  properties?: Record<string, unknown>
  children?: HastNode[]
}

const WORD = /\S+\s*|\s+/g

/**
 * rehypeFadeWords wraps each word in a span that fades in on mount. As text
 * grows React keeps the spans it already rendered and mounts only new ones,
 * so each word fades exactly once.
 */
export function rehypeFadeWords({ settledChars }: { settledChars: number }) {
  return (tree: HastNode) => {
    let offset = 0
    const walk = (node: HastNode) => {
      if (!node.children) return
      node.children = node.children.flatMap((child) => {
        // Code blocks appear whole; fading tokens reads as flicker.
        if (child.tagName === 'pre') return [child]
        if (child.type !== 'text' || !child.value) {
          walk(child)
          return [child]
        }
        return (child.value.match(WORD) ?? []).map((word) => {
          offset += word.length
          const settled = offset <= settledChars
          return { type: 'element', tagName: 'span', properties: settled ? {} : { className: ['ct-fade-word'] }, children: [{ type: 'text', value: word }] }
        })
      })
    }
    walk(tree)
  }
}

/**
 * completeMarkdown closes or holds back whatever is still open in markdown
 * cut off mid-token ("**Data", a half-typed [link](, a bare "#"), so only
 * well-formed markdown reaches the renderer. Streaming text only.
 */
export function completeMarkdown(text: string): string {
  let out = text
  const fences = out.match(/^```/gm)?.length ?? 0
  if (fences % 2 === 1) return `${out}\n\`\`\``
  out = out.replace(/(^|\n)[ \t]*(#{1,6}|[-*+]|\d+[.)]|>)[ \t]*$/, '')
  out = out.replace(/!?\[[^\]\n]*(\]\([^)\n]*)?$/, '')
  const lastBreak = out.lastIndexOf('\n')
  const head = out.slice(0, lastBreak + 1)
  let line = out.slice(lastBreak + 1)
  const ticks = line.match(/`/g)?.length ?? 0
  if (ticks % 2 === 1) line += '`'
  const plain = line.replace(/`[^`]*`/g, '')
  if ((plain.match(/\*\*/g)?.length ?? 0) % 2 === 1) line += '**'
  if ((plain.match(/~~/g)?.length ?? 0) % 2 === 1) line += '~~'
  line = line.replace(/(^|[^*_\w])[*_]$/, '$1')
  return head + line
}
