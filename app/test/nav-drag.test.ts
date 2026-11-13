import { describe, expect, it } from 'vitest'
import { applyMove, dropAt, flatRows, landed, placeArgv, undoArgv, without, withoutGone } from '../src/renderer/src/components/navDrag'
import type { BoardRow, BoardTrack } from '../src/shared/snapshot'

const t = (id: string, children?: BoardTrack[]) => ({ key: `p:${id}`, bead_id: id, title: id, letter: '', children }) as unknown as BoardTrack
const r = (id: string, project: string, tracks: BoardTrack[] = [], scope?: string) => ({ key: `${project}:${id}`, bead_id: id, title: id, project, tracks, scope }) as unknown as BoardRow

// a            depth 0
//   a1         depth 1
//     a11      depth 2
//   a2         depth 1
// b            depth 0
// other        depth 0, another project
const rows = [r('a', 'p', [t('a1', [t('a11')]), t('a2')]), r('b', 'p'), r('other', 'q')]
const flat = flatRows(rows, () => false, () => true)
const place = (key: string, gap: number, want: number) => {
  const d = dropAt(flat, key, gap, want)
  return d && { parent: d.parent?.bead ?? '', after: d.after?.bead ?? '', depth: d.depth, to: d.to }
}

describe('where a drag lands', () => {
  it('reads the rows as drawn', () => {
    expect(flat.map((n) => `${n.depth}${n.bead}`)).toEqual(['0a', '1a1', '2a11', '1a2', '0b', '0other'])
    expect(without(flat, 'p:a1').map((n) => n.bead)).toEqual(['a', 'a2', 'b', 'other'])
    expect(flatRows(rows, (k) => k === 'p:a', () => true).map((n) => n.bead)).toEqual(['a', 'b', 'other'])
    expect(flatRows(rows, () => false, () => false).map((n) => n.bead)).toEqual(['a', 'a1', 'a2', 'b', 'other'])
  })

  it('moving right nests it under the row above, however deep', () => {
    // b dropped just after a11 (list without b: a a1 a11 a2 other)
    expect(place('p:b', 3, 3)).toMatchObject({ parent: 'a11', depth: 3, after: '' })
    expect(place('p:b', 3, 2)).toMatchObject({ parent: 'a1', depth: 2, after: 'a11' })
  })

  it('cannot go shallower than the row below or deeper than one past the row above', () => {
    expect(place('p:b', 3, 0)).toMatchObject({ depth: 1, parent: 'a', after: 'a1' })
    expect(place('p:b', 1, 9)).toMatchObject({ depth: 1, parent: 'a', after: '' })
  })

  it('reorders at the top level by rank', () => {
    expect(place('p:b', 0, 0)).toMatchObject({ parent: '', depth: 0, to: 1 })
    expect(place('q:other', 4, 0)).toMatchObject({ parent: '', to: 2 })
  })

  it('takes a nested row out to the top', () => {
    expect(place('p:a2', 3, 0)).toMatchObject({ parent: '', depth: 0, to: 2, after: 'a' })
  })

  it('never nests across projects', () => {
    // Just under "other", a row of p can only stay at the top.
    expect(place('p:b', 5, 1)).toMatchObject({ parent: '', depth: 0 })
    // A row of q cannot go between a and its children: nothing there is allowed.
    expect(place('q:other', 1, 1)).toBeNull()
  })

  it('a drop where it already is does nothing', () => {
    expect(place('p:a2', 3, 1)).toBeNull()
    expect(place('p:b', 4, 0)).toBeNull()
    expect(place('p:a', 0, 0)).toBeNull()
  })

  it('a drop on a row standing in for its one child goes beside that child', () => {
    const f = flatRows([r('w', 'p', [], 'w.1'), r('x', 'p')], () => false, () => false)
    const d = dropAt(f, 'p:x', 1, 1)!
    expect(placeArgv(f[1], d)).toEqual(['internal-place', '--project', 'p', '--bead', 'x', '--parent', 'w', '--after', 'w.1'])
    // A chain under the row: beside the child, under its real parent.
    const chain = flatRows([r('w', 'p', [], 'w.1.1'), r('x', 'p')], () => false, () => false, () => 'w.1')
    expect(placeArgv(chain[1], dropAt(chain, 'p:x', 1, 1)!)[6]).toBe('w.1')
  })

  it('the row opens up at once, showing both', () => {
    const rs = [r('w', 'p', [], 'w.1'), r('x', 'p')]
    const f = flatRows(rs, () => false, () => false)
    const next = applyMove(rs, f[1], dropAt(f, 'p:x', 1, 1)!)
    expect(next[0].scope).toBeUndefined()
    expect(next[0].tracks.map((t) => `${t.letter} ${t.bead_id}`)).toEqual(['.1 w.1', '.2 x'])
  })

  it('names the sibling before it among nested rows', () => {
    const d = dropAt(flat, 'p:b', 3, 2)!
    expect(placeArgv(flat[4], d)).toEqual(['internal-place', '--project', 'p', '--bead', 'b', '--parent', 'a1', '--after', 'a11'])
  })
})

describe('what a drop does to the sidebar at once', () => {
  const numbers = (rs: BoardRow[]) => rs.map((x) => `${x.n} ${x.bead_id}[${x.tracks.map((y) => y.letter + y.bead_id).join(',')}]`)

  it('renumbers a reorder among nested rows', () => {
    const d = dropAt(flat, 'p:a2', 1, 1)!
    const next = applyMove(rows, flat.find((n) => n.key === 'p:a2')!, d)
    expect(numbers(next)[0]).toBe('1 a[.1a2,.2a1]')
  })

  it('takes a nested row out to the top', () => {
    const d = dropAt(flat, 'p:a2', 3, 0)!
    const next = applyMove(rows, flat.find((n) => n.key === 'p:a2')!, d)
    expect(numbers(next)).toEqual(['1 a[.1a1]', '2 a2[]', '3 b[]', '4 other[]'])
  })

  it('nests a top-level row, and the rows after it move up', () => {
    const d = dropAt(flat, 'p:b', 4, 1)!
    const next = applyMove(rows, flat.find((n) => n.key === 'p:b')!, d)
    expect(numbers(next)).toEqual(['1 a[.1a1,.2a2,.3b]', '2 other[]'])
  })

  it('knows when the board has caught up', () => {
    const d = dropAt(flat, 'p:b', 4, 1)!
    const node = flat.find((n) => n.key === 'p:b')!
    expect(landed(rows, node, d)).toBe(false)
    expect(landed(applyMove(rows, node, d), node, d)).toBe(true)
    const top = dropAt(flat, 'p:b', 0, 0)!
    expect(landed(rows, node, top)).toBe(false)
    expect(landed(applyMove(rows, node, top), node, top)).toBe(true)
  })
})

describe('a completed row', () => {
  it('closes the numbers up at once, at any depth', () => {
    const numbered = applyMove(rows, flat[4], dropAt(flat, 'p:b', 0, 0)!)
    const next = withoutGone(numbered, (k) => k === 'p:b' || k === 'p:a1')
    expect(next.map((x) => `${x.n} ${x.bead_id}[${x.tracks.map((y) => y.letter + y.bead_id).join(',')}]`)).toEqual(['1 a[.1a2]', '2 other[]'])
  })

  it('leaves the rows alone when none went', () => {
    expect(withoutGone(rows, () => false)).toBe(rows)
  })
})


describe('undo argv captured before a drag', () => {
  it('restores the top-level rank across projects', () => {
    expect(undoArgv(flat, flat[5])).toEqual(['internal-place', '--project', 'q', '--bead', 'other', '--parent', '', '--to', '3'])
  })
  it('restores a nested row after its previous sibling, ignoring descendants', () => {
    expect(undoArgv(flat, flat[3])).toEqual(['internal-place', '--project', 'p', '--bead', 'a2', '--parent', 'a', '--after', 'a1'])
  })
  it('restores the first nested sibling to rank one', () => {
    expect(undoArgv(flat, flat[1])).toEqual(['internal-place', '--project', 'p', '--bead', 'a1', '--parent', 'a', '--to', '1'])
  })
})
