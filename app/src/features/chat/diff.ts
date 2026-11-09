export interface DiffLine {
  kind: 'add' | 'del' | 'ctx' | 'hunk'
  text: string
  oldNo?: number
  newNo?: number
}

// lineDiff: a plain LCS line diff, fine for the small strings Edit carries.
// Very large inputs fall back to "all removed, all added".
export function lineDiff(oldText: string, newText: string, startLine = 1): DiffLine[] {
  const a = oldText === '' ? [] : oldText.split('\n')
  const b = newText === '' ? [] : newText.split('\n')
  if (a.length * b.length > 250_000) {
    return [...a.map((t, i) => ({ kind: 'del' as const, text: t, oldNo: startLine + i })), ...b.map((t, i) => ({ kind: 'add' as const, text: t, newNo: startLine + i }))]
  }
  const n = a.length
  const m = b.length
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) {
      out.push({ kind: 'ctx', text: a[i], oldNo: startLine + i, newNo: startLine + j })
      i++
      j++
    } else if (i < n && (j >= m || lcs[i + 1][j] >= lcs[i][j + 1])) {
      out.push({ kind: 'del', text: a[i], oldNo: startLine + i })
      i++
    } else {
      out.push({ kind: 'add', text: b[j], newNo: startLine + j })
      j++
    }
  }
  return out
}

interface Hunk {
  oldStart: number
  newStart: number
  lines: string[]
}

// fromStructuredPatch reads the `structuredPatch` that Claude's Edit/Write
// tool results carry (unified-diff hunks with +/-/space prefixes).
export function fromStructuredPatch(hunks: Hunk[]): DiffLine[] {
  const out: DiffLine[] = []
  for (const h of hunks) {
    out.push({ kind: 'hunk', text: `@@ -${h.oldStart} +${h.newStart} @@` })
    let o = h.oldStart
    let nn = h.newStart
    for (const l of h.lines) {
      const c = l[0]
      const text = l.slice(1)
      if (c === '+') out.push({ kind: 'add', text, newNo: nn++ })
      else if (c === '-') out.push({ kind: 'del', text, oldNo: o++ })
      else if (c !== '\\') out.push({ kind: 'ctx', text, oldNo: o++, newNo: nn++ })
    }
  }
  return out
}

export function diffStats(lines: DiffLine[]): { add: number; del: number } {
  let add = 0
  let del = 0
  for (const l of lines) {
    if (l.kind === 'add') add++
    else if (l.kind === 'del') del++
  }
  return { add, del }
}
