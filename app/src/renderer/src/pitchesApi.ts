// Every version of every pitch (cmdrd /api/pitches): the card each time it was
// written, and the answer on the version that got one.

import type { HostConfig } from '../../shared/snapshot'

export interface SavedPitch {
  bead_id: string
  version: number
  title: string
  decision: string // the card's JSON
  created_at: number
  answer?: string
  answered_at?: number
}

/** fetchPitches lists the versions of the pitches on a bead and the beads under it, oldest first. */
export async function fetchPitches(cfg: HostConfig, project: string, bead: string): Promise<SavedPitch[]> {
  if (cfg.demo) return []
  const q = new URLSearchParams({ project, bead })
  const res = await fetch(`${cfg.apiUrl}/api/pitches?${q}`, { headers: { Authorization: `Bearer ${cfg.token}` } })
  if (!res.ok) return []
  const body = (await res.json()) as { pitches?: SavedPitch[] }
  return body.pitches ?? []
}
