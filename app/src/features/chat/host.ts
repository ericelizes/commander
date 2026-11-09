// What the app around a chat lends it. The chat feature stays independent of
// the app: links to beads and drawn graphs work only where the host supplies them.

import { createContext, type ComponentType } from 'react'

import type { Attachment } from './transport'

export interface ChatHost {
  // A `cmdr:<bead-id>[/overview|workflow|decision]` link was clicked: open that
  // bead's workstream or track on that tab.
  openBead?(beadId: string, tab?: 'overview' | 'workflow' | 'decision'): void
  // Where such a link goes, in the user's terms, for its tooltip.
  describeBead?(beadId: string, tab?: 'overview' | 'workflow' | 'decision'): string
  // A web link written with the title "cmdr:<bead-id>": open the page in that
  // bead's workstream. false leaves it to openUrl. The href stays the plain page,
  // so copying the link gives just the page.
  openBeadPage?(beadId: string, url: string, title?: string): boolean
  // A web link was clicked: open it in the app's side panel. false (or absent)
  // leaves it to the browser.
  openUrl?(url: string): boolean
  // A role's name as the user knows it ("Commander", "Background jobs Manager").
  agentName?(role: string): string
  // Draws a ```cmdr-graph block once its JSON is complete.
  Graph?: ComponentType<{ spec: unknown }>
  // What the user sees right now, sent ahead of each of his messages to this role.
  screen?(role: string): string
  // The page the user is looking at, attached to a message when he sends it.
  capture?: {
    // What would ride along with a message to role now (null = nothing); a hook.
    usePreview(role: string): { host: string; title: string; blocked: boolean; off?: boolean } | null
    // Turns sharing on or off (the + menu and the chip's x).
    setEnabled?(on: boolean): void
    // The files and the note line, or null when nothing is attached (slow, failed, blocked).
    // The capture decision at the moment of Send (opaque; null = capture nothing).
    snapshot?(role: string): unknown
    take(role: string, target?: unknown): Promise<{ attachments: Attachment[]; note: string } | null>
  }
  // Ends the latest exchange while the agent is between turns (a Manager watching its Operators).
  Watching?: ComponentType<{ role: string }>
  // Sits on top of the input bar: what is waiting on the user's answer.
  Dock?: ComponentType<{ role: string }>
  // Mail waiting for the agent while the user talks to it, in the input bar.
  MailChip?: ComponentType<{ role: string }>
  // the user is typing to role (its mail waits until he pauses).
  typing?(role: string): void
}

export const ChatHostContext = createContext<ChatHost>({})
