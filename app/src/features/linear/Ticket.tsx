import { useSprint, mismatch, statusTone } from './sprint'
import { useIntegrations } from './settings'
import { openInPanel } from '../inspector/connections'
import { PrPill } from '../github/PrPill'

// Linear tickets belong beside a title, not inside it: "Review sample entry replacements
// (TEAM-103)" shows as "Review sample entry replacements" plus a Linear pill that opens the
// ticket.

const TICKET = /(?<![A-Za-z0-9-])[A-Z][A-Z0-9]{1,9}-\d{1,6}(?![A-Za-z0-9])/g
// Any case, for joining to real issue ids only: stripping these from a title would eat "utf-8".
const TICKET_ANY_CASE = /(?<![A-Za-z0-9-])[A-Za-z][A-Za-z0-9]{1,9}-\d{1,6}(?![A-Za-z0-9])/g
const REF = String.raw`(?:PR\s*)?(?:#\d{1,6}|[A-Z][A-Z0-9]{1,9}-\d{1,6})`
const GROUP = new RegExp(String.raw`\s*\((?:\s*${REF}\s*[,/&]?\s*)+\)`, 'g') // "(TEAM-1)", "(#117)", "(TEAM-1, #2)"

/**
 * splitTickets takes the Linear ticket ids and PR numbers out of a title and
 * returns what is left, tidied. A PR number only counts inside parentheses, so
 * prose like "Merge #101 > #102" is left alone.
 */
export function splitTickets(title: string): { text: string; tickets: string[]; prs: string[] } {
  const groups = title.match(GROUP) ?? []
  const prs = [...new Set(groups.flatMap((g) => [...g.matchAll(/#(\d{1,6})/g)].map((m) => m[1])))]
  const tickets = [...new Set(title.match(TICKET) ?? [])]
  if (!tickets.length && !prs.length) return { text: title, tickets, prs }
  const text = title
    .replace(GROUP, '')
    .replace(TICKET, '')
    .replace(/\s*[:,;]\s*(?=[,;)])/g, '') // "(Alice: X-1, X-2)" leaves "(Alice:,)"
    .replace(/[,;]\s*\)/g, ')')
    .replace(/\(\s*\)/g, '')
    .replace(/\s+([?.,!:;)])/g, '$1')
    .replace(/\s{2,}/g, ' ')
    .replace(/^[\s\-–—:·]+|[\s\-–—:·]+$/g, '')
    .trim()
  return { text: text || title, tickets, prs }
}

/** ticketIds is what could be a ticket in a title, upper-cased; callers match it against real issue ids (livedata.TitleTickets). */
export function ticketIds(title: string): string[] {
  return [...new Set((title.match(TICKET_ANY_CASE) ?? []).map((t) => t.toUpperCase()))]
}

export function linearUrl(id: string, workspace: string): string {
  return `https://linear.app/${encodeURIComponent(workspace)}/issue/${encodeURIComponent(id)}`
}

const LinearMark = () => (
  <svg width="10" height="10" viewBox="0 0 16 16" aria-hidden>
    <path
      fill="currentColor"
      d="M1.2 9.6 6.4 14.8A7 7 0 0 1 1.2 9.6ZM1 7.7l7.3 7.3a7 7 0 0 0 1.7-.3L1.3 6a7 7 0 0 0-.3 1.7ZM1.9 4.7l9.4 9.4a7 7 0 0 0 1.3-.8L2.7 3.4a7 7 0 0 0-.8 1.3ZM3.8 2.4A7 7 0 1 1 13.6 12.2Z"
    />
  </svg>
)

/** LinearPill is a ticket as a link: the Linear mark and its id. */
export function LinearPill({ id, progress }: { id: string; progress?: { done: number; total: number } }) {
  const issue = useSprint((s) => s.data?.issues.find((i) => i.id === id))
  const warning = issue && mismatch(issue, progress)
  const content = <><LinearMark />{issue && <span className={`linear-status ${statusTone(issue)}`} aria-hidden />}{id}{issue && <> · {issue.status_name}</>}{warning && <span className="linear-mismatch" title={warning} aria-label={warning}>⚠</span>}</>
  const workspace = useIntegrations((s) => s.linearWorkspace)
  if (!workspace) return <span className="linear-pill" title={issue?.title ?? id}>{content}</span>
  const url = linearUrl(id, workspace)
  return (
    <a
      className="linear-pill"
      href={url}
      title={issue ? `${issue.title}${warning ? ` · ${warning}` : ''}` : `Open ${id} in Linear`}
      // The pill sits inside clickable cards; it opens an inspector tab, never the browser, and must not also select the card.
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        openInPanel(url, id)
      }}
      onPointerDown={(e) => e.stopPropagation()}
    >
      {content}
    </a>
  )
}

/** TicketPills renders a title's PRs (GitHub) and tickets (Linear); place it after the (truncatable) title text. */
export function TicketPills({ tickets, prs = [], repo, progress }: { tickets: string[]; prs?: string[]; repo?: string; progress?: { done: number; total: number } }) {
  if (!tickets.length && !prs.length) return null
  return (
    <span className="linear-pills">
      {prs.map((n) => (
        <PrPill key={`pr${n}`} repo={repo} n={n} />
      ))}
      {tickets.map((t) => (
        <LinearPill key={t} id={t} progress={progress} />
      ))}
    </span>
  )
}
