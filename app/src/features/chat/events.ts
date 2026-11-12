// Normalized chat event contract + a pure reducer.
//
// cmdrd (internal/chat) owns every Claude process and emits these events over
// `GET /api/chat/{role}/events`. The shapes are derived from Claude Code's
// stream-json output (see app/src/features/chat/__tests__/testdata/*.jsonl and adapter.ts,
// which maps the raw lines into this model). Every event carries `seq`
// (monotonic per session, used for `?since=` resume) and `session`.

export type Origin = 'user' | 'mail' | 'hook' | 'system' | 'replay'

export interface SlashCommand {
  name: string
  description?: string
}

export interface PermissionSuggestion {
  type: string // addRules | replaceRules | addDirectories | setMode | ...
  destination?: string
  behavior?: string
  mode?: string
  rules?: { toolName: string; ruleContent?: string }[]
  directories?: string[]
}

export interface QuestionOption {
  label: string
  description?: string
}

export interface Question {
  question: string
  header?: string
  options: QuestionOption[]
  multiSelect?: boolean
}

export interface Usage {
  input: number
  output: number
  cacheRead: number
  cacheCreate: number
}

export interface RateWindow {
  utilization: number // 0..1
  resetsAt?: number // unix seconds
}

interface Base {
  seq: number
  session: string
  ts?: number // epoch ms
  parent?: string | null // parent tool_use id (sub-agent events)
}

export type ChatEvent = Base &
  (
    | { kind: 'init'; model?: string; cwd?: string; permissionMode?: string; commands?: SlashCommand[]; version?: string; fresh?: boolean }
    | { kind: 'user_message'; id: string; text: string; origin?: Origin; clientId?: string; attachments?: ItemAttachment[]; history?: boolean }
    | { kind: 'text_delta'; message: string; index: number; text: string }
    | { kind: 'text_final'; message: string; index: number; text: string }
    | { kind: 'thinking'; message: string; index: number; tokens?: number; text?: string; durationMs?: number; done?: boolean }
    | { kind: 'tool_start'; message: string; index: number; id: string; name: string; input?: unknown }
    | { kind: 'tool_input_delta'; id: string; partial: string }
    | { kind: 'tool_input'; id: string; input: unknown }
    | { kind: 'tool_result'; id: string; output: string; isError: boolean; structured?: unknown; decision?: string }
    | { kind: 'permission_request'; id: string; toolUseId?: string; tool: string; input: unknown; description?: string; suggestions?: PermissionSuggestion[]; blockedPath?: string }
    | { kind: 'question'; id: string; toolUseId?: string; questions: Question[] }
    | { kind: 'plan'; id: string; toolUseId?: string; plan: string }
    | { kind: 'request_resolved'; id: string; behavior: 'allow' | 'deny'; message?: string; always?: boolean }
    | { kind: 'status'; state: 'requesting' | 'compacting' | 'idle' }
    | { kind: 'task'; taskId: string; toolUseId?: string; description?: string; status: 'running' | 'completed' | 'failed' | 'killed'; summary?: string; lastTool?: string }
    | { kind: 'compact'; trigger?: string; preTokens?: number; postTokens?: number }
    | { kind: 'context'; tokens: number; window?: number }
    | { kind: 'rate_limit'; status: string; windows: Record<string, RateWindow> }
    | { kind: 'turn_result'; subtype: string; isError: boolean; durationMs?: number; numTurns?: number; costUsd?: number; usage?: Usage; contextWindow?: number; terminalReason?: string; localCommand?: string; denials?: number }
    | { kind: 'notice'; level: 'info' | 'warn' | 'error'; text: string; key?: string }
    | { kind: 'api_retry'; attempt: number; maxRetries?: number; delayMs?: number; error?: string }
    | { kind: 'interrupted' }
    | { kind: 'agent_idle' }
    | { kind: 'prompt_read' } // the CLI read the oldest waiting message into the turn
    | { kind: 'error'; message: string }
    | { kind: 'exit'; code?: number | null; signal?: string | null }
  )

// EventInput is a ChatEvent before seq/session are stamped on it.
export type EventInput = ChatEvent extends infer T ? (T extends ChatEvent ? Omit<T, 'seq' | 'session'> : never) : never

export type EventKind = ChatEvent['kind']
export type EventOf<K extends EventKind> = Extract<ChatEvent, { kind: K }>

// Local (UI-only) actions mixed into the same reducer.
export type LocalAction =
  | { kind: 'local_send'; clientId: string; text: string; attachments?: ItemAttachment[]; ts?: number }
  | { kind: 'local_delivered'; clientId: string; via: 'mail'; to: string } // sent, but by mail (no native process to echo it)
  | { kind: 'local_resolve'; id: string; behavior: 'allow' | 'deny'; always?: boolean; message?: string }
  | { kind: 'local_reset' }

// ---- timeline items -------------------------------------------------------

export interface ItemBase {
  id: string
  ts?: number
  parent?: string | null
}

// A file sent with a message. dataUrl is only there for what this session sent
// (the thumbnail); history has the name and type.
export interface ItemAttachment {
  name: string
  mediaType: string
  dataUrl?: string
}

export interface UserItem extends ItemBase {
  type: 'user'
  text: string
  origin: Origin
  pending?: boolean // optimistic, not yet echoed by the engine
  clientId?: string
  queued?: boolean
  attachments?: ItemAttachment[]
  deliveredVia?: 'mail' // reached a terminal chat by mail, not this stream
  deliveredTo?: string
}

export interface TextItem extends ItemBase {
  type: 'text'
  text: string
  streaming: boolean
}

export interface ThinkingItem extends ItemBase {
  type: 'thinking'
  text: string
  tokens?: number
  durationMs?: number
  done: boolean
}

export type ToolStatus = 'input' | 'running' | 'done' | 'error' | 'denied' | 'interrupted'

export interface ToolItem extends ItemBase {
  type: 'tool'
  name: string
  input: unknown
  inputPartial: string
  status: ToolStatus
  output?: string
  structured?: unknown
  endedAt?: number
  children: string[] // item ids of nested sub-agent events
  task?: { taskId: string; status: string; description?: string; summary?: string; lastTool?: string }
  approval?: 'allow' | 'always' | 'deny'
  awaiting?: boolean // a permission request for this call is open
}

export interface RequestItem extends ItemBase {
  type: 'permission' | 'question' | 'plan'
  requestId: string
  toolUseId?: string
  tool: string
  input: unknown
  description?: string
  suggestions: PermissionSuggestion[]
  blockedPath?: string
  questions: Question[]
  plan: string
  resolved?: { behavior: 'allow' | 'deny'; always?: boolean; message?: string }
}

export interface SystemItem extends ItemBase {
  type: 'system'
  variant: 'mail' | 'hook' | 'notice' | 'warn' | 'error' | 'compact' | 'interrupted' | 'exit' | 'summary' | 'command'
  text: string
  detail?: string
}

export interface TurnItem extends ItemBase {
  type: 'turn'
  durationMs?: number
  costDelta?: number
  outputTokens?: number
  isError: boolean
  terminalReason?: string
  subtype: string
  // An "Interrupted" divider already marks this stopped turn: no second marker.
  shownAsInterrupted?: boolean
}

export type Item = UserItem | TextItem | ThinkingItem | ToolItem | RequestItem | SystemItem | TurnItem

export interface ChatState {
  session: string
  lastSeq: number
  order: string[] // top-level item ids in display order
  items: Record<string, Item>
  model?: string
  cwd?: string
  permissionMode?: string
  commands: SlashCommand[]
  phase: 'idle' | 'requesting' | 'compacting'
  turnOpen: boolean
  turnStartTs?: number
  queue: string[] // queued user item ids
  readsPrompts?: boolean // the runtime reports each message it reads (prompt_read); Codex does not
  pendingRequests: string[] // unresolved permission/question/plan item ids
  runningTools: string[]
  tasks: Record<string, { status: string; description?: string; toolUseId?: string }>
  costUsd: number // cumulative session cost as reported by Claude
  usage: Usage // summed over turns
  contextTokens?: number
  contextWindow?: number
  rateLimit?: { status: string; windows: Record<string, RateWindow> }
  apiRetry?: { attempt: number; maxRetries?: number; delayMs?: number; error?: string; firstTs: number; ts: number } // the turn is waiting on Anthropic API retries
  compactions: number
  alive: boolean
  lastError?: string
}

export function initialState(session = ''): ChatState {
  return {
    session,
    lastSeq: 0,
    order: [],
    items: {},
    commands: [],
    phase: 'idle',
    turnOpen: false,
    queue: [],
    pendingRequests: [],
    runningTools: [],
    tasks: {},
    costUsd: 0,
    usage: { input: 0, output: 0, cacheRead: 0, cacheCreate: 0 },
    compactions: 0,
    alive: true,
  }
}

// ---- classification helpers ----------------------------------------------

const INTERRUPT_TEXT = '[Request interrupted by user'

// classifyUserText decides how a user-role message is shown: mail notices and
// hook-injected feedback are system rows, not user bubbles.
export function classifyUserText(text: string, origin?: Origin): Origin {
  const t = text.trimStart()
  // A compaction summary is the system's, whatever origin its transcript line carried.
  if (/^This session is being continued from a previous conversation/.test(t)) return 'system'
  if (origin && origin !== 'user') return origin
  if (/^\[mail\]/i.test(t) || /^<mail\b/i.test(t) || /^MAIL from /.test(t) || /^You have mail from /.test(t)) return 'mail'
  if (/^Stop hook feedback:/.test(t) || /^<system-reminder>/.test(t)) return 'hook'
  if (/^This session is being continued from a previous conversation/.test(t)) return 'system'
  if (/^<local-command-std(out|err)>/.test(t)) return 'replay'
  return 'user'
}

export function stripLocalCommand(text: string): string {
  return text.replace(/<\/?local-command-std(out|err)>/g, '').trim()
}

// ---- reducer ---------------------------------------------------------------

// A Draft is a state whose top-level maps were cloned once for a batch, so a
// batch of N events costs O(N) instead of O(N * items). Items are replaced
// (never mutated) so React.memo on item identity stays correct.
type Draft = ChatState

// The engine's echo knows names and types; the optimistic bubble has the thumbnails.
function withThumbs(echo?: ItemAttachment[], local?: ItemAttachment[]): ItemAttachment[] | undefined {
  if (!echo) return local
  return echo.map((a, i) => (local?.[i]?.name === a.name ? { ...a, dataUrl: local[i].dataUrl } : a))
}

function begin(s: ChatState): Draft {
  return {
    ...s,
    order: s.order.slice(),
    items: { ...s.items },
    queue: s.queue.slice(),
    pendingRequests: s.pendingRequests.slice(),
    runningTools: s.runningTools.slice(),
    tasks: { ...s.tasks },
    commands: s.commands,
    usage: { ...s.usage },
  }
}

function put(d: Draft, item: Item) {
  const isNew = !d.items[item.id]
  d.items[item.id] = item
  if (!isNew) return
  if (item.parent && d.items[item.parent]?.type === 'tool') {
    const parent = d.items[item.parent] as ToolItem
    d.items[parent.id] = { ...parent, children: [...parent.children, item.id] }
  } else {
    d.order.push(item.id)
  }
}

function patch<T extends Item>(d: Draft, id: string, fn: (it: T) => Partial<T>) {
  const it = d.items[id] as T | undefined
  if (!it) return
  d.items[id] = { ...it, ...fn(it) } as Item
}

function remove<T>(arr: T[], v: T) {
  const i = arr.indexOf(v)
  if (i >= 0) arr.splice(i, 1)
}

function sys(d: Draft, e: { seq: number; ts?: number; parent?: string | null }, variant: SystemItem['variant'], text: string, detail?: string) {
  put(d, { type: 'system', id: `sys-${e.seq}`, ts: e.ts, parent: e.parent, variant, text, detail })
}

function finishStreaming(d: Draft) {
  // Close any text/thinking still marked streaming (e.g. after interrupt).
  for (const id of d.order.slice(-40)) {
    const it = d.items[id]
    if (it?.type === 'text' && it.streaming) d.items[id] = { ...it, streaming: false }
    if (it?.type === 'thinking' && !it.done) d.items[id] = { ...it, done: true }
  }
}

function startTurn(d: Draft, ts?: number) {
  if (!d.turnOpen) {
    d.turnOpen = true
    d.turnStartTs = ts
  }
}

// Real output (or the end of the turn) means the API retries are over.
const ENDS_RETRY = new Set<string>(['text_delta', 'text_final', 'thinking', 'tool_start', 'tool_input', 'tool_result', 'turn_result', 'interrupted', 'error', 'exit'])

function apply(d: Draft, e: ChatEvent | LocalAction): void {
  if ('seq' in e) {
    if (e.seq <= d.lastSeq && d.lastSeq > 0) return // duplicate after reconnect
    d.lastSeq = e.seq
    if (e.session) d.session = e.session
  }
  if (d.apiRetry && ENDS_RETRY.has(e.kind)) d.apiRetry = undefined
  switch (e.kind) {
    case 'local_reset':
      Object.assign(d, initialState(d.session))
      return
    case 'local_send': {
      const id = `local-${e.clientId}`
      put(d, { type: 'user', id, ts: e.ts, text: e.text, origin: 'user', pending: true, clientId: e.clientId, queued: d.turnOpen, attachments: e.attachments })
      return
    }
    case 'local_delivered':
      patch<UserItem>(d, `local-${e.clientId}`, () => ({ pending: false, queued: false, deliveredVia: e.via, deliveredTo: e.to }))
      return
    case 'local_resolve':
    case 'request_resolved': {
      const itemId = `req-${e.id}`
      patch<RequestItem>(d, itemId, () => ({ resolved: { behavior: e.behavior, always: e.always, message: e.message } }))
      remove(d.pendingRequests, itemId)
      const req = d.items[itemId] as RequestItem | undefined
      if (req?.toolUseId) patch<ToolItem>(d, req.toolUseId, () => ({ awaiting: false, approval: e.behavior === 'deny' ? 'deny' : e.always ? 'always' : 'allow' }))
      return
    }
    case 'init':
      if (e.model) d.model = e.model
      if (e.cwd) d.cwd = e.cwd
      if (e.permissionMode) d.permissionMode = e.permissionMode
      if (e.commands?.length) d.commands = e.commands
      d.alive = true
      // A freshly spawned process has no turn open: whatever the hydrated
      // history left running (it has no turn results) ended with the old one.
      if (e.fresh) {
        finishStreaming(d)
        for (const id of d.runningTools) patch<ToolItem>(d, id, (it) => ({ status: it.status === 'input' || it.status === 'running' ? 'interrupted' : it.status }))
        d.runningTools = []
        d.turnOpen = false
        d.phase = 'idle'
        d.turnStartTs = undefined
      }
      return
    case 'user_message': {
      const origin = classifyUserText(e.text, e.origin)
      if (origin === 'user') {
        // Replace an optimistic bubble (same clientId, or same text) if any.
        const local = d.order.find((id) => {
          const it = d.items[id]
          return it?.type === 'user' && it.pending && (e.clientId ? it.clientId === e.clientId : it.text === e.text)
        })
        const queued = d.turnOpen && !e.history // history has no turn results to close turns
        const item: UserItem = { type: 'user', id: e.id, ts: e.ts, parent: e.parent, text: e.text, origin, queued, attachments: withThumbs(e.attachments, local ? (d.items[local] as UserItem).attachments : undefined) }
        if (local) {
          const at = d.order.indexOf(local)
          delete d.items[local]
          remove(d.queue, local)
          d.order[at] = e.id
          d.items[e.id] = item
        } else {
          put(d, item)
        }
        if (queued) d.queue.push(e.id)
        else startTurn(d, e.ts)
        return
      }
      if (e.text.startsWith(INTERRUPT_TEXT)) {
        sys(d, e, 'interrupted', 'Interrupted')
        return
      }
      if (origin === 'mail') return sys(d, e, 'mail', e.text) // parsed for display by parseMail
      // Hook context is written for the agent (board lines, reminders), not the user.
      if (origin === 'hook') return
      if (origin === 'system') return sys(d, e, 'summary', 'Summary of the earlier conversation', e.text)
      return sys(d, e, 'command', stripLocalCommand(e.text))
    }
    case 'api_retry': {
      const ts = ('ts' in e && e.ts) || Date.now()
      d.apiRetry = { attempt: e.attempt, maxRetries: e.maxRetries, delayMs: e.delayMs, error: e.error, firstTs: d.apiRetry?.firstTs ?? ts, ts }
      return
    }
    case 'interrupted':
      finishStreaming(d)
      sys(d, e, 'interrupted', 'Interrupted')
      return
    case 'text_delta': {
      const id = `${e.message}:${e.index}`
      const it = d.items[id]
      if (it?.type === 'text') d.items[id] = { ...it, text: it.text + e.text }
      else put(d, { type: 'text', id, ts: e.ts, parent: e.parent, text: e.text, streaming: true })
      startTurn(d, e.ts)
      return
    }
    case 'text_final': {
      const id = `${e.message}:${e.index}`
      const it = d.items[id]
      if (it?.type === 'text') d.items[id] = { ...it, text: e.text, streaming: false }
      else if (e.text) put(d, { type: 'text', id, ts: e.ts, parent: e.parent, text: e.text, streaming: false })
      return
    }
    case 'thinking': {
      const id = `${e.message}:${e.index}`
      const it = d.items[id]
      if (it?.type === 'thinking') {
        d.items[id] = {
          ...it,
          text: e.text ? (e.done ? e.text : it.text + e.text) : it.text,
          tokens: e.tokens ?? it.tokens,
          durationMs: e.durationMs ?? it.durationMs,
          done: e.done ?? it.done,
        }
      } else {
        put(d, { type: 'thinking', id, ts: e.ts, parent: e.parent, text: e.text ?? '', tokens: e.tokens, durationMs: e.durationMs, done: !!e.done })
      }
      startTurn(d, e.ts)
      return
    }
    case 'tool_start': {
      if (!d.items[e.id]) {
        put(d, { type: 'tool', id: e.id, ts: e.ts, parent: e.parent, name: e.name, input: e.input ?? {}, inputPartial: '', status: 'input', children: [] })
        d.runningTools.push(e.id)
      }
      startTurn(d, e.ts)
      return
    }
    case 'tool_input_delta':
      patch<ToolItem>(d, e.id, (it) => ({ inputPartial: it.inputPartial + e.partial }))
      return
    case 'tool_input':
      patch<ToolItem>(d, e.id, (it) => ({ input: e.input, status: it.status === 'input' ? 'running' : it.status }))
      return
    case 'tool_result': {
      const denied = e.decision === 'reject'
      const interrupted = isInterruptedResult(e.structured)
      patch<ToolItem>(d, e.id, () => ({
        output: e.output,
        structured: e.structured,
        status: denied ? 'denied' : interrupted ? 'interrupted' : e.isError ? 'error' : 'done',
        endedAt: e.ts,
      }))
      remove(d.runningTools, e.id)
      return
    }
    case 'permission_request':
    case 'question':
    case 'plan': {
      const id = `req-${e.id}`
      const item: RequestItem = {
        type: e.kind === 'permission_request' ? 'permission' : e.kind,
        id,
        ts: e.ts,
        parent: e.parent,
        requestId: e.id,
        toolUseId: e.toolUseId,
        tool: e.kind === 'permission_request' ? e.tool : e.kind === 'question' ? 'AskUserQuestion' : 'ExitPlanMode',
        input: e.kind === 'permission_request' ? e.input : {},
        description: e.kind === 'permission_request' ? e.description : undefined,
        suggestions: e.kind === 'permission_request' ? e.suggestions ?? [] : [],
        blockedPath: e.kind === 'permission_request' ? e.blockedPath : undefined,
        questions: e.kind === 'question' ? e.questions : [],
        plan: e.kind === 'plan' ? e.plan : '',
      }
      put(d, { ...item, parent: null }) // approvals always surface at top level
      if (e.kind === 'permission_request' && e.toolUseId) patch<ToolItem>(d, e.toolUseId, () => ({ awaiting: true }))
      if (!d.pendingRequests.includes(id)) d.pendingRequests.push(id)
      return
    }
    case 'status':
      d.phase = e.state
      if (e.state === 'requesting') startTurn(d, e.ts)
      return
    case 'task': {
      d.tasks[e.taskId] = { status: e.status, description: e.description ?? d.tasks[e.taskId]?.description, toolUseId: e.toolUseId ?? d.tasks[e.taskId]?.toolUseId }
      const toolId = e.toolUseId ?? d.tasks[e.taskId]?.toolUseId
      if (toolId) {
        patch<ToolItem>(d, toolId, (it) => ({
          task: { taskId: e.taskId, status: e.status, description: e.description ?? it.task?.description, summary: e.summary ?? it.task?.summary, lastTool: e.lastTool ?? it.task?.lastTool },
        }))
      }
      return
    }
    case 'compact':
      d.compactions++
      if (e.postTokens) d.contextTokens = e.postTokens
      sys(d, e, 'compact', 'Context compacted', e.preTokens && e.postTokens ? `${fmtTokens(e.preTokens)} → ${fmtTokens(e.postTokens)} tokens (${e.trigger ?? 'auto'})` : e.trigger)
      return
    case 'context':
      if (!e.parent) {
        d.contextTokens = e.tokens
        if (e.window) d.contextWindow = e.window
      }
      return
    case 'rate_limit':
      d.rateLimit = { status: e.status, windows: e.windows }
      return
    case 'turn_result': {
      finishStreaming(d)
      const prevCost = d.costUsd
      if (e.costUsd != null) d.costUsd = Math.max(d.costUsd, e.costUsd)
      if (e.usage) {
        d.usage.input += e.usage.input
        d.usage.output += e.usage.output
        d.usage.cacheRead += e.usage.cacheRead
        d.usage.cacheCreate += e.usage.cacheCreate
      }
      if (e.contextWindow) d.contextWindow = e.contextWindow
      // Tools still running when the turn ends were cut off.
      for (const id of d.runningTools) patch<ToolItem>(d, id, (it) => ({ status: it.status === 'input' || it.status === 'running' ? 'interrupted' : it.status }))
      d.runningTools = []
      if (!e.localCommand) {
        put(d, {
          type: 'turn',
          id: `turn-${e.seq}`,
          ts: e.ts,
          durationMs: e.durationMs,
          costDelta: e.costUsd != null ? Math.max(0, e.costUsd - prevCost) : undefined,
          outputTokens: e.usage?.output,
          isError: e.isError,
          terminalReason: e.terminalReason,
          subtype: e.subtype,
          shownAsInterrupted: isAborted(e.terminalReason) && interruptedThisTurn(d),
        })
      }
      d.phase = 'idle'
      d.turnOpen = false
      d.turnStartTs = undefined
      releaseUnread(d)
      return
    }
    case 'agent_idle': {
      finishStreaming(d)
      for (const id of d.runningTools) patch<ToolItem>(d, id, () => ({ status: 'interrupted' }))
      d.runningTools = []
      releaseUnread(d)
      d.turnOpen = false
      d.phase = 'idle'
      d.turnStartTs = undefined
      return
    }
    case 'prompt_read': {
      d.readsPrompts = true
      // A message sent mid-turn joins the thread where the agent read it, so
      // the reply that answers it comes after it.
      const next = d.queue.shift()
      if (next) {
        remove(d.order, next)
        d.order.push(next)
        patch<UserItem>(d, next, () => ({ queued: false }))
      }
      return
    }
    case 'notice':
      sys(d, e, e.level === 'error' ? 'error' : e.level === 'warn' ? 'warn' : 'notice', e.text)
      return
    case 'error':
      d.lastError = e.message
      sys(d, e, 'error', e.message)
      return
    case 'exit':
      finishStreaming(d)
      d.alive = false
      d.turnOpen = false
      d.phase = 'idle'
      for (const id of d.runningTools) patch<ToolItem>(d, id, () => ({ status: 'interrupted' }))
      d.runningTools = []
      // A clean exit is a restart: the app's start screen covers it, and the
      // chat carries on. Only a crash is worth a line in the conversation.
      if (e.code !== 0 || e.signal) sys(d, e, 'exit', 'The session stopped unexpectedly')
      return
  }
}

function isAborted(reason?: string): boolean {
  return reason === 'aborted_streaming' || reason === 'aborted_tools'
}

// interruptedThisTurn: an "Interrupted" divider sits after the last message the
// user sent.
function interruptedThisTurn(d: Draft): boolean {
  for (let i = d.order.length - 1; i >= 0; i--) {
    const it = d.items[d.order[i]]
    if (!it || it.type === 'user' || it.type === 'turn') return false
    if (it.type === 'system' && it.variant === 'interrupted') return true
  }
  return false
}

function isInterruptedResult(s: unknown): boolean {
  return !!s && typeof s === 'object' && (s as { interrupted?: boolean }).interrupted === true
}

export function reduce(state: ChatState, e: ChatEvent | LocalAction): ChatState {
  const d = begin(state)
  apply(d, e)
  return d
}

export function reduceAll(state: ChatState, events: (ChatEvent | LocalAction)[]): ChatState {
  if (events.length === 0) return state
  const d = begin(state)
  for (const e of events) apply(d, e)
  return d
}

// ---- derived status -------------------------------------------------------

export interface DerivedStatus {
  state: 'idle' | 'working' | 'waiting' | 'compacting' | 'exited'
  label: string
  detail?: string
  subAgents: number
}

export function deriveStatus(s: ChatState): DerivedStatus {
  const subAgents = Object.values(s.tasks).filter((t) => t.status === 'running').length
  if (!s.alive) return { state: 'exited', label: 'Exited', subAgents }
  if (s.pendingRequests.length) {
    const it = s.items[s.pendingRequests[0]] as RequestItem
    const what = it.type === 'question' ? 'question' : it.type === 'plan' ? 'plan approval' : `approval: ${it.tool}`
    return { state: 'waiting', label: 'Needs you', detail: what, subAgents }
  }
  if (s.phase === 'compacting') return { state: 'compacting', label: 'Compacting', subAgents }
  if (s.turnOpen || s.phase === 'requesting') {
    const tool = s.runningTools.map((id) => s.items[id]).find((it): it is ToolItem => it?.type === 'tool' && !it.parent)
    if (tool && (tool.name === 'Task' || tool.name === 'Agent')) return { state: 'working', label: 'Working', detail: `sub-agent: ${toolHeadline(tool.name, tool.input)}`, subAgents }
    if (tool) return { state: 'working', label: 'Working', detail: toolHeadline(tool.name, tool.input), subAgents }
    if (subAgents) return { state: 'working', label: 'Working', detail: `waiting on ${subAgents} sub-agent${subAgents > 1 ? 's' : ''}`, subAgents }
    return { state: 'working', label: 'Working', subAgents }
  }
  if (subAgents) return { state: 'working', label: 'Background', detail: `${subAgents} sub-agent${subAgents > 1 ? 's' : ''} running`, subAgents }
  return { state: 'idle', label: 'Idle', subAgents }
}

// ---- shared formatting ------------------------------------------------------

export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k`
  return String(n)
}

export function fmtCost(usd: number): string {
  if (usd === 0) return '$0'
  if (usd < 0.01) return `$${usd.toFixed(4)}`
  return `$${usd.toFixed(2)}`
}

export function fmtDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`
  const m = Math.floor(ms / 60_000)
  return `${m}m ${Math.round((ms % 60_000) / 1000)}s`
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

export function shortPath(p: string): string {
  const parts = p.split('/')
  return parts.length > 3 ? `…/${parts.slice(-2).join('/')}` : p
}

// toolHeadline: the one-line summary shown on a tool card and in the status strip.
export function toolHeadline(name: string, input: unknown): string {
  const i = (input ?? {}) as Record<string, unknown>
  switch (name) {
    case 'Bash':
      return str(i.description) || str(i.command)
    case 'Read':
    case 'Write':
    case 'Edit':
    case 'MultiEdit':
    case 'NotebookEdit':
      return shortPath(str(i.file_path) || str(i.notebook_path))
    case 'Grep':
      return `"${str(i.pattern)}"${i.path ? ` in ${shortPath(str(i.path))}` : ''}`
    case 'Glob':
      return str(i.pattern)
    case 'WebFetch':
      return str(i.url)
    case 'WebSearch':
      return str(i.query)
    case 'Task':
    case 'Agent':
      return str(i.description) || str(i.subagent_type)
    case 'TodoWrite':
      return `${Array.isArray(i.todos) ? i.todos.length : 0} todos`
    default:
      return ''
  }
}

export function toolLabel(name: string): string {
  if (name.startsWith('mcp__')) {
    const [, server, ...rest] = name.split('__')
    return `${server} · ${rest.join('__')}`
  }
  return name
}

// releaseUnread ends the queue when a turn ends without reading what was sent
// during it: those messages come after everything that turn wrote, so the
// reply that answers them follows them instead of a wrap-up seeming to.
function releaseUnread(d: Draft) {
  for (const id of d.queue) {
    // A runtime that does not report reads may have answered it in that turn.
    if (d.readsPrompts) {
      remove(d.order, id)
      d.order.push(id)
    }
    patch<UserItem>(d, id, () => ({ queued: false }))
  }
  d.queue = []
}
