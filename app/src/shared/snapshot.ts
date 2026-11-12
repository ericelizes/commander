import type { WebviewBridge } from '../features/inspector/types'

// The JSON cmdrd serves at /api/snapshot and pushes over /api/events.
// Mirrors internal/livedata/snapshot.go; keep the two in sync.

export interface SnapExecution {
  startup_attempts?: number
  startup_error?: string
  role: string
  state: string
  project_id: number
  bead_id: string
  run_id?: number
  session_id?: string
}

export interface Snapshot {
  orchestration_mode?: 'off' | 'shadow' | 'active'
  storage?: string
  executions?: SnapExecution[] | null
  pr_states?: Record<string, string>
  answers?: Record<string, number>
  version: number
  load_completed_at?: string
  load_error?: boolean
  generated_at: string
  projects: SnapProject[]
  workstreams: SnapWorkstream[] // the user's rank order; n is the sidebar number
  needs_you: NeedsYou[]
  agents: Record<string, SnapAgent> // by mail role: "commander", "manager-<bead>"
  fleet: SnapFleet
  unread: Record<string, number>
  // cmdrd's minute watch over each Manager's running Operators, by Manager role.
  watch?: Record<string, SnapWatch>
  error?: string
  // Set when cmdrd cannot read its state: the rest of the snapshot is stale or empty.
  degraded?: { reason: string }
  pending?: string[] // steps shown as a write the door answered left them, before a load read them back
  // What keeps failing in cmdrd right now (mail that cannot reach an agent, a
  // plan that breaks the rules): each a problem until it clears.
  health?: { key: string; detail: string; since: number }[]
  wrong?: { key: string; target?: string; text: string }[] // where the screen disagrees with what is happening (cmdrd)
  display?: Display // the board as cmdrd derives it: rendered as it is
  board_versions?: Record<string, number> // each role's board file version (boardturn), by role
}

// The board (livedata.Board): every row's state as one derivation in cmdrd
// says it, for the sidebar and the agents alike. Text never holds an age;
// since is when the state began (unix s, 0 when unknown).
export type LineKind = 'none' | 'idle' | 'working' | 'waiting' | 'stuck' | 'needs' | 'stalled' | 'quiet' | 'done'

export interface Display {
  commander: { kind: LineKind; text: string; since: number; unread: number }
  asks: BoardAsk[]
  workstreams: BoardRow[]
}

export interface BoardAsk {
  key: string
  text: string
  rank: number
  workstream: string
  track: string
  track_letter: string
  ws_key: string
  stale?: 'spec' | 'age' | 'push' | 'named' | 'pr' | 'decision' // the card is out of date: its step was edited after it was raised, or it is older than [needs] stale_days
  stale_why?: string // what changed, one line
}

export interface BoardProgress {
  done: number
  total: number
}

export interface BoardRow {
  planning_commander?: string
  jurisdiction_revision?: number
  key: string
  n: number
  title: string
  project: string
  bead_id: string
  manager_role: string
  starred: boolean
  kind: LineKind
  text: string
  since: number
  progress: BoardProgress
  milestones?: BoardProgress // planned stages; excludes internal task attempts
  working: number // its Manager in a turn and its live Operators
  flag: number // NEEDS YOU items under it
  unread: number
  gate: { bead_id: string; title: string; who: string; at: boolean; for_you: boolean } | null
  tracks: BoardTrack[]
  scope?: string // the one open nested workstream standing in for this row: its title, and where a click lands
  scope_role?: string // that stand-in's own Manager (nested Managers on)
  workflow?: Workflow // absent from a cmdrd that predates it
  why?: string // when the row is not what the beads last said: what cmdrd saw instead
  age_as?: string // the state its age runs under when that is not kind|text
  diagram?: Diagram // absent from a cmdrd that predates it
}

// A workstream's (or track's) plan drawn as whose move each step is
// (livedata.Diagram), derived from beads: steps in work order, each in a lane.
// Drawn as it is; kind and text are the workflow's words.
export type Lane = 'you' | 'agents' | 'elsewhere' | ''

export interface Diagram {
  id: string
  title: string
  current?: string // the step at this level the work is in now
  steps: DiagramStep[]
  tracks?: Diagram[] // on a workstream: each open track's own diagram
  findings?: DiagramFinding[] // what keeps the graph from reading right (cmdr graph check)
}

export interface DiagramStep {
  why?: string
  id: string
  title: string
  kind: LineKind | 'done'
  text: string
  since?: number
  lane: Lane // the coarse side: you; agents (manager, operators); elsewhere (reviewers, automatic)
  actor: '' | 'you' | 'manager' | 'operators' | 'reviewers' | 'automatic' // whose line it is on; "" = nobody's (a finding)
  who?: string // more on the hand: reviewers' or a team's names, what runs and what starts it
  gate?: boolean
  approvers?: string // who approves a gate, the user as "you"
  criteria?: string // what counts as passed
  after?: string[] // the steps of this diagram it waits on
  track?: boolean
  progress?: string // a step with work under it: "3 of 5 done"
  children?: DiagramStep[] // the steps nested under an open step, in work order, drawn inside its box
  review?: ReviewBlock // a plugged review block: drawn collapsed as this one step, its checks open on a click
}

// A plugged review block (livedata.ReviewBlock): its checks, each with what
// its check found, read from the snapshot's evidence (no GitHub call).
export interface ReviewBlock {
  template: string
  pr?: string
  head?: string // the PR head the display knows; a check recorded on another head is stale
  done: number
  total: number
  stale?: number
  settled?: number // checks closed by settle-block after the PR merged outside the review (not in done)
  checks: ReviewCheck[]
}

export interface ReviewCheck {
  id: string // the step's bead, "" when plug has not made it
  step: string
  title: string
  kind: LineKind | 'done'
  text: string
  verifier?: string
  head?: string // the head its evidence was recorded on
  finding?: string // what the check found
  stale?: boolean // closed, but the PR moved since its check
  settled?: boolean // closed by settle-block: the PR merged, this check never ran
}

export interface DiagramFinding {
  check: string
  bead: string
  title: string
  why: string
  fix: string
  advisory?: boolean
}

// A workstream's plan as cmdrd derives it (livedata.Workflow): every step in
// plan order with the board's status words, where it is now and what
// approving each open decision starts. Rendered as it is.
export interface Workflow {
  current?: string // the step being decided, else where work is now, else the next gate
  unlocks?: string[] // what approving current starts, through the next gate
  steps: WorkflowStep[]
}

export interface WorkflowStep {
  id: string
  title: string
  kind: LineKind | 'done'
  text: string
  since?: number
  why?: string // as BoardRow.why
  gate?: boolean
  who?: string // who approves a gate: "you", a reviewer, an agent
  track?: boolean
  current?: string // on a step with children: its own current step
  unlocks?: string[] // on an open decision: the open steps approving it starts, through the next gate
  card_on?: string // a gate whose open card sits on an ancestor: the bead holding it
  children?: WorkflowStep[] // left out once the step is done
  review?: ReviewBlock // a plugged review block: one step, its checks open on a click
}

export interface BoardTrack {
  planning_commander?: string
  jurisdiction_revision?: number
  key: string
  bead_id: string
  letter: string // its number after the row's: ".1", ".1.2"
  children?: BoardTrack[] // its own open nested workstreams
  title: string
  kind: LineKind
  text: string
  since: number
  progress: BoardProgress
  milestones?: BoardProgress // planned stages; excludes internal task attempts
  working: number
  why?: string
  age_as?: string // as BoardRow.age_as
  manager_role?: string // its own Manager (nested Managers on)
}

export interface SnapProject {
  id: number
  name: string
  type: string
  path: string
  status: string
  repo?: string // GitHub owner/name, for PR links
}

export interface Decision {
  question: string
  context?: string
  options?: { label: string; implications?: string; recommended?: boolean; approves?: boolean }[]
  input?: boolean
  steps?: string[]
}

export type StepState = 'not-started' | 'in-progress' | 'review' | 'needs-user' | 'blocked' | 'done' | string

export interface SnapStep {
  planning_pending?: boolean
 result_run_id?: number
 result_ready_at?: number
  question_waiting?: boolean
 human_answer_pending?: boolean
  execution_mode?: string
  execution_eligible?: boolean
  execution_ready?: boolean
  execution_reserved?: boolean
  execution_capable?: boolean
  execution_attempts?: number
  execution_error?: string
  planning_commander?: string
  jurisdiction_revision?: number
  pr_repo?: string
  bead_id: string
  title: string
  state: StepState
  working?: boolean // an agent under it is working now (derived; the app fills it when cmdrd is older)
  status: string
  owner?: string
  owner_state?: 'working' | 'finished' | 'died' // its Operator by process: alive, exited after reporting, exited with the step open
  starred?: boolean
  review?: string
  pr?: string[] // PR numbers of its pr: labels
  tickets?: string[] // Linear ids of its ticket:<ID> labels
  // What GitHub says now about the reviews it waits on, looked up live by cmdrd.
  review_fact?: { acted?: boolean; no_pr?: boolean; text: string }
  ask?: string
  note?: string
  decision?: Decision
  now?: boolean
  phase_note?: string
  children?: SnapStep[]
  blocked_by?: string[] // bead IDs this step waits on (real "blocks" dependencies)
  waiting?: string // what its owner waits on: "CI #104", "review (reviewer-a, reviewer-b)", "you"
  waiting_since?: number // unix s its oldest wait was recorded
  parked_until?: number // unix s a parked (deferred) step comes back: "Parked until <time>"
  waiting_until?: number // unix s of its earliest time wait ("until"): cmdrd wakes the Manager then
  waiting_until_as?: string // that time wait as it reads in `waiting` (its label)
  waiting_checked?: number // unix s cmdrd last looked at its waits (it wakes the Manager only on an event)
  gates?: SnapGate[] // checks it must pass before it closes
  no_gates?: boolean // an open stage (top-level step) with no gates
  broken?: boolean // open with no agent, recorded wait or the user on it (the open-bead invariant)
  until?: string // owned: its owner keeps at it until this finish line (e.g. "merged"); children are the owner's pipeline
  change?: 'add' | 'remove' | 'edit' // only in a graph an agent draws in chat: a proposed change
  gate?: boolean // a human approval gate: work past it waits until it is approved (closed)
  approvers?: string[] // who approves the gate: user, GitHub logins, names
  viz?: GraphLevel // its level in the Graph (label viz:<n>; else gate 1, stage 2, task 3); absent in an older cmdrd
  superseded?: boolean // dropped from the plan: not drawn, never approved
  task?: boolean // an Operator task bead: agent-internal, never drawn as a step
  track?: boolean // a track of its workstream: a separate outcome with its own gate chain and status line
  manager_role?: string // a nested workstream with a Manager of its own: manager-<its bead> (cmdrd with nested Managers on)
  workstream?: string // a track's workstream bead id
  lane?: string // its lane label (you, agents, elsewhere[:who]) where nothing else says whose move it is
  criteria?: string // what counts as passed (acceptance criteria): a gate's criteria
  close_reason?: string // a closed bead's close reason (capped): what it ended with
  finished_tasks?: { id: string; title: string; outcome: string }[] // latest finished tasks folded away under this closed step
  done_blockers?: string[] // blocked_by ids that are finished and not drawn: they hold nothing
  spec_hash?: string // fingerprint of its title+description+acceptance now
  basis?: CardBasis // what its open card was raised against (absent = raised before it was recorded)
}

// What a decision card was raised against (store.Basis): the step's spec
// fingerprint, named dependency states, answered versions, and raise time.
export interface CardBasis {
  spec?: string
  gen?: number
  raised?: number // unix s
  named?: Record<string, string>
  prs?: Record<string, string>
  answers?: Record<string, number>
  age_checks?: number
}

// A bead's level in the Graph: 1 gates only, 2 adds the stages, 3 every bead.
export type GraphLevel = 1 | 2 | 3

export interface SnapWorkstream {
  planning_pending?: boolean
 result_run_id?: number
 result_ready_at?: number
  question_waiting?: boolean
 human_answer_pending?: boolean
  execution_mode?: string
  execution_ready?: boolean
  execution_eligible?: boolean
  execution_reserved?: boolean
  execution_capable?: boolean
  execution_attempts?: number
  execution_error?: string
  planning_commander?: string
  jurisdiction_revision?: number
  repo?: string // GitHub owner/name from cmdrd
  n: number
  project: string
  project_id: number
  bead_id: string
  title: string
  tickets?: string[] // Linear ids of its ticket:<ID> labels
  state: StepState
  working?: boolean // an agent under it is working now (derived; the app fills it when cmdrd is older)
  status: string
  owner?: string
  starred?: boolean
  hold?: boolean // the root bead has the hold label
  hold_reason?: string // from its latest "hold by" comment
  review?: string
  ask?: string
  note?: string
  decision?: Decision
  manager_role: string
  now_step?: string
  done: number
  total: number
  steps: SnapStep[]
  waiting?: string // the workstream bead's own waits
  waiting_since?: number
  waiting_until?: number
  waiting_until_as?: string
  no_gates?: boolean // open, and neither it nor any stage has a gate
  gate_nodes?: SnapGateNode[] // every gate of the workstream, as graph nodes
}

export type GateStatus = 'open' | 'passed' | 'failed'

export interface SnapGate {
  bead_id?: string // the gate's own bead; absent for a row not yet migrated from the old table
  name: string
  verifier: string // 'auto' | 'operator' | 'reviewer:<login>' | 'user'
  status: GateStatus
  evidence?: string
  by?: string
  at?: number // unix seconds of the last result
}

// A stage gate as a node of the bead graph: it waits on its stage and the
// steps waiting on that stage wait on it.
export interface SnapGateNode {
  kind: 'gate'
  bead_id: string
  stage: string
  name: string
  verifier: string
  status: GateStatus
  check?: string
  evidence?: string
  by?: string
  at?: number
  blocked_by: string[]
  blocks: string[]
  viz?: GraphLevel // default 1
}

export interface NeedsYou {
 plan_revision?: number
  stale_why?: string // the server-derived reason this card needs another look
  project: string
  bead_id: string
  title: string
  workstream: string
  rank: number
  ask?: string
  note?: string
  decision?: Decision
  track?: string // the track (title) the item sits in: it reads "Workstream › Track"
  track_id?: string
  track_letter?: string // the track's letter in its workstream (A, B…), set by cmdrd
  spec_hash?: string // the card's step spec fingerprint now
  basis?: CardBasis // what the card was raised against
}

export interface SnapAgent {
  state: 'idle' | 'working' | 'waiting' | 'stuck' | string
  detail?: string
  since: number // unix seconds of the last state change
  wait?: string // what an idle agent's turn ended on ("CI #4101"); state is then "waiting"
}

export interface SnapFleet {
  cost_usd_24h: number
  cost_usd_7d: number
  cost_series_24h: number[] | null
  agents: number
  active: number
  blocked: number
  idle_ratio: number
  projects: number
  chat_cost_usd_24h?: number // Commander + Managers, inside cost_usd_24h
  roles_cost?: SnapRoleCost[] // estimated (plan-equivalent) spend per chat role
}

export interface SnapRoleCost {
  role: string
  today_usd: number // last 24h
  session_usd: number
  total_usd: number
  input_tokens: number
  output_tokens: number
}

// What the host (Electron preload, or nothing in a plain browser) offers.
export interface HostConfig {
  apiUrl: string
  token: string
  fixture?: Snapshot // smoke/offline runs: render this before (or instead of) the live feed
  smoke?: boolean
  demo?: boolean // ?fixture=chat screenshots: the fixture snapshot and a fake chat engine, no cmdrd
  localHost?: string // the machine the app runs on; cmdrd on another one is a remote connection
}

export interface CommanderHost {
  platform: string
  getConfig(): Promise<HostConfig | null>
  onShortcut(cb: (n: number) => void): () => void
  rendered(report: { ok: boolean; detail: string }): void
  webview?: WebviewBridge // inspector webviews (Electron only)
}

export interface SnapWatch {
  checked_at?: number // unix s of the watch's last look
  checkin_at?: number // unix s of the Manager's next check-in
  operators: { role: string; bead_id: string; active_at?: number; since: number }[]
  finished?: { role: string; bead_id: string; at: number; died?: boolean; report?: string }[] // Operators that ended in the last ~10 min
  issues?: { workstream: string; text: string; since: number }[] // Commander's watch: what is wrong across workstreams
}
