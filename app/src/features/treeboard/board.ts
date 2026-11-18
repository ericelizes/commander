// The tree board: the approved workstream map (poc/workstream-graph-v2.html),
// drawn from cmdrd's board tree. Sidebar header (progress, need-you count),
// the workstream tree, the main pane header (search, Filter, views), the
// Now / Next / steps-out table with the arrows between steps, and the right
// panel for a clicked workstream or step. Layout and drawing are worked out
// here; every word about state comes from the tree as cmdrd built it.

import type { Selection, Tree, TreeRow, TreeStep } from './types'
import { RULES_HTML, WORKFLOWS } from './content'

export interface BoardOptions {
  onSelect(sel: Selection | null): void
  onChat(): void
  linearUrl(id: string): string | null
  openUrl(url: string): void
  /** onPanel: the workstream panel was drawn; slot is where its Linear progress goes (null for a step). */
  onPanel?(slot: HTMLElement | null, row: TreeRow | null): void
}

export interface Board {
  /** linearSlot is the sidebar header's place for the Linear progress bar. */
  linearSlot: HTMLElement
  /** inspectorSlot is the popup's place for the original focus pane's tabs, under the details. */
  inspectorSlot: HTMLElement
  update(tree: Tree): void
  destroy(): void
  /** clear closes the panel, as Close or Escape do. */
  clear(): void
  /** setChatOpen: the chat dock is open, so the table makes room on the right. */
  setChatOpen(open: boolean): void
}

type Node = TreeStep
const OWNER: Record<string, string> = { you: 'You', agent: 'Implementer agent', review: 'Review agent', person: 'Another person', automatic: 'Automatic' }
// Automatic steps carry a clock instead of an owner's dot.
const CLOCK = '<svg class="clock" width="11" height="11" viewBox="0 0 12 12" aria-hidden="true"><circle cx="6" cy="6" r="5" fill="none" stroke="currentColor" stroke-width="1.4"/><path d="M6 3.2V6l1.9 1.3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>'
const esc = (s: unknown) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c] as string)
const cssEscape = (s: string) => (typeof CSS !== 'undefined' && CSS.escape ? CSS.escape(s) : s.replace(/["\\]/g, '\\$&'))
// Time on a step: "now" for its first minute, then 1m, 2m … h, d.
export const ago = (t: number, now = Date.now()) => {
  const m = Math.floor((now - t) / 60000)
  if (m < 1) return 'now'
  return m < 60 ? m + 'm' : m < 1440 ? Math.floor(m / 60) + 'h' : Math.floor(m / 1440) + 'd'
}
const LINEAR_SVG =
  '<svg width="11" height="11" viewBox="0 0 12 12" aria-hidden="true"><defs><clipPath id="tblc"><circle cx="6" cy="6" r="6"/></clipPath></defs><g clip-path="url(#tblc)"><circle cx="6" cy="6" r="6" fill="currentColor"/><path d="M-1 5.2L6.8 13M-1 8.4L3.6 13M0.6 1.6L10.4 11.4" stroke="var(--surface)" stroke-width="1.1"/></g></svg>'

// Progress is a ring beside the title; a finished workstream gets a tick.
function progHtml(pct: number, title?: string) {
  const p = Math.round(pct)
  const C = 2 * Math.PI * 7
  const mark =
    p >= 100
      ? '<circle cx="9" cy="9" r="8.5" fill="currentColor"/><path d="M5.2 9.3l2.6 2.6 5-5.4" fill="none" stroke="var(--surface)" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>'
      : `<circle class="trk" cx="9" cy="9" r="7" fill="none" stroke-width="3"/><circle cx="9" cy="9" r="7" fill="none" stroke="currentColor" stroke-width="3" stroke-dasharray="${((C * pct) / 100).toFixed(2)} ${C.toFixed(2)}" transform="rotate(-90 9 9)"/>`
  return `<span class="pr" title="${esc(title ?? p + '% done')}"><svg width="18" height="18" viewBox="0 0 18 18" aria-hidden="true">${mark}</svg><span class="pct">${p}%</span></span>`
}

const SHELL = `
<div class="page">
  <header class="appbar">
    <div class="ab-side">
      <div class="tb-linear" data-tb="linear"></div>
      <button type="button" class="needbtn" data-tb="need" aria-pressed="false" title="Show only what needs you"><i></i><span><span data-tb="needN"></span> need you</span></button>
    </div>
    <div class="ab-main" role="search">
      <label class="search"><svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><circle cx="7" cy="7" r="5" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M11 11l3.5 3.5" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg><input data-tb="q" type="search" placeholder="Search workstreams and steps" autocomplete="off" spellcheck="false"><kbd>/</kbd></label>
      <div class="fwrap">
        <button type="button" class="fbtn" data-tb="fBtn" aria-haspopup="true" aria-expanded="false"><svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true"><path d="M2 3.5h12M4.5 8h7M7 12.5h2" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" fill="none"/></svg>Filter</button>
        <div class="fmenu" data-tb="fMenu" hidden>
          <div class="fsec"><div class="fh">Workstreams</div>
            <div class="seg" role="radiogroup" aria-label="Which workstreams"><button type="button" role="radio" data-tb="segAll" aria-checked="true">All</button><button type="button" role="radio" data-tb="segYou" aria-checked="false"><i></i>Needs you</button></div>
          </div>
          <div class="fsec"><div class="fh">Show</div>
            <label class="fopt"><input type="checkbox" data-tb="hideDone" checked>Hide done workstreams</label>
          </div>
          <div class="ffoot"><button type="button" class="fclear" data-tb="fClear" hidden>Clear filters</button></div>
        </div>
      </div>
      <span class="fcount" data-tb="fCount"></span>
      <nav class="tabs" aria-label="Views"><a href="#" data-view="graph">Graph</a><a href="#" data-view="flows">Workflows</a><a href="#" data-view="rules">Rules</a></nav>
      <button type="button" class="abtn" data-tb="chat" title="Message the Commander">Message Commander</button>
    </div>
  </header>
  <div class="view">
    <div class="graph"><div class="canvas" data-tb="canvas"></div></div>
    <section class="rules" data-tb="rulesView" hidden>${RULES_HTML}</section>
    <section class="rules" data-tb="flowsView" hidden>
      <p>A workflow is a set of steps that fit together in a set shape. Commander stamps one out when a workstream needs that shape, so the same kind of work always has the same steps, owners and reviews. Each step has one owner, and a review always sits before your approval.</p>
      <div data-tb="flowList" class="flows"></div>
    </section>
  </div>
</div>
<div class="sheet" data-tb="sheet" hidden><div class="in"><div class="tb-panel" data-tb="panel"></div><div class="tb-inspector" data-tb="inspector"></div></div></div>`

// mountBoard draws the board as the prototype does: one header bar split at the
// tree's width, and one grid in which each workstream is one row across its tree
// cell and its board cells, so the two always line up.
export function mountBoard(root: HTMLElement, opts: BoardOptions): Board {
  root.innerHTML = SHELL
  const $ = <T extends HTMLElement = HTMLElement>(k: string) => root.querySelector(`[data-tb="${k}"]`) as T
  const canvasEl = $('canvas'),
    sheetEl = $('sheet'),
    panelEl = $('panel'),
    qEl = $<HTMLInputElement>('q'),
    fMenu = $('fMenu'),
    fBtn = $('fBtn'),
    hdBox = $<HTMLInputElement>('hideDone')

  // ---------- model (rebuilt on every tree) ----------
  let tree: Tree = { version: 0, asof: 0, done: 0, total: 0, needs_you: 0, rows: [], steps: [], edges: [] }
  let rowsById: Record<string, TreeRow & { kids: TreeRow[] }> = {}
  let kidsOf: Record<string, TreeRow[]> = {}
  let ownSteps: Record<string, Node[]> = {}
  let byId: Record<string, Node> = {}
  let blockers: Record<string, string[]> = {}
  let unblocks: Record<string, string[]> = {}
  let edges: [string, string][] = []
  const collapsed = new Set<string>()
  const knownRows = new Set<string>()
  const stepsCache: Record<string, Node[]> = {}

  const open = (n: Node) => n.state !== 'done'
  const kids = (id: string) => kidsOf[id] ?? []
  const under = (id: string): string[] => [id, ...kids(id).flatMap((k) => under(k.id))]
  const stepsUnder = (id: string) => stepsCache[id] || (stepsCache[id] = under(id).flatMap((r) => ownSteps[r] ?? []))
  const isDone = (id: string) => !!rowsById[id]?.done
  const label = (id: string) => rowsById[id]?.label ?? ''
  const closure = (id: string, map: Record<string, string[]>) => {
    const out = new Set<string>(),
      st = [...(map[id] ?? [])]
    while (st.length) {
      const x = st.pop() as string
      if (out.has(x)) continue
      out.add(x)
      st.push(...(map[x] ?? []))
    }
    return out
  }

  function setTree(t: Tree) {
    tree = t
    rowsById = {}
    kidsOf = {}
    ownSteps = {}
    for (const k of Object.keys(stepsCache)) delete stepsCache[k]
    t.rows.forEach((r) => {
      rowsById[r.id] = { ...r, kids: [] }
      kidsOf[r.id] = []
      ownSteps[r.id] = []
    })
    t.rows.forEach((r) => {
      if (r.parent && kidsOf[r.parent]) kidsOf[r.parent].push(r)
    })
    t.steps.forEach((n) => (ownSteps[n.row] ??= []).push(n))
    byId = Object.fromEntries(t.steps.map((n) => [n.id, n]))
    blockers = {}
    unblocks = {}
    t.steps.forEach((n) => {
      blockers[n.id] = []
      unblocks[n.id] = []
    })
    const all = t.edges.filter(([a, b]) => byId[a] && byId[b])
    all.forEach(([a, b]) => {
      blockers[b].push(a)
      unblocks[a].push(b)
    })
    // transitive reduction for drawing: drop a→c when a→…→c already exists
    const reach = (from: string, to: string) => {
      const seen = new Set<string>(),
        st = unblocks[from].filter((x) => x !== to)
      while (st.length) {
        const x = st.pop() as string
        if (x === to) return true
        if (seen.has(x)) continue
        seen.add(x)
        st.push(...unblocks[x])
      }
      return false
    }
    edges = all.filter(([a, b]) => !reach(a, b))
    // finished workstreams start collapsed; a row seen before keeps the user's choice
    t.rows.forEach((r) => {
      if (knownRows.has(r.id)) return
      knownRows.add(r.id)
      if (kidsOf[r.id].length && r.done) collapsed.add(r.id)
    })
    if (sel && !byId[sel]) sel = null
    if (wsSel && !rowsById[wsSel]) wsSel = null
  }

  // ---------- timers: only where the data says when the step began ----------
  const timerHtml = (n: Node) => (n.since && n.since > 0 ? `<span class="tm" data-since="${n.since * 1000}" title="Time on this step">${ago(n.since * 1000)}</span>` : '')
  const timer = setInterval(
    () =>
      root.querySelectorAll<HTMLElement>('.tm').forEach((el) => {
        const t = ago(+(el.dataset.since ?? 0))
        if (t === el.textContent) return
        el.classList.add('swap')
        setTimeout(() => {
          el.textContent = t
          el.classList.remove('swap')
        }, 700)
      }),
    30000, // every 30 s, so "now" turns to 1m on time
  )

  // ---------- search + filters ----------
  let fq = '',
    fHideDone = true
  const fOwners = new Set<string>()
  const filtersOn = () => !!fq || fOwners.size > 0
  function rowSelfMatch(r: TreeRow) {
    const own = ownSteps[r.id] ?? []
    if (fq && !(r.title.toLowerCase().includes(fq) || r.label.toLowerCase().includes(fq) || (r.tix ?? []).some((t) => t.toLowerCase().includes(fq)) || own.some((n) => n.title.toLowerCase().includes(fq)))) return false
    if (fOwners.size && !own.some((n) => n.state === 'active' && fOwners.has(n.type))) return false
    return !!(fq || fOwners.size)
  }
  function ancTitleMatch(r: TreeRow) {
    if (!fq) return false
    let p = r.parent
    while (p) {
      const pr = rowsById[p]
      if (pr && pr.title.toLowerCase().includes(fq) && !fOwners.size) return true
      p = pr?.parent
    }
    return false
  }
  function keepRow(r: TreeRow): boolean {
    if (fHideDone && r.done) return false
    if (!fq && !fOwners.size) return true
    return rowSelfMatch(r) || ancTitleMatch(r) || kids(r.id).some((k) => keepRow(k))
  }
  const cardDim = (n: Node) => (fq && !n.title.toLowerCase().includes(fq)) || (fOwners.size > 0 && !fOwners.has(n.type))
  const cardHit = (n: Node) => !!fq && n.title.toLowerCase().includes(fq)

  // ---------- cards ----------
  function cardEl(n: Node) {
    // A div, not a button, so the card's text can be selected and copied; it still opens on click and Enter.
    const el = document.createElement('div')
    el.setAttribute('role', 'button')
    el.tabIndex = 0
    el.dataset.id = n.id
    const live = n.state === 'active' && (n.type === 'agent' || n.type === 'review') && !!n.run
    const norun = n.state === 'active' && (n.type === 'agent' || n.type === 'review') && !n.run && !n.asleep
    el.className = `card t-${n.type} s-${n.state}` + (filtersOn() && cardDim(n) ? ' fdim' : '') + (cardHit(n) ? ' hit' : '') + (live ? ' run' : n.asleep ? ' asleep' : norun ? ' norun' : '')
    el.innerHTML =
      `<span class="own">${n.type === 'automatic' ? CLOCK : '<i></i>'}${esc(n.own)}${n.state === 'active' ? timerHtml(n) : ''}</span><span class="tt">${esc(n.title)}</span>` + (n.wt ? `<span class="wt">${esc(n.wt)}</span>` : '')
    el.title = n.title
    return el
  }
  const linHtml = (id: string) => {
    const url = opts.linearUrl(id)
    return url
      ? `<a class="lin" href="${esc(url)}" data-url="${esc(url)}" target="_blank" rel="noopener" title="Open ${esc(id)} in Linear">${LINEAR_SVG}${esc(id)}</a>`
      : `<span class="lin">${LINEAR_SVG}${esc(id)}</span>`
  }

  // ---------- layout ----------
  let els: Record<string, HTMLElement> = {}
  let sel: string | null = null,
    wsSel: string | null = null,
    lit: Set<string> | null = null,
    shown: (TreeRow & { kids: TreeRow[] })[] = [],
    view: 'graph' | 'flows' | 'rules' = 'graph',
    chatOpen = false

  // The tree's title cell also says how many workstreams are done and how fresh the board is.
  function treeHead() {
    const at = tree.asof ? new Date(tree.asof * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }) : ''
    const fresh = at ? `<span class="hd-at"> · as of ${esc(at)}</span>` : ''
    return `<span class="hd-t">Workstreams</span><span class="hd-meta" data-tb="sub">${tree.done} of ${tree.total} done${fresh}</span>`
  }

  function render() {
    $('needN').textContent = String(tree.needs_you)
    const frag = document.createDocumentFragment()
    canvasEl.innerHTML = ''
    els = {}
    const R = Math.max(1, ...tree.steps.filter(open).map((n) => n.col))
    canvasEl.style.gridTemplateColumns = `var(--treew) var(--noww) repeat(${R}, var(--colw))`
    canvasEl.style.gridTemplateRows = 'auto'
    canvasEl.style.gridAutoRows = 'minmax(64px, auto)'
    // a search or an owner filter looks inside collapsed workstreams too
    const seek = filtersOn()
    const anc = (r: TreeRow) => {
      if (seek) return false
      let p = r.parent
      while (p) {
        if (collapsed.has(p)) return true
        p = rowsById[p]?.parent
      }
      return false
    }
    const rowsShown = tree.rows.filter((r) => !anc(r) && keepRow(r)).map((r) => rowsById[r.id])
    const fOn = filtersOn()
    $('fClear').hidden = !fOn
    const youOnly = fOwners.has('you')
    $('need').setAttribute('aria-pressed', String(youOnly))
    $('segAll').setAttribute('aria-checked', String(!youOnly))
    $('segYou').setAttribute('aria-checked', String(youOnly))
    fBtn.classList.toggle('on', youOnly)
    const leaves = tree.rows.filter((r) => !kids(r.id).length)
    $('fCount').textContent = fOn ? `${rowsShown.filter((r) => !kids(r.id).length).length} of ${leaves.length} workstreams` : ''
    const nowCol = 2,
      lastRow = rowsShown.length + 2
    const add = (cls: string, row: number | string, col: number | string, html?: string, rid?: string) => {
      const d = document.createElement('div')
      d.className = cls
      if (rid) d.dataset.row = rid
      d.style.gridRow = String(row)
      d.style.gridColumn = String(col)
      if (html) d.innerHTML = html
      frag.appendChild(d)
      return d
    }
    add('band', `1 / ${lastRow}`, nowCol)
    add('tree hd', 1, 1, treeHead())
    for (let c = 0; c <= R; c++) add('hd' + (c ? '' : ' now'), 1, nowCol + c, c === 0 ? 'Now' : c === 1 ? 'Next' : c + ' steps out')
    rowsShown.forEach((row, i) => {
      const gr = i + 2,
        rv = ownSteps[row.id] ?? [],
        nk = kids(row.id).length
      add('rowline', gr, '1 / -1')
      const shut = collapsed.has(row.id) && !seek
      const chev = nk
        ? `<button type="button" class="chev${shut ? '' : ' open'}" data-toggle="${esc(row.id)}" aria-label="${shut ? 'Expand' : 'Collapse'}" aria-expanded="${!shut}">${shut ? '▸' : '▾'}</button>`
        : ''
      add(
        'tree' + (row.depth ? '' : ' root') + (nk ? ' parent' : '') + (row.done ? ' isdone' : ''),
        gr,
        1,
        `<div class="tbody ringed" style="padding-left:calc(var(--gut) + ${row.depth} * var(--step))"><div class="th"><span class="tk"><span class="tl">${esc(row.label)}</span></span><span class="tn">${esc(row.title)}${(row.tix ?? []).map(linHtml).join('')}${chev}</span></div>` +
          progHtml(row.pct, `${row.pct}% done: ${row.of}`) +
          '</div>',
        row.id,
      ).dataset.ws = row.id
      const pill = nk && shut && row.needs_you ? `<p class="note you">${row.needs_you} need${row.needs_you === 1 ? 's' : ''} you</p>` : ''
      // open steps, by the column cmdrd placed them in
      const cols: Record<number, Node[]> = {}
      rv.filter(open).forEach((n) => (cols[n.col] = cols[n.col] || []).push(n))
      Object.entries(cols).forEach(([r, ns]) => {
        const c = add('cell', gr, nowCol + +r)
        ns.forEach((n) => c.appendChild((els[n.id] = cardEl(n))))
      })
      if (!cols[0]) {
        if (row.done) {
          add('donerow', gr, `${nowCol} / -1`)
          add('cell', gr, nowCol, `<p class="note done">${esc(row.now ?? '✓ Completed')}</p>`, row.id)
        } else
          add(
            'cell sumcell',
            gr,
            nowCol,
            nk
              ? `<div class="sum" title="${esc(row.line.text)}"><span class="sumlab">Summary of ${esc(row.label)}</span><p class="note sum">${esc(row.line.text)}</p>${pill}</div>`
              : `<p class="note">${esc(row.now ?? '')}</p>`,
            row.id,
          )
      }
    })
    if (!rowsShown.length) add('fempty', 2, '1 / -1', 'No workstreams match these filters.')
    canvasEl.appendChild(frag)
    shown = rowsShown
    drawTree()
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
    svg.setAttribute('class', 'edges')
    svg.dataset.tb = 'edges'
    canvasEl.appendChild(svg)
    drawEdges()
    applySel()
  }

  // Tree lines, placed from measurements: read every number's box first, then write the lines.
  function drawTree() {
    const S = parseFloat(getComputedStyle(root).getPropertyValue('--step')) || 28,
      W = 1.5,
      cells: Record<string, HTMLElement> = {},
      m: Record<string, { h: number; left: number; cy: number; under: number; cx: number }> = {},
      stacked = S < 20
    canvasEl.querySelectorAll<HTMLElement>('.tree[data-ws]').forEach((t) => (cells[t.dataset.ws as string] = t))
    shown.forEach((r) => {
      const t = cells[r.id],
        tl = t.querySelector('.tl') as HTMLElement,
        a = t.getBoundingClientRect(),
        n = tl.getBoundingClientRect(),
        left = n.left - a.left,
        cy = n.top + n.height / 2 - a.top
      m[r.id] = { h: a.height, left, cy, under: cy + (parseFloat(getComputedStyle(tl).fontSize) || 13) * 0.364 + 4, cx: stacked ? left - 5 : Math.min(left + n.width / 2, left + S - 11) }
    })
    const later = (i: number, d: number) => {
      for (let j = i + 1; j < shown.length; j++) {
        if (shown[j].depth === d) return true
        if (shown[j].depth < d) return false
      }
      return false
    }
    const v = (x: number, y1: number, y2: number) => `<i class="ln v" style="left:${(x - W / 2).toFixed(2)}px;top:${y1.toFixed(2)}px;height:${Math.max(0, y2 - y1).toFixed(2)}px"></i>`
    shown.forEach((r, i) => {
      const t = cells[r.id],
        me = m[r.id]
      let out = '',
        k = 0
      for (let p = r.parent; p && m[p]; p = rowsById[p]?.parent, k++) {
        const x = m[p].cx,
          more = later(i, rowsById[p].depth + 1)
        if (k === 0) {
          // The last child gets a rounded elbow (as in the prototype); a child with siblings below keeps a straight T.
          const R = 8
          if (more) out += v(x, 0, me.h) + `<i class="ln h" data-elbow style="left:${x.toFixed(2)}px;top:${(me.cy - W / 2).toFixed(2)}px;width:${(me.left - 6 - x).toFixed(2)}px"></i>`
          else
            out +=
              v(x, 0, me.cy - R) +
              `<i class="ln el" data-elbow style="left:${(x - W / 2).toFixed(2)}px;top:${(me.cy - R).toFixed(2)}px;width:${(me.left - 6 - x + W / 2).toFixed(2)}px;height:${(R + W / 2).toFixed(2)}px"></i>`
        }
        else if (more) out += v(x, 0, me.h)
      }
      if (shown[i + 1] && shown[i + 1].parent === r.id)
        out += `<i class="ln v" data-trunk style="left:${(me.cx - W / 2).toFixed(2)}px;top:${me.under.toFixed(2)}px;height:${(me.h - me.under).toFixed(2)}px"></i>`
      t.querySelectorAll('.ln').forEach((e) => e.remove())
      t.insertAdjacentHTML('beforeend', out)
    })
  }

  function drawEdges() {
    const svg = canvasEl.querySelector('svg.edges')
    if (!svg) return
    const base = canvasEl.getBoundingClientRect()
    svg.setAttribute('width', String(canvasEl.scrollWidth))
    svg.setAttribute('height', String(canvasEl.scrollHeight))
    let out = ''
    const lanes: Record<number, string[]> = {},
      rects: Record<string, DOMRect> = {},
      rect = (id: string) => rects[id] || (rects[id] = els[id].getBoundingClientRect())
    edges.forEach(([a, b]) => {
      if (!els[a] || !els[b]) return
      const ra = rect(a),
        rb = rect(b)
      const x1 = ra.right - base.left,
        y1 = ra.top + ra.height / 2 - base.top,
        x2 = rb.left - base.left - 6,
        y2 = rb.top + rb.height / 2 - base.top
      const dx = Math.max(18, (x2 - x1) / 2),
        A = byId[a],
        B = byId[b]
      // between workstreams: out of the card, down or up the gutter just left of the target's column, then into the target
      const cross = A.row !== B.row,
        col = lanes[Math.round(x2)] || (lanes[Math.round(x2)] = [])
      if (cross && !col.includes(a)) col.push(a)
      const xm = x2 - 11.5 - 4 * (col.indexOf(a) % 3),
        r = Math.min(3, Math.abs(y2 - y1) / 2),
        s = y2 > y1 ? 1 : -1
      const d =
        cross && xm - r >= x1
          ? `M${x1} ${y1}H${xm - r}Q${xm} ${y1} ${xm} ${y1 + s * r}V${y2 - s * r}Q${xm} ${y2} ${xm + r} ${y2}H${x2}`
          : `M${x1} ${y1}C${x1 + dx} ${y1} ${x2 - dx} ${y2} ${x2} ${y2}`
      out +=
        `<g data-a="${esc(a)}" data-b="${esc(b)}" class="${cross ? 'x' : ''}${!open(A) && !open(B) ? ' dn' : ''}${lit && lit.has(a) && lit.has(b) ? ' on' : ''}">` +
        `<path class="ln" d="${d}"/><path class="hd2" d="M${x2 + 6} ${y2}l-8 -4.5v9z"/></g>`
    })
    svg.innerHTML = out
  }

  // ---------- selection ----------
  function selection(): Selection | null {
    if (sel && byId[sel]) {
      const n = byId[sel],
        r = rowsById[n.row]
      return { kind: 'step', id: n.id, key: r?.key ?? '', label: r?.label ?? '', title: n.title, row: r?.title }
    }
    if (wsSel && rowsById[wsSel]) {
      const r = rowsById[wsSel]
      return { kind: 'workstream', id: r.id, key: r.key, label: r.label, title: r.title }
    }
    return null
  }
  let lastSel = ''
  function applySel() {
    const svg = canvasEl.querySelector('svg.edges')
    canvasEl.querySelectorAll('.on,.sel,.wsel').forEach((e) => e.classList.remove('on', 'sel', 'wsel'))
    const any = !!(sel || wsSel)
    canvasEl.classList.toggle('selecting', any)
    svg?.classList.toggle('selecting', any)
    root.classList.toggle('sheet-open', any || chatOpen)
    sheetEl.hidden = !any
    lit = null
    const s = selection()
    const k = s ? `${s.kind}:${s.id}` : ''
    if (k !== lastSel) {
      lastSel = k
      opts.onSelect(s)
    }
    if (!any) {
      panelEl.innerHTML = ''
      return
    }
    // a step lights its whole chain; a workstream lights its steps and everything they unblock, in any workstream
    let chain: Set<string>, rows: Set<string>
    if (sel) {
      chain = new Set([sel, ...closure(sel, blockers), ...closure(sel, unblocks)])
      rows = new Set()
    } else {
      const own = stepsUnder(wsSel as string).filter(open)
      chain = new Set(own.map((n) => n.id))
      own.forEach((n) => closure(n.id, unblocks).forEach((x) => chain.add(x)))
      rows = new Set(under(wsSel as string))
    }
    lit = chain
    chain.forEach((id) => {
      if (els[id]) {
        els[id].classList.add('on')
        rows.add(byId[id].row)
      }
    })
    if (sel && els[sel]) els[sel].classList.add('sel')
    ;[...rows].forEach((r) => {
      for (let p = rowsById[r]?.parent; p; p = rowsById[p]?.parent) rows.add(p)
    })
    canvasEl.querySelectorAll<HTMLElement>('[data-row]').forEach((e) => e.classList.toggle('on', rows.has(e.dataset.row as string)))
    svg?.querySelectorAll<SVGGElement>('g').forEach((g) => g.classList.toggle('on', chain.has(g.dataset.a as string) && chain.has(g.dataset.b as string)))
    if (!sel) {
      const t = canvasEl.querySelector(`.tree[data-ws="${cssEscape(wsSel as string)}"]`)
      if (t) t.classList.add('wsel')
      panelEl.innerHTML = wsPanel(rowsById[wsSel as string])
      opts.onPanel?.(panelEl.querySelector<HTMLElement>('[data-tb="wslinear"]'), rowsById[wsSel as string])
      return
    }
    panelEl.innerHTML = stepPanel(byId[sel])
    opts.onPanel?.(null, null)
  }

  // ---------- right panel ----------
  const panelCard = (n: Node) => {
    const el = cardEl(n)
    el.removeAttribute('data-id')
    el.dataset.go = n.id
    el.classList.remove('fdim', 'hit')
    return el.outerHTML
  }
  const askOf = (n: Node) => (n.type === 'you' && n.state === 'active' ? (n.ask ?? '') : '')
  const stepRow = (n: Node, showWs?: boolean) =>
    `<button class="prow" data-go="${esc(n.id)}"><i class="d-${n.type}"></i><span class="pt">${showWs ? `<span class="pw">${esc(label(n.row))}</span> ` : ''}${esc(n.title)}</span><span class="ps">${n.state === 'done' ? 'done' : esc(n.owner)}</span></button>`
  const wsRow = (k: TreeRow) =>
    `<button class="prow ws" data-wsgo="${esc(k.id)}">${progHtml(k.pct)}<span class="pt"><span class="pw">${esc(k.label)}</span> ${esc(k.title)}</span><span class="ps${/need/i.test(k.state) ? ' you' : ''}">${esc(k.state)}</span></button>`
  const moreBox = (n: number, word: string, html: string) => (n ? `<details class="more"><summary>${n} ${word}</summary><div class="plist">${html}</div></details>` : '')
  const leavesUnder = (id: string) => under(id).map((r) => rowsById[r]).filter((r) => r && !kids(r.id).length)
  // Acceptance criteria: what must be true for the workstream to count as done, as written on its bead.
  function acBlock(row: TreeRow, forStep: boolean) {
    const list = row.ac ?? []
    if (!list.length) return ''
    return `<div class="sec"><b>Acceptance criteria${forStep ? ' for ' + esc(row.label) : ''}</b><ul class="ac${row.done ? ' met' : ''}">${list.map((c) => `<li><span>${esc(c)}</span></li>`).join('')}</ul></div>`
  }
  function wsPanel(row: TreeRow) {
    const nk = kids(row.id),
      parent = nk.length > 0,
      done = row.done
    // live steps and what follows, grouped by the workstream they sit in (no repeated prefix)
    const groups = leavesUnder(row.id).map((l) => ({
      l,
      now: (ownSteps[l.id] ?? []).filter((n) => n.state === 'active'),
      next: (ownSteps[l.id] ?? []).filter((n) => n.state === 'notstarted').sort((a, b) => a.col - b.col),
    }))
    const many = groups.filter((g) => g.now.length || g.next.length).length > 1
    const head = (l: TreeRow) => (parent && many ? `<button class="pgroup" data-wsgo="${esc(l.id)}"><span class="pw">${esc(l.label)}</span> ${esc(l.title)}</button>` : '')
    const nowHtml = groups
      .filter((g) => g.now.length)
      .map((g) => head(g.l) + g.now.map((n) => panelCard(n) + (askOf(n) ? `<p class="ask">${esc(askOf(n))}</p>` : '')).join(''))
      .join('')
    const nextHtml = groups
      .filter((g) => g.next.length)
      .map(
        (g) =>
          head(g.l) +
          `<div class="plist">${g.next
            .slice(0, 3)
            .map((n) => stepRow(n))
            .join('')}</div>` +
          moreBox(
            Math.max(0, g.next.length - 3),
            'more',
            g.next
              .slice(3)
              .map((n) => stepRow(n))
              .join(''),
          ),
      )
      .join('')
    const openKids = nk.filter((k) => !isDone(k.id)),
      doneKids = nk.filter((k) => isDone(k.id)),
      doneSteps = parent ? [] : stepsUnder(row.id).filter((n) => !open(n))
    const idle = !done && !nowHtml ? `<p class="none">${esc(parent ? 'Nothing is active right now.' : (row.status[0]?.text ?? ''))}</p>` : ''
    return (
      `<div class="top"><div class="phead"><span class="kick">Workstream ${esc(row.label)}</span>${progHtml(row.pct, `${row.pct}% done: ${row.of}`)}${(row.tix ?? []).map(linHtml).join('')}</div><button class="x" data-tb="close">Close</button></div>` +
      `<h2>${esc(row.title)}</h2>` +
      (done ? `<p class="lead done">${esc(row.now ?? '✓ Completed')}</p>` : row.sum ? `<p class="lead">${esc(row.sum)}</p>` : '') +
      acBlock(row, false) +
      '<div class="tb-linear-ws" data-tb="wslinear"></div>' +
      (nowHtml || idle ? `<div class="sec"><b>Now</b>${nowHtml}${idle}</div>` : '') +
      (nextHtml ? `<div class="sec"><b>Next</b>${nextHtml}</div>` : '') +
      (parent
        ? `<div class="sec"><b>Workstreams inside</b>${openKids.length ? `<div class="plist">${openKids.map(wsRow).join('')}</div>` : ''}${moreBox(doneKids.length, 'done', doneKids.map(wsRow).join(''))}</div>`
        : '') +
      (doneSteps.length ? moreBox(doneSteps.length, doneSteps.length === 1 ? 'step done' : 'steps done', doneSteps.map((n) => stepRow(n)).join('')) : '')
    )
  }
  function stepPanel(n: Node) {
    const row = rowsById[n.row],
      bl = blockers[n.id].map((b) => byId[b]),
      ub = unblocks[n.id].map((b) => byId[b]),
      openBl = bl.filter(open),
      doneBl = bl.filter((b) => !open(b))
    const openDown = [...closure(n.id, unblocks)].filter((x) => open(byId[x])).length,
      other = (x: Node) => x.row !== n.row
    const status = n.state === 'done' ? '' : n.state === 'notstarted' ? (openBl.length ? 'Starts when the steps it waits on are done.' : 'Not started.') : askOf(n)
    return (
      `<div class="top"><div class="phead"><span class="kick">Step in <button class="wslink" data-wsgo="${esc(row.id)}">${esc(row.label)} ${esc(row.title)}</button></span></div><button class="x" data-tb="close">Close</button></div>` +
      panelCard(n).replace('data-go=', 'data-self=') +
      (status ? `<p class="ask">${esc(status)}</p>` : '') +
      (n.ac ? `<div class="sec"><b>Done when</b><p>${esc(n.ac)}</p></div>` : '') +
      acBlock(row, true) +
      (openBl.length || doneBl.length
        ? `<div class="sec"><b>Waits on</b>${openBl.length ? `<div class="plist">${openBl.map((x) => stepRow(x, other(x))).join('')}</div>` : '<p class="none">Nothing still open.</p>'}${moreBox(doneBl.length, 'done', doneBl.map((x) => stepRow(x, other(x))).join(''))}</div>`
        : '') +
      (ub.length
        ? `<div class="sec"><b>Then${open(n) && openDown > ub.length ? ` <span class="cnt">unblocks ${openDown} steps in all</span>` : ''}</b><div class="plist">${ub.map((x) => stepRow(x, other(x))).join('')}</div></div>`
        : '') +
      (n.model && n.state === 'active' ? `<div class="sec"><b>Agent</b><p>${esc(n.owner)} · ${esc(n.model)}${n.model_id ? ` <code>${esc(n.model_id)}</code>` : ' (model not recorded)'}</p></div>` : '') +
      `<details class="more"><summary>Details</summary><div class="fine">${open(n) && n.text ? `<p><b>Status in the data</b> ${esc(n.text)}</p>` : ''}<p><code>${esc(n.id)}</code></p></div></details>`
    )
  }

  function go(id: string) {
    if (!byId[id]) return
    wsSel = null
    sel = id
    applySel()
    els[id]?.scrollIntoView?.({ block: 'center', inline: 'center', behavior: 'smooth' })
  }
  function clear() {
    sel = wsSel = null
    applySel()
  }
  function route(v: typeof view) {
    view = v
    ;(['rules', 'flows'] as const).forEach((x) => ($(x + 'View').hidden = x !== view))
    root.querySelectorAll<HTMLElement>('.tabs a').forEach((t) => (t.dataset.view === view ? t.setAttribute('aria-current', 'page') : t.removeAttribute('aria-current')))
    if (view === 'graph') drawEdges()
    else clear()
  }

  // ---------- events ----------
  const onClick = (e: MouseEvent) => {
    const target = e.target as HTMLElement
    // Selecting text on a card (to copy it) is not a click.
    if (window.getSelection()?.toString() && target.closest('.card')) return
    const lin = target.closest<HTMLAnchorElement>('a.lin')
    if (lin) {
      e.preventDefault()
      if (lin.dataset.url) opts.openUrl(lin.dataset.url)
      return
    }
    const tab = target.closest<HTMLElement>('.tabs a[data-view]')
    if (tab) {
      e.preventDefault()
      route(tab.dataset.view as typeof view)
      return
    }
    if (target.closest('[data-tb="chat"]')) return opts.onChat()
    if (fMenu && !fMenu.hidden && !target.closest('.fwrap')) setMenu(false)
    if (target.closest('[data-tb="fBtn"]')) return setMenu(fMenu.hidden)
    if (target.closest('[data-tb="need"]')) return setYouOnly(!fOwners.has('you'))
    if (target.closest('[data-tb="segAll"]')) return setYouOnly(false)
    if (target.closest('[data-tb="segYou"]')) return setYouOnly(true)
    if (target.closest('[data-tb="fClear"]')) {
      fq = ''
      qEl.value = ''
      fOwners.clear()
      return render()
    }
    if (!target.closest('.graph, .sheet, .rules')) return
    const wg = target.closest<HTMLElement>('[data-wsgo]')
    if (wg) {
      wsSel = wg.dataset.wsgo as string
      sel = null
      applySel()
      canvasEl.querySelector(`.tree[data-ws="${cssEscape(wsSel)}"]`)?.scrollIntoView?.({ block: 'center', behavior: 'smooth' })
      return
    }
    const tg = target.closest<HTMLElement>('[data-toggle]')
    if (tg) {
      const id = tg.dataset.toggle as string
      if (collapsed.has(id)) collapsed.delete(id)
      else collapsed.add(id)
      render()
      return
    }
    const t = target.closest<HTMLElement>('[data-go],[data-id],[data-tb="close"]')
    const ws = !t && target.closest<HTMLElement>('[data-ws]')
    // from Rules or Workflows, a workstream in the tree goes back to the Graph, focused on it
    if (ws && view !== 'graph') {
      route('graph')
      wsSel = ws.dataset.ws as string
      sel = null
      applySel()
      return
    }
    if (ws) {
      wsSel = wsSel === ws.dataset.ws ? null : (ws.dataset.ws as string)
      sel = null
      applySel()
      return
    }
    if (!t) {
      if ((sel || wsSel) && target.closest('.canvas')) clear()
      return
    }
    if (t.dataset.tb === 'close') clear()
    else if (t.dataset.go) go(t.dataset.go)
    else {
      sel = sel === t.dataset.id ? null : (t.dataset.id as string)
      wsSel = null
      applySel()
    }
  }
  // pointing at a step shows what it waits on and what waits on it
  let hov: string | null = null
  const onOver = (e: MouseEvent) => {
    const c = (e.target as HTMLElement).closest<HTMLElement>('.canvas .card'),
      id = c ? (c.dataset.id ?? null) : null
    if (id === hov) return
    hov = id
    canvasEl.querySelectorAll<SVGGElement>('svg.edges g').forEach((g) => g.classList.toggle('hov', !!id && (g.dataset.a === id || g.dataset.b === id)))
  }
  const onKey = (e: KeyboardEvent) => {
    const card = (e.target as HTMLElement | null)?.closest?.('.card[role="button"]') as HTMLElement | null
    if (card && (e.key === 'Enter' || e.key === ' ')) {
      e.preventDefault()
      card.click()
      return
    }
    const a = document.activeElement as HTMLElement | null
    const typing = !!a && (/INPUT|TEXTAREA|SELECT/.test(a.tagName) || a.isContentEditable)
    if (e.key === 'Escape') {
      if (!fMenu.hidden) {
        setMenu(false)
        fBtn.focus()
        return
      }
      if (a === qEl && qEl.value) {
        qEl.value = ''
        fq = ''
        render()
        return
      }
      if (!typing && (sel || wsSel)) clear()
      return
    }
    if (e.key === '/' && !typing) {
      e.preventDefault()
      qEl.focus()
    }
  }
  const setMenu = (o: boolean) => {
    fMenu.hidden = !o
    fBtn.setAttribute('aria-expanded', String(o))
  }
  const setYouOnly = (on: boolean) => {
    fOwners.clear()
    if (on) fOwners.add('you')
    render()
  }
  let qT: ReturnType<typeof setTimeout> | undefined
  const onInput = () => {
    clearTimeout(qT)
    qT = setTimeout(() => {
      fq = qEl.value.trim().toLowerCase()
      render()
    }, 120)
  }
  const onHide = () => {
    fHideDone = hdBox.checked
    render()
  }
  const onResize = () => {
    drawTree()
    drawEdges()
  }
  const sized = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(() => onResize()) : null
  sized?.observe(root)
  root.addEventListener('click', onClick)
  root.addEventListener('mouseover', onOver)
  document.addEventListener('keydown', onKey)
  qEl.addEventListener('input', onInput)
  hdBox.addEventListener('change', onHide)
  window.addEventListener('resize', onResize)
  if (typeof document !== 'undefined' && document.fonts?.ready) void document.fonts.ready.then(() => root.isConnected && onResize())

  // Workflow templates, from Commander's shape templates (internal/beads/shape_templates.go).
  $('flowList').innerHTML = WORKFLOWS.map(
    (w) =>
      `<div class="flow"><h3><span title="used by agents as: ${esc(w.name.replace(', ', '-'))}">${esc(w.title)}</span></h3><p>${esc(w.what)}</p><div class="chain">` +
      w.steps
        .map(
          ([t, s, orig, opt, note, who], i) =>
            (i ? '<span class="arr" aria-hidden="true">→</span>' : '') +
            `<span class="chip t-${t}${opt ? ' opt' : ''}" title="In the template: ${esc(orig)}"><b><i></i>${esc(who || OWNER[t])}</b>${esc(s)}${opt ? "<em>only when the owner's go is needed</em>" : ''}${note ? `<em>${esc(note)}</em>` : ''}</span>`,
        )
        .join('') +
      '</div></div>',
  ).join('')
  route('graph')

  return {
    linearSlot: $('linear'),
    inspectorSlot: $('inspector'),
    update(t: Tree) {
      setTree(t)
      render()
    },
    clear,
    destroy() {
      clearInterval(timer)
      clearTimeout(qT)
      root.removeEventListener('click', onClick)
      root.removeEventListener('mouseover', onOver)
      sized?.disconnect()
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', onResize)
      root.innerHTML = ''
    },
    setChatOpen(o: boolean) {
      chatOpen = o
      root.classList.toggle('sheet-open', o || !!(sel || wsSel))
    },
  }
}
