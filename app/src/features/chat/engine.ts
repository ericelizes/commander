// EngineMapper turns cmdrd's chat.Event frames (internal/chat/event.go, see
// docs/plans/2026-10-20-native-chat-engine.md "Event model") into the pane's
// ChatEvents. One mapper per subscription: tool_input_delta frames carry only
// message_id + block, so it remembers which tool_use id each block started.
// Frames that are not cmdrd events (already-normalized ones, {raw} lines) fall
// back to fromEngine.
import { fromEngine } from './adapter'
import type { ChatEvent, EventInput, Origin, PermissionSuggestion, Question, RateWindow, SlashCommand } from './events'
import { stripScreen } from './screen'

type Raw = Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any

// isEngineFrame: cmdrd events always carry role + source (or session_id).
export function isEngineFrame(f: Raw): boolean {
  return typeof f.kind === 'string' && typeof f.seq === 'number' && ('source' in f || 'session_id' in f || 'role' in f)
}

const ORIGINS: Record<string, Origin | undefined> = { user: 'user', mail: 'mail', replay: 'replay' }

function attachments(list: unknown): { name: string; mediaType: string }[] | undefined {
  if (!Array.isArray(list) || !list.length) return undefined
  return list.map((a: Raw) => ({ name: String(a.name ?? 'file'), mediaType: String(a.mime ?? '') }))
}

function taskStatus(s: unknown): 'running' | 'completed' | 'failed' | 'killed' {
  if (s === 'completed' || s === 'failed') return s
  if (s === 'killed' || s === 'stopped' || s === 'cancelled') return 'killed'
  return 'running'
}

function rateWindows(r: Raw): Record<string, RateWindow> {
  const out: Record<string, RateWindow> = {}
  for (const [k, w] of Object.entries((r.unifiedWindows ?? {}) as Raw)) {
    if (w && typeof w.utilization === 'number') out[k] = { utilization: w.utilization, resetsAt: w.resetsAt }
  }
  if (!Object.keys(out).length && r.rateLimitType && typeof r.utilization === 'number') {
    out[r.rateLimitType] = { utilization: r.utilization, resetsAt: r.resetsAt }
  }
  return out
}

function denied(permission: unknown): boolean {
  if (!permission) return false
  const p = permission as Raw
  const v = typeof permission === 'string' ? permission : (p.behavior ?? p.decision)
  return v === 'deny' || v === 'reject'
}

function commands(list: unknown): SlashCommand[] | undefined {
  if (!Array.isArray(list)) return undefined
  return list.map((c) => (typeof c === 'string' ? { name: c.replace(/^\//, '') } : { name: String(c?.name ?? ''), description: c?.description })).filter((c) => c.name)
}

export class EngineMapper {
  private blockTool = new Map<string, string>() // `${message_id}:${block}` -> tool_use id

  map(frame: unknown, role = ''): ChatEvent[] {
    if (!frame || typeof frame !== 'object') return []
    if (Array.isArray(frame)) return frame.flatMap((f) => this.map(f, role))
    const f = frame as Raw
    if (!isEngineFrame(f)) return fromEngine(f, role)
    const ev = this.one(f)
    const base = { seq: f.seq as number, session: String(f.session_id ?? ''), ts: f.time ? Date.parse(f.time) : undefined, parent: f.parent_tool_use_id || undefined }
    // UserPromptSubmit fires the moment the CLI reads a message, including one
    // sent mid-turn, which it reads at the next tool boundary of the same turn.
    const read = f.kind === 'hook' && (f.hook as Raw)?.event === 'UserPromptSubmit' ? [{ kind: 'prompt_read', ...base } as ChatEvent] : []
    return ev ? [...read, { ...ev, ...base } as ChatEvent] : read
  }

  private one(f: Raw): EventInput | null {
    const key = `${f.message_id ?? ''}:${f.block ?? 0}`
    const message = String(f.message_id ?? `m${f.seq}`)
    const index = typeof f.block === 'number' ? f.block : 0
    const tool: Raw = f.tool ?? {}
    const req: Raw = f.request ?? {}
    switch (f.kind) {
      case 'session': {
        const s: Raw = f.session ?? {}
        return { kind: 'init', model: s.model, cwd: s.cwd, permissionMode: s.permission_mode, version: s.cli_version, commands: commands(s.slash_commands), fresh: f.source === 'engine' }
      }
      case 'user_message': {
        if (f.origin === 'subagent') return null // shown inside its Task card's own events
        // The CLI echoes a blocking Stop hook's reason as a user message; the hook row already shows it.
        if (String(f.text ?? '').startsWith('Stop hook feedback:')) return null
        return { kind: 'user_message', id: String(f.uuid || `u-${f.seq}`), text: stripScreen(String(f.text ?? '')), origin: ORIGINS[f.origin], history: f.source === 'transcript', attachments: attachments(f.attachments) }
      }
      case 'text_delta':
        return { kind: 'text_delta', message, index, text: String(f.text ?? '') }
      case 'text':
        return { kind: 'text_final', message, index, text: String(f.text ?? '') }
      case 'thinking_delta':
        return { kind: 'thinking', message, index, text: f.text || undefined, tokens: f.tokens || undefined }
      case 'thinking':
        return { kind: 'thinking', message, index, text: f.text || undefined, done: true }
      case 'tool_start':
        if (!tool.id) return null
        this.blockTool.set(key, tool.id)
        return { kind: 'tool_start', message, index, id: tool.id, name: String(tool.name ?? 'tool'), input: tool.input }
      case 'tool_input_delta': {
        const id = tool.id || this.blockTool.get(key)
        if (!id || !tool.partial_json) return null
        return { kind: 'tool_input_delta', id, partial: tool.partial_json }
      }
      case 'tool_input': {
        const id = tool.id || this.blockTool.get(key)
        return id ? { kind: 'tool_input', id, input: tool.input ?? {} } : null
      }
      case 'tool_result':
        if (!tool.id) return null
        return {
          kind: 'tool_result',
          id: tool.id,
          output: typeof tool.content === 'string' ? tool.content : JSON.stringify(tool.content ?? ''),
          isError: !!tool.is_error,
          structured: tool.structured,
          decision: denied(tool.permission) ? 'reject' : undefined,
        }
      case 'permission_request':
        return {
          kind: 'permission_request',
          id: String(req.id),
          toolUseId: req.tool_use_id,
          tool: String(req.tool_name ?? 'tool'),
          input: req.input ?? {},
          description: req.description,
          suggestions: (req.suggestions ?? []) as PermissionSuggestion[],
          blockedPath: req.blocked_path,
        }
      case 'question':
        return { kind: 'question', id: String(req.id), toolUseId: req.tool_use_id, questions: ((req.input ?? {}).questions ?? []) as Question[] }
      case 'plan':
        return { kind: 'plan', id: String(req.id), toolUseId: req.tool_use_id, plan: String((req.input ?? {}).plan ?? '') }
      case 'request_resolved': {
        const d = req.decision
        return { kind: 'request_resolved', id: String(req.id), behavior: d === 'deny' || d === 'cancelled' ? 'deny' : 'allow', always: d === 'allow_session', message: req.message || (d === 'cancelled' ? 'cancelled' : undefined) }
      }
      case 'status':
        if (f.status === 'requesting' || f.status === 'compacting' || f.status === 'idle') return { kind: 'status', state: f.status }
        return null
      case 'task': {
        const t: Raw = f.task ?? {}
        if (!t.task_id) return null
        return { kind: 'task', taskId: t.task_id, toolUseId: t.tool_use_id, description: t.description, status: taskStatus(t.status ?? (f.subtype === 'task_started' ? 'running' : undefined)), summary: t.summary }
      }
      case 'compaction': {
        const c: Raw = f.compaction ?? {}
        return { kind: 'compact', trigger: c.trigger, preTokens: c.pre_tokens, postTokens: c.post_tokens }
      }
      case 'rate_limit': {
        const r: Raw = f.rate_limit ?? {}
        return { kind: 'rate_limit', status: String(r.status ?? ''), windows: rateWindows(r) }
      }
      case 'hook': {
        const h: Raw = f.hook ?? {}
        // Only hooks that injected something are worth a row (e.g. mail).
        if (!h.output) return null
        const id = String(f.uuid || `h-${f.seq}`)
        // A tool permission hook (the Manager guard's deny) shows on the failed
        // tool step itself; a second row with its JSON is noise.
        if (isPermissionHook(String(h.output))) return null
        const said = hookReason(String(h.output))
        // The Stop hook delivers mail by blocking the stop: show it as the mail it is.
        if (said.startsWith(MAIL_HOOK)) return { kind: 'user_message', id, text: said.slice(MAIL_HOOK.length).trim(), origin: 'mail' }
        return { kind: 'user_message', id, text: said, origin: 'hook' }
      }
      case 'notice':
        if (f.subtype === 'api_retry') {
          const r: Raw = f.raw ?? {}
          return { kind: 'api_retry', attempt: Number(r.attempt ?? 1), maxRetries: r.max_retries, delayMs: r.retry_delay_ms, error: r.error ? String(r.error) : undefined }
        }
        if (f.subtype === 'interrupted') return { kind: 'interrupted' }
        // cli_version is cmdrd checking the CLI's version against the one it was
        // verified with: a note for whoever maintains cmdrd, not for the user.
        if (f.subtype === 'session_title_changed' || f.subtype === 'cli_version' || String(f.subtype ?? '').startsWith('unknown:')) return null
        // The terminal UI's label for a Stop hook that blocked (how mail arrives), not a failure.
        if (/^Stop hook error occurred/.test(String(f.text ?? ''))) return null
        if (!f.text) return null
        return { kind: 'notice', level: f.subtype === 'interrupt_fallback' ? 'warn' : 'info', text: String(f.text) }
      case 'agent_state':
        return f.state?.state === 'idle' ? { kind: 'agent_idle' } : null
      case 'turn_result': {
        const r: Raw = f.result ?? {}
        const u: Raw = r.usage ?? {}
        const windows = Object.values((r.model_usage ?? {}) as Raw)
          .map((m: Raw) => m?.contextWindow)
          .filter((n): n is number => typeof n === 'number')
        return {
          kind: 'turn_result',
          subtype: String(r.subtype ?? 'success'),
          isError: !!r.is_error,
          durationMs: r.duration_ms,
          numTurns: r.num_turns,
          costUsd: r.total_cost_usd,
          usage: { input: u.input_tokens ?? 0, output: u.output_tokens ?? 0, cacheRead: u.cache_read_input_tokens ?? 0, cacheCreate: u.cache_creation_input_tokens ?? 0 },
          contextWindow: windows.length ? Math.max(...windows) : undefined,
          terminalReason: r.terminal_reason,
          denials: Array.isArray(r.permission_denials) ? r.permission_denials.length : undefined,
        }
      }
      case 'error':
        return { kind: 'error', message: String(f.error ?? 'error') }
      case 'process_exit': {
        const x: Raw = f.exit ?? {}
        return { kind: 'exit', code: x.code ?? null, signal: x.signal || null }
      }
      default:
        return null // replay_done: no timeline item
    }
  }
}

// The mail Stop hook's reason starts with this (cmd/cmdr/internal_mail_hook.go).
const MAIL_HOOK = 'Commander mail system: new mail for you (delivered automatically; the user did not type this).'

function isPermissionHook(output: string): boolean {
  try {
    return typeof JSON.parse(output)?.hookSpecificOutput?.permissionDecision === 'string'
  } catch {
    return false
  }
}

// hookReason is what a hook said: the reason of a {"decision":"block"} reply, or its raw output.
function hookReason(output: string): string {
  try {
    const o = JSON.parse(output)
    if (typeof o?.reason === 'string') return o.reason
    const ctx = o?.hookSpecificOutput?.additionalContext
    if (typeof ctx === 'string') return ctx
  } catch {
    // cmdrd keeps the first 4000 bytes of a hook's output, so a long one is
    // JSON cut off mid-string: read the message up to the cut.
    const m = /"(?:reason|additionalContext)"\s*:\s*"((?:[^"\\]|\\.)*)/.exec(output)
    if (m) {
      const body = m[1].replace(/\\(u[0-9a-fA-F]{0,3})?$/, '')
      try {
        return JSON.parse(`"${body}"`) + '…'
      } catch {
        return body + '…'
      }
    }
  }
  return output
}
