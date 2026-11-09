import { create } from 'zustand'

// Shared by ticket pills and the app's settings loader. Missing configuration
// must never send a ticket to an arbitrary workspace.
// linearTeam is the ticket prefix board titles drop ("" = drop nothing).
export const useIntegrations = create<{ linearWorkspace: string; linearTeam: string; timezone: string }>(() => ({ linearWorkspace: '', linearTeam: '', timezone: '' }))
