// The Brief's data: GET /api/brief for the latest, POST to write a new one.
// cmdrd writes it on its own schedule (7 AM Eastern, then every 6 hours), so
// the page only reads, and polls while a rewrite is under way.

import { useEffect } from 'react'
import { create } from 'zustand'
import { useStore } from '../../renderer/src/store'

// A paragraph sentence or a bullet, with what it cites (bead:<id>, linear:<ID>, pr:<repo>#<n>, run:<repo>#<id>).
export interface BriefSentence { text: string; sources: string[] }
export interface BriefSection { title: string; paragraph: BriefSentence[]; bullets: BriefSentence[] }
export interface BriefEvidence { id: string; ref?: string; workstream: string; when: string; label: string; text: string; at?: string }
export interface BriefWindow { start: string; end: string; label: string }
export interface Brief {
  sections: BriefSection[]
  markdown: string
  generated_at: string
  windows: { now: string; today: BriefWindow; yesterday: BriefWindow; week: BriefWindow }
  linear_note?: string
}
export interface BriefStatus { brief?: Brief; evidence: BriefEvidence[]; generating: boolean; error?: string; next_at: string }

export const useBrief = create<{ status: BriefStatus | null; failed: boolean }>(() => ({ status: null, failed: false }))

type Config = { apiUrl: string; token: string }

async function call(config: Config, method: 'GET' | 'POST') {
  try {
    const res = await fetch(`${config.apiUrl}/api/brief`, { method, headers: { Authorization: `Bearer ${config.token}` } })
    if (!res.ok) throw new Error(String(res.status))
    useBrief.setState({ status: (await res.json()) as BriefStatus, failed: false })
  } catch {
    useBrief.setState({ failed: true })
  }
}

export const loadBrief = (config: Config) => call(config, 'GET')
export const regenerateBrief = (config: Config) => call(config, 'POST')

// useBriefStatus reads the Brief while the page is open: every 3s while a
// rewrite runs, else every minute (a scheduled rewrite shows up on its own).
export function useBriefStatus() {
  const config = useStore((s) => s.config)
  const generating = useBrief((s) => s.status?.generating ?? false)
  useEffect(() => {
    if (!config) return
    void loadBrief(config)
    const timer = setInterval(() => void loadBrief(config), generating ? 3_000 : 60_000)
    return () => clearInterval(timer)
  }, [config, generating])
}
