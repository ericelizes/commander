// The decisions feature talks to the host only through these types. They
// mirror cmdrd's JSON: internal/livedata (Snapshot.needs_you), internal/store
// (Decision) and internal/api (POST /api/cmd -> CmdResult).

export interface DecisionOption {
  label: string
  implications?: string
  recommended?: boolean
  approves?: boolean // picking it closes the gate
}

// A Manager-written decision card (store.Decision).
export interface Decision {
  question: string
  context?: string
  options?: DecisionOption[]
  input?: boolean
  steps?: string[]
}

// One open needs-the user step (livedata.NeedsYou).
export interface NeedsYouItem {
  stale_why?: string // the server-derived reason this card needs another look
  project: string
  bead_id: string
  title: string
  workstream: string
  rank: number
  ask?: string
  note?: string
  decision?: Decision
  since?: number // unix seconds the ask was raised; absent = unknown
  track?: string // the track (title) it sits in: shown as "Workstream › Track"
  track_id?: string
  track_letter?: string // A, B…: the track's priority letter, as in the sidebar
}

// The slice of /api/snapshot this feature reads.
export interface SnapshotLike {
  needs_you: NeedsYouItem[]
}

// POST /api/cmd response (api.CmdResult).
export interface CmdResult {
  ok: boolean
  stdout: string
  stderr: string
  exit: number
  error?: string
}

// What the host must provide. snapshot() and runCmd() map 1:1 onto cmdrd's
// GET /api/snapshot and POST /api/cmd {argv}. copy/openUrl are optional: the
// feature falls back to navigator.clipboard and window.open.
export interface CommanderApi {
  snapshot(): Promise<SnapshotLike>
  runCmd(argv: string[]): Promise<CmdResult>
  copy?(text: string): Promise<void>
  openUrl?(url: string): void
}

export type ResolveAction = 'decide' | 'approve' | 'defer'

export type DecisionOutcome =
  | { kind: 'answered', action: ResolveAction, choice?: string, note?: string }
  | { kind: 'gone' }
