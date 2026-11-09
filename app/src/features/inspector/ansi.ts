// A small SGR parser for cmd tab output: basic/bright 16 colors, 256-color
// and truecolor foregrounds, bold, dim. Every other escape is dropped.

export type Span = { text: string; fg?: string; bold?: boolean; dim?: boolean }

const PALETTE = [
  '#45475a', '#f38ba8', '#a6e3a1', '#f9e2af', '#89b4fa', '#f5c2e7', '#94e2d5', '#bac2de',
  '#585b70', '#f38ba8', '#a6e3a1', '#f9e2af', '#89b4fa', '#f5c2e7', '#94e2d5', '#a6adc8',
]

function color256(n: number): string | undefined {
  if (n < 16) return PALETTE[n]
  if (n < 232) {
    const v = n - 16
    const c = (x: number) => (x === 0 ? 0 : 55 + x * 40)
    return `rgb(${c(Math.floor(v / 36))},${c(Math.floor(v / 6) % 6)},${c(v % 6)})`
  }
  if (n < 256) {
    const g = 8 + (n - 232) * 10
    return `rgb(${g},${g},${g})`
  }
  return undefined
}

// eslint-disable-next-line no-control-regex
const ESC_RE = /\x1b\[([0-9;]*)([A-Za-z])|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\r/g

export function parseAnsi(input: string): Span[] {
  const spans: Span[] = []
  let cur: Omit<Span, 'text'> = {}
  let last = 0
  const push = (text: string) => {
    if (!text) return
    const prev = spans[spans.length - 1]
    if (prev && prev.fg === cur.fg && prev.bold === cur.bold && prev.dim === cur.dim) prev.text += text
    else spans.push({ text, ...cur })
  }
  for (const m of input.matchAll(ESC_RE)) {
    push(input.slice(last, m.index))
    last = m.index! + m[0].length
    if (m[2] !== 'm') continue // only SGR changes style
    const codes = (m[1] || '0').split(';').map((x) => Number(x || 0))
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i]
      if (c === 0) cur = {}
      else if (c === 1) cur = { ...cur, bold: true }
      else if (c === 2) cur = { ...cur, dim: true }
      else if (c === 22) cur = { ...cur, bold: undefined, dim: undefined }
      else if (c >= 30 && c <= 37) cur = { ...cur, fg: PALETTE[c - 30] }
      else if (c >= 90 && c <= 97) cur = { ...cur, fg: PALETTE[c - 90 + 8] }
      else if (c === 39) cur = { ...cur, fg: undefined }
      else if (c === 38 && codes[i + 1] === 5) {
        cur = { ...cur, fg: color256(codes[i + 2]) }
        i += 2
      } else if (c === 38 && codes[i + 1] === 2) {
        cur = { ...cur, fg: `rgb(${codes[i + 2]},${codes[i + 3]},${codes[i + 4]})` }
        i += 4
      } else if (c === 48 && (codes[i + 1] === 5 || codes[i + 1] === 2)) {
        i += codes[i + 1] === 5 ? 2 : 4 // backgrounds are ignored
      }
    }
  }
  push(input.slice(last))
  // Drop undefined keys so spans compare cleanly.
  return spans.map((s) => Object.fromEntries(Object.entries(s).filter(([, v]) => v !== undefined)) as Span)
}
