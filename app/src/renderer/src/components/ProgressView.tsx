// ProgressView is the Progress page, joined to the board's workstreams.

import { useMemo } from 'react'
import { ProgressPage, progressWorkstreams } from '../../../features/linear/MyProgress'
import { useStore } from '../store'
import { board } from '../viewmodel'

export function ProgressView() {
  const snapshot = useStore((s) => s.snapshot)
  const workstreams = useMemo(() => {
    if (!snapshot) return []
    return progressWorkstreams(snapshot.workstreams, board(snapshot).workstreams)
  }, [snapshot])
  return <ProgressPage workstreams={workstreams} onOpen={(key) => useStore.getState().openWorkflow(key)} />
}
