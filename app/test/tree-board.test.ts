// The tree board draws cmdrd's board tree as it is: every row, card and Now
// cell on screen says exactly what the tree (the Commander's read tool,
// `cmdr workstream-statuses`) says. Runs on the fixture cmdrd's Go tests
// write (test/fixtures/tree-board.json) and, with TREE_LIVE=<file>, on a live tree.
import { readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { mountBoard, type Board } from '../src/features/treeboard/board'
import type { Selection, Tree } from '../src/features/treeboard/types'

const futureCard = join(__dirname, 'fixtures', 'tree-future-card.json')
const sources: [string, string][] = [
  ['fixture', join(__dirname, 'fixtures', 'tree-board.json')],
  ['future card', futureCard],
]
if (process.env.TREE_LIVE) sources.push(['live', process.env.TREE_LIVE])

let board: Board | null = null
afterEach(() => {
  board?.destroy()
  board = null
  document.body.innerHTML = ''
})

function mount(tree: Tree, onSelect: (s: Selection | null) => void = () => {}) {
  const root = document.createElement('div')
  root.className = 'tb'
  document.body.appendChild(root)
  board = mountBoard(root, { onSelect, onChat: () => {}, linearUrl: () => null, openUrl: () => {} })
  board.update(tree)
  return root
}

const text = (el: Element | null) => (el?.textContent ?? '').replace(/\s+/g, ' ').trim()

for (const [name, file] of sources) {
  describe(`tree board from the ${name} tree`, () => {
    if (!existsSync(file)) {
      it.skip(`no ${file}`, () => {})
      return
    }
    const tree = JSON.parse(readFileSync(file, 'utf8')) as Tree
    const rowsById = Object.fromEntries(tree.rows.map((r) => [r.id, r]))

    it('shows the header counts the tree gives', () => {
      const root = mount(tree)
      expect(text(root.querySelector('[data-tb="sub"]'))).toMatch(new RegExp(`^${tree.done} of ${tree.total} done`))
      expect(text(root.querySelector('[data-tb="needN"]'))).toBe(String(tree.needs_you))
    })

    it('draws every open workstream, in tree order, with its label and title', () => {
      const root = mount(tree)
      const shown = [...root.querySelectorAll<HTMLElement>('.tree[data-ws]')].map((t) => t.dataset.ws)
      // Hide done is on by default: every workstream that is not done, in order.
      expect(shown).toEqual(tree.rows.filter((r) => !r.done).map((r) => r.id))
      for (const id of shown) {
        const cell = root.querySelector(`.tree[data-ws="${CSS.escape(id as string)}"]`) as HTMLElement
        expect(text(cell.querySelector('.tl'))).toBe(rowsById[id as string].label)
        expect(text(cell.querySelector('.tn'))).toContain(rowsById[id as string].title)
      }
    })

    it('draws every open step of a shown workstream as its card says in the tree', () => {
      const root = mount(tree)
      const shownRows = new Set(tree.rows.filter((r) => !r.done).map((r) => r.id))
      const open = tree.steps.filter((s) => s.state !== 'done' && shownRows.has(s.row))
      expect(root.querySelectorAll('.canvas .card').length).toBe(open.length)
      for (const s of open) {
        const card = root.querySelector(`.canvas .card[data-id="${CSS.escape(s.id)}"]`) as HTMLElement
        expect(card, s.id).not.toBeNull()
        const own = card.querySelector('.own') as HTMLElement
        const tm = own.querySelector('.tm')
        expect(text(own).replace(text(tm), '').trim()).toBe(s.own)
        expect(text(card.querySelector('.tt'))).toBe(s.title)
        expect(text(card.querySelector('.wt'))).toBe(s.wt ?? '')
        expect(card.classList.contains(`t-${s.type}`)).toBe(true)
        expect(card.classList.contains(`s-${s.state}`)).toBe(true)
        // the column cmdrd placed it in: grid column 2 is Now (the tree is column 1)
        expect((card.parentElement as HTMLElement).style.gridColumn).toBe(String(2 + s.col))
      }
    })

    it("says in the Now cell what the tree says, where no step of the row's own is current", () => {
      const root = mount(tree)
      for (const r of tree.rows.filter((r) => !r.done)) {
        if (tree.steps.some((s) => s.row === r.id && s.state !== 'done' && s.col === 0)) continue
        const cell = root.querySelector(`.cell[data-row="${CSS.escape(r.id)}"]`) as HTMLElement
        expect(cell, r.label).not.toBeNull()
        const note = cell.querySelector('.note.sum') ?? cell.querySelector('.note')
        expect(text(note)).toBe(r.now)
      }
    })

    it('opens a workstream panel and reports it as the selection', () => {
      const r = tree.rows.find((x) => !x.done)
      if (!r) return
      const seen: (Selection | null)[] = []
      const root = mount(tree, (s) => seen.push(s))
      ;(root.querySelector(`.tree[data-ws="${CSS.escape(r.id)}"]`) as HTMLElement).click()
      expect(seen.at(-1)).toMatchObject({ kind: 'workstream', id: r.id, label: r.label, title: r.title })
      expect(text(root.querySelector('.tb-panel h2'))).toBe(r.title)
      expect((root.querySelector('[data-tb="sheet"]') as HTMLElement).hidden).toBe(false)
    })
  })
}

// Nov 20: a merge card of Eric's, written ahead and three steps out, was drawn
// live (red border, dot, "2h") and counted as needing him. Only the current step is.
describe('a card of yours behind open work', () => {
  it('draws only the working step as current; your future cards are faded, with no timer, and not counted', () => {
    const tree = JSON.parse(readFileSync(futureCard, 'utf8')) as Tree
    const root = mount(tree)
    const card = (id: string) => root.querySelector(`.canvas .card[data-id="${id}"]`) as HTMLElement
    const active = [...root.querySelectorAll<HTMLElement>('.canvas .card.s-active')].map((c) => c.dataset.id)
    expect(active).toEqual(['c.2'])
    expect((card('c.2').parentElement as HTMLElement).style.gridColumn).toBe('2') // the Now column
    for (const id of ['c.3', 'c.4', 'c.5', 'c.6']) expect(card(id).classList.contains('s-notstarted'), id).toBe(true)
    const merge = card('c.5')
    expect(merge.classList.contains('t-you')).toBe(true)
    expect(merge.querySelector('.tm')).toBeNull()
    expect((merge.parentElement as HTMLElement).style.gridColumn).toBe('5') // three steps out
    expect(text(root.querySelector('[data-tb="needN"]'))).toBe('0')
  })
})

describe('one grid', () => {
  it('puts the tree header and the Now header in one grid row, and each workstream in one row across the tree and the board', () => {
    const tree = JSON.parse(readFileSync(sources[0][1], 'utf8')) as Tree
    const root = mount(tree)
    const grids = root.querySelectorAll('.canvas')
    expect(grids.length).toBe(1)
    const grid = grids[0] as HTMLElement
    expect((grid.querySelector('.tree.hd') as HTMLElement).style.gridRow).toBe('1')
    expect((grid.querySelector('.hd.now') as HTMLElement).style.gridRow).toBe('1')
    for (const t of grid.querySelectorAll<HTMLElement>('.tree[data-ws]')) {
      const row = t.style.gridRow
      const cells = [...grid.querySelectorAll<HTMLElement>(`.cell`)].filter((c) => c.style.gridRow === row)
      expect(cells.length, t.dataset.ws).toBeGreaterThan(0)
    }
  })
})

describe('time on a step', () => {
  it('reads now for the first minute, then minutes, hours and days', async () => {
    const { ago } = await import('../src/features/treeboard/board')
    const t = 1_800_000_000_000
    expect(ago(t, t + 5_000)).toBe('now')
    expect(ago(t, t + 59_000)).toBe('now')
    expect(ago(t, t + 60_000)).toBe('1m')
    expect(ago(t, t + 2 * 3_600_000)).toBe('2h')
    expect(ago(t, t + 3 * 86_400_000)).toBe('3d')
  })
})
