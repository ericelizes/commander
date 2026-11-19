// ProgressView is the Progress page, joined to the workstreams of cmdrd's tree.

import { useEffect, useMemo } from 'react'
import { ProgressPage, treeWorkstreams } from '../../../features/linear/MyProgress'
import { fetchTree } from '../../../features/treeboard/TreeBoard'
import { useLiveTree } from '../../../features/treeboard/liveTree'
import { useStore } from '../store'

export function ProgressView() {
  const cfg = useStore((s) => s.config)
  const version = useStore((s) => s.snapshot?.version ?? 0)
  const tree = useLiveTree((s) => s.tree)
  // The board is not on screen here, so this page reads the tree itself.
  useEffect(() => {
    if (!cfg || cfg.demo || !cfg.apiUrl) return
    let live = true
    fetchTree(cfg.apiUrl, cfg.token).then((t) => live && useLiveTree.setState({ tree: t }), () => {})
    return () => {
      live = false
    }
  }, [cfg, version])
  const workstreams = useMemo(() => treeWorkstreams(tree), [tree])
  return <ProgressPage workstreams={workstreams} onOpen={(key) => useStore.getState().openWorkflow(key)} />
}
