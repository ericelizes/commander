// "Install latest from main": cmdrd's GET /api/update (how far behind this
// build is, the commits, the running job), polled into a small store. The
// install button only asks cmdrd (POST /api/update/install); cmdrd itself shows
// the native confirmation on the machine and installs only if the person
// presses Install there. This page cannot confirm anything. When cmdrd cannot
// show that dialog (headless, or on another machine than the app) the desktop
// app's main process shows its own and installs over SSH
// (commanderShell.runUpdate, electron/remote-update.ts).

import { useEffect } from 'react'
import { create } from 'zustand'
import type { HostConfig } from '../../shared/snapshot'
import { useStore } from './store'

export interface UpdateCommit {
  hash: string
  subject: string
  review: 'reviewed' | 'not confirmed' | string
  evidence?: string
}

export interface JobEvent {
  seq: number
  step: string
  level: string
  text: string
}

export interface UpdateJob {
  id: string
  action: 'install' | 'rollback'
  state: 'confirming' | 'declined' | 'running' | 'restarting' | 'done' | 'failed'
  error?: string
  events: JobEvent[]
}

export interface UpdateStatus {
  running: string
  installed: string
  main: string
  behind: number
  commits: UpdateCommit[]
  truncated?: boolean
  up_to_date: boolean
  dirty: boolean
  error?: string
  commands: string[]
  can_rollback: boolean
  job?: UpdateJob
  repo?: string
  host?: string // the machine cmdrd runs on
  no_dialog?: string // why cmdrd cannot show the confirmation there
}

/** An install the desktop app runs over SSH: its output, then the result. */
export interface AppInstall {
  action: 'install' | 'rollback'
  running: boolean
  lines: string[]
  ok?: boolean
  declined?: boolean
  error?: string
}

interface UpdateStore {
  status: UpdateStatus | null
  setStatus(s: UpdateStatus | null): void
  panelOpen: boolean
  setPanelOpen(open: boolean): void
  appInstall: AppInstall | null
}

export const useUpdateStore = create<UpdateStore>((set) => ({
  status: null,
  setStatus: (status) => set({ status }),
  panelOpen: false,
  setPanelOpen: (panelOpen) => set({ panelOpen }),
  appInstall: null,
}))

const IDLE_MS = 60_000
const BUSY_MS = 1_000

/** jobActive: a job whose end the page still waits for. */
export function jobActive(job: UpdateJob | undefined): boolean {
  return job?.state === 'confirming' || job?.state === 'running' || job?.state === 'restarting'
}

/** showBanner: only while there is something to install, or a job to show. */
export function showBanner(s: UpdateStatus | null): boolean {
  return !!s && !s.error && (s.behind > 0 || !!s.job)
}

export function bannerText(s: UpdateStatus): string {
  if (s.job?.state === 'confirming') return 'Waiting for your confirmation on screen…'
  if (jobActive(s.job)) return s.job!.action === 'rollback' ? 'Rolling back…' : 'Installing…'
  if (s.job?.state === 'failed') return 'Update failed'
  if (s.job?.state === 'declined') return `Update available: main is ${s.behind} ${s.behind === 1 ? 'commit' : 'commits'} ahead`
  if (s.job?.state === 'done' && s.behind === 0) return 'Updated'
  return `Update available: main is ${s.behind} ${s.behind === 1 ? 'commit' : 'commits'} ahead`
}

export function shortHash(h: string): string {
  return h.slice(0, 7)
}

export function reviewedCount(commits: UpdateCommit[]): number {
  return commits.filter((c) => c.review === 'reviewed').length
}

const demoStatus: UpdateStatus = {
  running: '4c2bd19',
  installed: '4c2bd19',
  main: '9f3a7c1',
  behind: 3,
  up_to_date: false,
  dirty: false,
  can_rollback: true,
  commands: ['cd ~/code/commander', 'just build', 'just install --yes --restart-cmdrd'],
  commits: [
    { hash: '9f3a7c1e', subject: 'feat: the one-click install button', review: 'reviewed', evidence: 'bead commander-il9.56.3' },
    { hash: '5be2d40a', subject: 'fix: a read overlapping a write keeps last-good rows', review: 'reviewed', evidence: 'Reviewed-by: a reviewer' },
    { hash: '1c8e9b27', subject: 'docs: describe the install threat model', review: 'not confirmed' },
  ],
}

export async function fetchUpdate(cfg: HostConfig, fresh = false): Promise<UpdateStatus | null> {
  if (cfg.demo) return new URLSearchParams(location.search).get('update') === '0' ? null : demoStatus
  try {
    const res = await fetch(`${cfg.apiUrl}/api/update${fresh ? '?fresh=1' : ''}`, { headers: { Authorization: `Bearer ${cfg.token}` } })
    return res.ok ? ((await res.json()) as UpdateStatus) : null
  } catch {
    return null
  }
}

/** runningChats counts chats with a live process, for the install warning. */
export function runningChats(): number {
  return Object.values(useStore.getState().chats).filter((c) => c.running).length
}

/** startUpdate asks cmdrd, which shows the confirmation dialog itself; it answers 202 once the job waits for the person. */
export async function startUpdate(cfg: HostConfig, action: 'install' | 'rollback'): Promise<{ ok: boolean; error?: string }> {
  if (cfg.demo) return { ok: true }
  try {
    const res = await fetch(`${cfg.apiUrl}/api/update/install`, {
      method: 'POST',
      // X-Commander-Origin only changes the dialog wording; it is not protection.
      headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json', 'X-Commander-Origin': 'app' },
      body: JSON.stringify({ action }),
    })
    const body = (await res.json().catch(() => ({}))) as { error?: string }
    if (!res.ok) return { ok: false, error: body.error ?? `cmdrd answered ${res.status}` }
  } catch {
    return { ok: false, error: 'cmdrd is not reachable' }
  }
  const st = await fetchUpdate(cfg, true)
  if (st) useUpdateStore.getState().setStatus(st)
  return { ok: true }
}

/**
 * installRoute: 'app' when the desktop app can confirm and install itself
 * (an Electron shell with runUpdate) and cmdrd cannot ask at a screen the
 * person sees (it reports no dialog, or runs on another machine); else 'cmdrd'.
 */
export function installRoute(s: UpdateStatus, localHost: string | undefined, canRunHere: boolean): 'app' | 'cmdrd' {
  if (!canRunHere) return 'cmdrd'
  const remote = !!s.host && !!localHost && s.host !== localHost
  return remote || !!s.no_dialog ? 'app' : 'cmdrd'
}

/** appShell is the Electron shell when it has runUpdate (an app installed before it does not). */
export function appShell() {
  const sh = typeof window === 'undefined' ? undefined : window.commanderShell
  return typeof sh?.runUpdate === 'function' ? sh : undefined
}

const MAX_LINES = 200

/** startAppInstall asks the app's main process, which shows its own dialog and runs the install over SSH. */
export async function startAppInstall(cfg: HostConfig, action: 'install' | 'rollback'): Promise<void> {
  const sh = appShell()
  const st = useUpdateStore.getState()
  if (!sh || st.appInstall?.running) return
  const set = (a: AppInstall) => useUpdateStore.setState({ appInstall: a })
  let cur: AppInstall = { action, running: true, lines: [] }
  set(cur)
  let res: Awaited<ReturnType<typeof sh.runUpdate>>
  try {
    res = await sh.runUpdate(action, (line) => {
      cur = { ...cur, lines: [...cur.lines, line].slice(-MAX_LINES) }
      set(cur)
    })
  } catch (e) {
    res = { ok: false, error: (e as Error).message }
  }
  set({ ...cur, running: false, ok: res.ok, declined: res.declined, error: res.error, lines: cur.lines.length ? cur.lines : (res.output ?? []) })
  if (res.declined) return
  // cmdrd restarts after an install; ask until the new one answers.
  for (let i = 0; i < 15; i++) {
    const fresh = await fetchUpdate(cfg, true)
    if (fresh) {
      useUpdateStore.getState().setStatus(fresh)
      return
    }
    await new Promise((r) => setTimeout(r, 2000))
  }
}

/** useUpdatePoll keeps the status fresh: every minute, every second while a job runs. */
export function useUpdatePoll(): void {
  const config = useStore((s) => s.config)
  const active = useUpdateStore((s) => jobActive(s.status?.job))
  useEffect(() => {
    if (!config) return
    let stop = false
    const poll = async () => {
      const st = await fetchUpdate(config)
      // While cmdrd restarts the fetch fails; keep what is shown.
      if (!stop && st) useUpdateStore.getState().setStatus(st)
    }
    void poll()
    const t = setInterval(() => void poll(), active ? BUSY_MS : IDLE_MS)
    return () => {
      stop = true
      clearInterval(t)
    }
  }, [config, active])
}
