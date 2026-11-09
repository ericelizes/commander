// adapter.ts maps RAW Claude Code stream-json lines (as recorded by
// spikes/native-chat/driver.mjs) into normalized ChatEvents, and holds the one
// small `fromEngine` function that adapts cmdrd's (internal/chat) wire format.
import type { ChatEvent, EventInput, PermissionSuggestion, Question, RateWindow, SlashCommand, Usage } from './events'

type Raw = Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any
type Emit = EventInput

// Commands that work in `-p` stream mode (picker commands like /model don't).
export const FALLBACK_COMMANDS: SlashCommand[] = [
  { name: 'compact', description: 'Summarize the conversation to free context' },
  { name: 'context', description: 'Show context window usage' },
  { name: 'cost', description: 'Show session cost and plan usage' },
  { name: 'init', description: 'Write a CLAUDE.md for this repo' },
  { name: 'review', description: 'Review a pull request' },
  { name: 'security-review', description: 'Security review of pending changes' },
]

function textOf(content: unknown): string {
  if (typeof content === 'string') return content
  if (Array.isArray(content)) {
    return content
      .map((c: Raw) => (c?.type === 'text' ? c.text : c?.type === 'image' ? '[image]' : ''))
      .filter(Boolean)
      .join('\n')
  }
  return ''
}

function usageOf(u: Raw | undefined): Usage | undefined {
  if (!u) return undefined
  return {
    input: u.input_tokens ?? 0,
    output: u.output_tokens ?? 0,
    cacheRead: u.cache_read_input_tokens ?? 0,
    cacheCreate: u.cache_creation_input_tokens ?? 0,
  }
}

interface MsgTrack {
  streamed: boolean
  lastStarted: number
  snapshots: number
  blocks: Record<number, { type: string; id?: string }>
}

export class RawAdapter {
  private seq = 0
  private session = ''
  private msgs = new Map<string, MsgTrack>()
  private currentThinking?: { message: string; index: number; parent: string | null }
  private toolIdByIndex = new Map<string, string>() // `${msg}:${index}` -> tool_use id
  private base = Date.parse('2026-10-20T21:45:00Z')
  private pendingTools = new Map<string, { tool: string; requestId: string }>()

  constructor(opts: { session?: string; baseTs?: number } = {}) {
    if (opts.session) this.session = opts.session
    if (opts.baseTs) this.base = opts.baseTs
  }

  private track(id: string): MsgTrack {
    let m = this.msgs.get(id)
    if (!m) {
      m = { streamed: false, lastStarted: -1, snapshots: 0, blocks: {} }
      this.msgs.set(id, m)
    }
    return m
  }

  // line: one recorded line ({t, dir, ev|msg}) or a bare raw stream-json event
  // (treated as dir "out"). Returns zero or more normalized events.
  push(line: string | Raw): ChatEvent[] {
    let rec: Raw
    if (typeof line === 'string') {
      if (!line.trim()) return []
      try {
        rec = JSON.parse(line)
      } catch {
        return []
      }
    } else rec = line
    const dir: string = rec.dir ?? 'out'
    const ts = typeof rec.t === 'number' ? this.base + rec.t : Date.now()
    const out: Emit[] = []
    if (dir === 'in') this.input(rec.msg ?? rec, ts, out)
    else if (dir === 'out') this.output(rec.ev ?? rec, ts, out)
    else if (dir === 'exit') out.push({ kind: 'exit', code: rec.code, signal: rec.sig, ts })
    else if (dir === 'stderr' && rec.text?.trim()) out.push({ kind: 'notice', level: 'warn', text: String(rec.text).trim(), ts })
    return out.map((e) => ({ ...e, seq: ++this.seq, session: this.session }) as ChatEvent)
  }

  pushAll(lines: (string | Raw)[]): ChatEvent[] {
    return lines.flatMap((l) => this.push(l))
  }

  // Messages the UI wrote to stdin (user prompts, permission answers).
  private input(m: Raw, ts: number, out: Emit[]) {
    if (m.type === 'user') {
      const text = textOf(m.message?.content)
      out.push({ kind: 'user_message', id: m.uuid ?? `u${this.seq + 1}`, text, ts })
    } else if (m.type === 'control_response') {
      const r = m.response?.response ?? {}
      const behavior = r.behavior === 'allow' ? 'allow' : 'deny'
      out.push({ kind: 'request_resolved', id: m.response?.request_id, behavior, message: r.message, always: Array.isArray(r.updatedPermissions) && r.updatedPermissions.length > 0, ts })
    }
  }

  private output(ev: Raw, ts: number, out: Emit[]) {
    if (ev.session_id) this.session = ev.session_id
    const parent: string | null = ev.parent_tool_use_id ?? null
    switch (ev.type) {
      case 'system':
        return this.system(ev, ts, parent, out)
      case 'stream_event':
        return this.stream(ev, ts, parent, out)
      case 'assistant':
        return this.assistant(ev, ts, parent, out)
      case 'user':
        return this.user(ev, ts, parent, out)
      case 'control_request':
        return this.control(ev, ts, out)
      case 'rate_limit_event': {
        const info = ev.rate_limit_info ?? {}
        const windows: Record<string, RateWindow> = {}
        for (const [k, v] of Object.entries<Raw>(info.unifiedWindows ?? {})) windows[k] = { utilization: v.utilization ?? 0, resetsAt: v.resetsAt }
        if (!Object.keys(windows).length && info.rateLimitType) windows[info.rateLimitType] = { utilization: info.utilization ?? 0, resetsAt: info.resetsAt }
        out.push({ kind: 'rate_limit', status: info.status ?? 'allowed', windows, ts })
        return
      }
      case 'result': {
        const mu = Object.values<Raw>(ev.modelUsage ?? {})[0]
        out.push({
          kind: 'turn_result',
          subtype: ev.subtype,
          isError: !!ev.is_error,
          durationMs: ev.duration_ms,
          numTurns: ev.num_turns,
          costUsd: ev.total_cost_usd,
          usage: ev.local_command ? undefined : usageOf(ev.usage),
          contextWindow: mu?.contextWindow,
          terminalReason: ev.terminal_reason,
          localCommand: ev.local_command,
          denials: ev.permission_denials?.length,
          ts,
        })
        return
      }
    }
  }

  private system(ev: Raw, ts: number, parent: string | null, out: Emit[]) {
    switch (ev.subtype) {
      case 'init': {
        const cmds: SlashCommand[] = Array.isArray(ev.slash_commands)
          ? ev.slash_commands.map((c: string | Raw) => (typeof c === 'string' ? { name: c.replace(/^\//, '') } : { name: String(c.name).replace(/^\//, ''), description: c.description }))
          : FALLBACK_COMMANDS
        out.push({ kind: 'init', model: ev.model, cwd: ev.cwd, permissionMode: ev.permissionMode, commands: cmds, version: ev.claude_code_version, ts })
        return
      }
      case 'status':
        out.push({ kind: 'status', state: ev.status === 'requesting' ? 'requesting' : ev.status === 'compacting' ? 'compacting' : 'idle', ts })
        return
      case 'thinking_tokens':
        if (this.currentThinking) out.push({ kind: 'thinking', ...this.currentThinking, tokens: ev.estimated_tokens, ts })
        return
      case 'compact_boundary': {
        const m = ev.compact_metadata ?? {}
        out.push({ kind: 'compact', trigger: m.trigger, preTokens: m.pre_tokens, postTokens: m.post_tokens, ts })
        return
      }
      case 'notification':
        out.push({ kind: 'notice', level: ev.priority === 'immediate' ? 'warn' : 'info', text: String(ev.text ?? '').replace(/ · ctrl\+o to see$/, ''), key: ev.key, ts })
        return
      case 'hook_response': {
        // UserPromptSubmit additionalContext is how mail reaches the model.
        let ctx = ''
        try {
          ctx = JSON.parse(ev.output || ev.stdout || '{}')?.hookSpecificOutput?.additionalContext ?? ''
        } catch {
          /* not JSON */
        }
        if (ctx) out.push({ kind: 'user_message', id: ev.uuid ?? `h${this.seq + 1}`, text: ctx, origin: /mail/i.test(ctx) ? 'mail' : 'hook', ts })
        if (ev.exit_code && ev.exit_code !== 0 && ev.outcome !== 'success') out.push({ kind: 'notice', level: 'warn', text: `${ev.hook_name} hook failed (exit ${ev.exit_code})`, ts })
        return
      }
      case 'task_started':
        out.push({ kind: 'task', taskId: ev.task_id, toolUseId: ev.tool_use_id, description: ev.description, status: 'running', parent, ts })
        return
      case 'task_progress':
        out.push({ kind: 'task', taskId: ev.task_id, toolUseId: ev.tool_use_id, description: ev.description, status: 'running', lastTool: ev.last_tool_name, parent, ts })
        return
      case 'task_notification': {
        const st = ev.status === 'completed' ? 'completed' : ev.status === 'failed' ? 'failed' : 'killed'
        out.push({ kind: 'task', taskId: ev.task_id, toolUseId: ev.tool_use_id, status: st, summary: ev.summary, parent, ts })
        return
      }
    }
  }

  private stream(ev: Raw, ts: number, parent: string | null, out: Emit[]) {
    const se = ev.event ?? {}
    const msg: string = ev.api_message_id ?? se.message?.id ?? 'msg'
    const m = this.track(msg)
    m.streamed = true
    switch (se.type) {
      case 'message_start': {
        const u = se.message?.usage
        if (u && !parent) out.push({ kind: 'context', tokens: (u.input_tokens ?? 0) + (u.cache_read_input_tokens ?? 0) + (u.cache_creation_input_tokens ?? 0), ts })
        return
      }
      case 'content_block_start': {
        const b = se.content_block ?? {}
        m.lastStarted = se.index
        m.blocks[se.index] = { type: b.type, id: b.id }
        if (b.type === 'thinking') {
          this.currentThinking = { message: msg, index: se.index, parent }
          out.push({ kind: 'thinking', message: msg, index: se.index, parent, ts })
        } else if (b.type === 'tool_use' || b.type === 'server_tool_use' || b.type === 'mcp_tool_use') {
          this.toolIdByIndex.set(`${msg}:${se.index}`, b.id)
          out.push({ kind: 'tool_start', message: msg, index: se.index, id: b.id, name: b.name, parent, ts })
        }
        return
      }
      case 'content_block_delta': {
        const d = se.delta ?? {}
        if (d.type === 'text_delta') out.push({ kind: 'text_delta', message: msg, index: se.index, text: d.text, parent, ts })
        else if (d.type === 'thinking_delta' && d.thinking) out.push({ kind: 'thinking', message: msg, index: se.index, text: d.thinking, parent, ts })
        else if (d.type === 'input_json_delta' && d.partial_json) {
          const id = this.toolIdByIndex.get(`${msg}:${se.index}`)
          if (id) out.push({ kind: 'tool_input_delta', id, partial: d.partial_json, ts })
        }
        return
      }
    }
  }

  private assistant(ev: Raw, ts: number, parent: string | null, out: Emit[]) {
    const msg: string = ev.message?.id ?? 'msg'
    const m = this.track(msg)
    const content: Raw[] = ev.message?.content ?? []
    content.forEach((b, i) => {
      // A snapshot carries one finished block; with partial messages it is the
      // block that just streamed, otherwise blocks arrive in order.
      let index = m.streamed && content.length === 1 ? m.lastStarted : m.snapshots + i
      if (b.type === 'tool_use') {
        const known = [...this.toolIdByIndex.entries()].find(([, id]) => id === b.id)
        if (known) index = Number(known[0].split(':').pop())
      }
      if (b.type === 'text') out.push({ kind: 'text_final', message: msg, index, text: b.text ?? '', parent, ts })
      else if (b.type === 'thinking' || b.type === 'redacted_thinking') {
        out.push({ kind: 'thinking', message: msg, index, text: b.thinking || undefined, durationMs: ev.thinking_duration_ms, done: true, parent, ts })
        this.currentThinking = undefined
      } else if (b.type === 'tool_use' || b.type === 'server_tool_use' || b.type === 'mcp_tool_use') {
        if (!this.toolIdByIndex.has(`${msg}:${index}`)) {
          this.toolIdByIndex.set(`${msg}:${index}`, b.id)
          out.push({ kind: 'tool_start', message: msg, index, id: b.id, name: b.name, parent, ts })
        }
        out.push({ kind: 'tool_input', id: b.id, input: b.input ?? {}, ts })
      }
    })
    m.snapshots += content.length
    if (ev.error) out.push({ kind: 'notice', level: 'error', text: String(ev.error), ts })
  }

  private user(ev: Raw, ts: number, parent: string | null, out: Emit[]) {
    const content = ev.message?.content
    if (Array.isArray(content) && content.some((c: Raw) => c?.type === 'tool_result')) {
      const metas: Raw[] = ev.tool_result_meta ?? []
      for (const c of content) {
        if (c?.type !== 'tool_result') continue
        const meta = metas.find((x) => x.id === c.tool_use_id)
        const structured = content.length === 1 ? ev.tool_use_result : undefined
        out.push({ kind: 'tool_result', id: c.tool_use_id, output: textOf(c.content), isError: !!c.is_error, structured, decision: meta?.permission_decision?.decision, parent, ts })
      }
      return
    }
    const text = textOf(content)
    if (!text) return
    if (ev.isReplay && /^<local-command-std/.test(text) === false) return // replayed user prompt
    out.push({ kind: 'user_message', id: ev.uuid ?? `u${this.seq + 1}`, text, origin: ev.isSynthetic || ev.isReplay ? undefined : 'system', parent, ts })
  }

  private control(ev: Raw, ts: number, out: Emit[]) {
    const r = ev.request ?? {}
    if (r.subtype !== 'can_use_tool') return
    const id: string = ev.request_id
    if (r.tool_name === 'AskUserQuestion') {
      const questions: Question[] = (r.input?.questions ?? []).map((q: Raw) => ({
        question: q.question,
        header: q.header,
        multiSelect: !!q.multiSelect,
        options: (q.options ?? []).map((o: Raw) => ({ label: o.label, description: o.description })),
      }))
      out.push({ kind: 'question', id, toolUseId: r.tool_use_id, questions, ts })
    } else if (r.tool_name === 'ExitPlanMode') {
      out.push({ kind: 'plan', id, toolUseId: r.tool_use_id, plan: String(r.input?.plan ?? ''), ts })
    } else {
      out.push({
        kind: 'permission_request',
        id,
        toolUseId: r.tool_use_id,
        tool: r.tool_name,
        input: r.input,
        description: r.description,
        suggestions: (r.permission_suggestions ?? []) as PermissionSuggestion[],
        blockedPath: r.blocked_path,
        ts,
      })
    }
    this.pendingTools.set(id, { tool: r.tool_name, requestId: id })
  }
}

// parseRecording turns a whole recorded .jsonl file into ChatEvents.
export function parseRecording(jsonl: string, opts?: { session?: string; baseTs?: number }): ChatEvent[] {
  return new RawAdapter(opts).pushAll(jsonl.split('\n'))
}

// fromEngine adapts one frame from cmdrd's /api/chat/{role}/events into a
// ChatEvent. cmdrd (internal/chat) emits the normalized model in events.ts;
// this is the single place to tweak if its field names differ. Frames that
// carry a raw stream-json line ({raw: {...}}) go through a per-role RawAdapter.
const rawAdapters = new Map<string, RawAdapter>()

export function fromEngine(frame: unknown, role = ''): ChatEvent[] {
  if (!frame || typeof frame !== 'object') return []
  const f = frame as Raw
  if (Array.isArray(f)) return f.flatMap((x) => fromEngine(x, role))
  if (f.raw) {
    let a = rawAdapters.get(role)
    if (!a) rawAdapters.set(role, (a = new RawAdapter({ session: f.session })))
    return a.push(f.raw).map((e) => ({ ...e, seq: f.seq ?? e.seq }))
  }
  const kind = f.kind ?? f.type
  if (typeof kind !== 'string' || typeof f.seq !== 'number') return []
  return [{ ...f, kind, session: f.session ?? f.session_id ?? '', ts: f.ts ?? (f.time ? Date.parse(f.time) : undefined) } as ChatEvent]
}
