import { cleanup, render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { Navigator } from '../src/renderer/src/components/Navigator'

afterEach(() => { cleanup(); localStorage.clear() })

it('names root ownership only when more than one Commander owns rows, nested overrides, and reflects a returned jurisdiction', () => {
  const s = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
  const root = s.display!.workstreams[0]
  root.planning_commander = 'commander'
  s.display!.workstreams[1].planning_commander = 'commander-other'
  root.tracks = [{key:root.key+'.urgent',bead_id:'nested-urgent',letter:'.1',title:'Urgent work',kind:'waiting',text:'Commander to plan the next action',since:0,progress:{done:0,total:1},working:0,planning_commander:'commander-urgent'}]
  localStorage.clear()
  localStorage.setItem('nav.tab','all')
  useStore.setState({ snapshot:s, selected:{kind:'commander'}, cursor:'commander', conn:'live', completed:{}, leaving:{} })
  const view = render(<Navigator />)
  expect(screen.getAllByLabelText('Responsible Commander: commander').length).toBeGreaterThan(0)
  expect(screen.getByLabelText('Responsible Commander: commander-urgent')).toHaveTextContent('Commander · urgent')
  expect(screen.getByLabelText('Responsible Commander: commander-urgent')).toHaveAttribute('title','commander-urgent')
  const returned = structuredClone(s)
  returned.display!.workstreams[0].tracks[0].planning_commander = 'commander'
  useStore.setState({snapshot:returned})
  view.rerender(<Navigator />)
  expect(screen.queryByLabelText('Responsible Commander: commander-urgent')).not.toBeInTheDocument()
  // One Commander owning every row needs no label.
  const single = structuredClone(returned)
  single.display!.workstreams[1].planning_commander = 'commander'
  useStore.setState({snapshot:single})
  view.rerender(<Navigator />)
  expect(screen.queryByLabelText('Responsible Commander: commander')).not.toBeInTheDocument()
})
