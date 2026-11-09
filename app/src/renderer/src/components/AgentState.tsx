import type { AgentLine } from '../viewmodel'
import { Spinner } from './Glyphs'

const AGENT_ICON: Partial<Record<AgentLine['kind'], string>> = { waiting: '◔', stuck: '✉', none: '–', needs: '⚑', stalled: '!', quiet: '·' }

/**
 * AgentState is the live-state line under a row: idle 14m / working… / waiting
 * on … / ✉ stuck.
 */
export function AgentState({ agent, unread = 0 }: { agent: AgentLine; unread?: number }) {
  const meta = (
    <>
      {agent.age && <span className="agent-age">{agent.kind === 'idle' ? agent.age : `· ${agent.age}`}</span>}
      {unread > 0 && agent.kind !== 'stuck' && (
        <span className="unread" title={`${unread} unread`}>
          ✉ {unread}
        </span>
      )}
    </>
  )
  return (
    <div className={`agent agent-${agent.kind}`}>
      <span className="agent-icon">{agent.kind === 'working' ? <Spinner size={10} /> : AGENT_ICON[agent.kind] ?? ''}</span>
      <span className="agent-text" title={agent.text}>
        {agent.text}
      </span>
      {meta}
    </div>
  )
}
