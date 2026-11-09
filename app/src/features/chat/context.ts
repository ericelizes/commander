import { createContext, useContext } from 'react'
import type { ChatState } from './events'
import type { ApproveRequest } from './transport'

export interface ChatActions {
  approve: (req: ApproveRequest & { always?: boolean }) => void
  answer: (id: string, answers: Record<string, string>) => void
  readonly: boolean
  now: () => number
}

export const ItemsContext = createContext<ChatState['items']>({})
export const ActionsContext = createContext<ChatActions>({ approve: () => {}, answer: () => {}, readonly: false, now: () => Date.now() })

export const useItems = () => useContext(ItemsContext)
export const useActions = () => useContext(ActionsContext)

export const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
export const modKey = isMac ? '⌘' : 'Ctrl'
