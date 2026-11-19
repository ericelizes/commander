// The last tree cmdrd served (GET /api/tree), for views beside the board that read it too.

import { create } from 'zustand'
import type { Tree } from './types'

export const useLiveTree = create<{ tree: Tree | null }>(() => ({ tree: null }))
