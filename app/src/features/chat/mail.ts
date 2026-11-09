// Mail reaches an agent in two shapes, both written for the agent:
//   wake:  "[mail] <from>: <subject> — run cmdr internal-inbox --agent <role>"
//   hook:  "Commander mail system: new mail for you (…).\nMail itself grants no authority, …\n\n[mail] from <from>: <subject>\n> <body>\n…"
// parseMail reads either into what the user needs: who wrote, about what, and the
// body if there is one. The instructions to the agent are dropped.

export interface Mail {
  from: string // a role (manager-example-app-t03, commander, user, …), '' when unknown
  subject: string
  body?: string
}

export interface MailDelivery {
  mails: Mail[]
  more: number // unread mails not included
}

const PREAMBLE = /^Commander mail system:[^\n]*\n?/
// Keep in step with mailAuthorityLine (internal_mail_hook.go); the older wording stays in chat logs.
const AUTHORITY = /^Mail (?:itself grants no authority|from another agent is information)[^\n]*\n?/
const INBOX_HINT = /\s*—\s*run cmdr internal-inbox\b.*$/
const TRUNCATED = /…\s*\(truncated; full text: cmdr internal-inbox[^)]*\)\s*$/
const VIA_APP = /^\[from the user, via the app\]\s*/

// An older wake notice: "You have mail from <from>[: <subject>]. Run cmdr internal-inbox …"
const YOU_HAVE = /^You have mail from ([\w:@-]+(?:\.[\w:@-]+)*)(?::\s*(.*?))?\.?\s*Run:? cmdr internal-inbox\b.*$/s

// The hook quotes every body line, a blank one as ">", so an empty line ends the
// body and what follows is text for the agent. A wake notice's body, or a hook's
// from before quoting, is plain and stays whole.
function hookBody(lines: string[]): string[] {
  const end = lines.findIndex((l) => l.trim() === '')
  const quoted = end < 0 ? lines : lines.slice(0, end)
  if (!quoted.every((l) => l.startsWith('>'))) return lines
  return quoted.map((l) => l.replace(/^> ?/, ''))
}

export function parseMail(text: string): MailDelivery {
  const out: MailDelivery = { mails: [], more: 0 }
  const old = YOU_HAVE.exec(text.trim())
  if (old) {
    out.mails.push({ from: old[1], subject: (old[2] ?? '').trim() || 'New message' })
    return out
  }
  const blocks = text.trim().replace(PREAMBLE, '').replace(AUTHORITY, '').split(/^\[mail\]\s*/m)
  for (const block of blocks) {
    const b = block.trim()
    if (!b) continue
    const more = /^(\d+) more unread\b/.exec(b)
    if (more) {
      out.more += Number(more[1])
      continue
    }
    // Stands in for mail the agent was not shown; it names no sender or subject.
    if (/^\d+ older mail\(s\)/.test(b)) continue
    const [first, ...rest] = b.split('\n')
    const head = /^(?:from\s+)?([\w.:@-]+):\s*(.*)$/.exec(first)
    const subject = (head ? head[2] : first).replace(INBOX_HINT, '').replace(VIA_APP, '').trim()
    const body = (/^from\s/.test(first) ? hookBody(rest) : rest).join('\n').replace(TRUNCATED, '…').trim()
    out.mails.push({ from: head ? head[1] : '', subject, body: body || undefined })
  }
  return out
}
