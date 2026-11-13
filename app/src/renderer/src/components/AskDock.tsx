// AskDock puts the selected ask's choices above Commander's input bar, so the
// user answers where they type. PitchChoices records the answer, then sends
// Commander an FYI; an answered or unwritten ask has no dock.

import { useState } from 'react'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { askWhere } from '../../../features/decisions'
import { useStore } from '../store'
import { askKey, trackTitle } from '../viewmodel'
import { PitchChoices, pitchUnwritten, usePitch } from './PitchView'

export function AskDock({ role }: { role: string }) {
  const selected = useStore((s) => s.selected)
  const key = selected.kind === 'ask' ? selected.key : ''
  const item = useStore((s) => (key ? s.snapshot?.needs_you.find((n) => askKey(n) === key) : undefined))
  if (role !== 'commander' || !item) return null
  return <Dock key={key} item={item} />
}

function Dock({ item }: { item: NonNullable<ReturnType<typeof useStore.getState>['snapshot']>['needs_you'][number] }) {
  const { versions, answer } = usePitch(item)
  const [folded, setFolded] = useState(false)
  if (answer || pitchUnwritten(item.decision, item.note)) return null
  const card = item.decision
  const question = card?.question && card.question !== 'Approve?' ? card.question : item.title
  const where = [`${item.rank ? `#${item.rank}${item.track_letter ?? ''} ` : ''}${item.workstream || askWhere(item)}`, item.track ? trackTitle(item.track) : ''].filter(Boolean).join(' › ')
  return (
    <div className={`ask-dock${folded ? ' is-folded' : ''}`}>
      <button type="button" className="ask-dock-fold" aria-expanded={!folded} aria-label={folded ? 'Show the choices' : 'Fold the choices'} onClick={() => setFolded(!folded)}>
        {folded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
      </button>
      {folded ? (
        <button type="button" className="ask-dock-q" onClick={() => setFolded(false)}>
          {question}
        </button>
      ) : (
        <PitchChoices item={item} question={question} where={where} version={versions.length} />
      )}
    </div>
  )
}
