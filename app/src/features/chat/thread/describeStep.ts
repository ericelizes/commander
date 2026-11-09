// Plain-language labels for what the agent did, read out of each tool call
// itself: the file it read, the query it searched, the commit it saved. Done
// steps are past tense ("Read events.ts"); the live one is "-ing".
// Labels map onto Claude Code's tool names.

import {
  BookOpen,
  Bot,
  Brain,
  FileText,
  FolderSearch,
  GitCompare,
  Globe,
  ListChecks,
  type LucideIcon,
  Package,
  PencilLine,
  Play,
  Plug,
  Save,
  Search,
  SquareTerminal,
  FolderOpen,
} from 'lucide-react'
import type { Step } from './toThread'

export type StepDescription = { label: string; activeLabel: string; icon: LucideIcon }

type Obj = Record<string, unknown>
const obj = (v: unknown): Obj => (v && typeof v === 'object' ? (v as Obj) : {})
const str = (v: unknown): string => (typeof v === 'string' ? v : '')
const basename = (path: string) => path.replace(/\/+$/, '').split('/').pop() || path
const clip = (text: string, max = 60) => (text.length > max ? `${text.slice(0, max - 1).trimEnd()}…` : text)

/** thoughtParts splits a thinking block into its bold title line and the rest. */
export function thoughtParts(text: string) {
  const cleaned = text.trim()
  const match = cleaned.match(/^\*\*(.+?)\*\*\s*/)
  if (!match) {
    const [first, ...rest] = cleaned.split('\n')
    return { title: first.replace(/\*\*/g, ''), body: rest.join('\n').trim() }
  }
  return { title: match[1], body: cleaned.slice(match[0].length).trim() }
}

const firstPath = (command: string) =>
  command.replace(/["']/g, ' ').replace(/\$\{?\w+\}?\//g, '').match(/(?:^|\s)((?:[\w.~-]+\/)*[\w.-]+\.\w{1,8})(?=\s|;|$|\||\))/)?.[1]

const quoted = (command: string) => command.match(/["']([^"']{3,})["']/)?.[1]

// Blank out quoted strings and heredoc bodies (keeping offsets), so a ";" or
// newline inside an inline script is not mistaken for a command separator.
function maskQuoted(command: string) {
  let masked = command.replace(/<<-?\s*(['"]?)(\w+)\1[^\n]*\n[\s\S]*?\n\s*\2\b/g, (m) => m.replace(/[^\n]/g, ' ').replace(/^ {2}/, '<<'))
  masked = masked.replace(/'[^']*'|"(?:[^"\\]|\\.)*"/g, (m) => m[0] + 'x'.repeat(Math.max(0, m.length - 2)) + m[m.length - 1])
  return masked
}

// Agents chain setup in front of the real work ("cd x && rg ..."); describe
// the first segment that does something.
function mainSegment(command: string) {
  const masked = maskQuoted(command)
  const segments: string[] = []
  let start = 0
  const separators = /&&|\|\||;|\n/g
  for (let m = separators.exec(masked); m; m = separators.exec(masked)) {
    segments.push(command.slice(start, m.index))
    start = m.index + m[0].length
  }
  segments.push(command.slice(start))
  const parts = segments.map((part) => part.trim().replace(/^(do|then|else)\s+/, '')).filter(Boolean)
  const skip = /^(?:(?:cd|export|source|set|umask|true|echo|printf|sleep|mkdir|for|while|if|fi|done)\b|\.\s|[A-Za-z_]\w*=)/
  return parts.find((part) => !skip.test(part)) ?? parts[0] ?? command
}

function describeCommand(command: string, description: string): Omit<StepDescription, 'activeLabel'> {
  const cmd = mainSegment(command.trim())
  const first = cmd.split(/\s+/)[0].replace(/^.*\//, '')
  const file = firstPath(cmd)
  const text = quoted(cmd)
  if (/^(pytest|go test|npm (run )?test|npx vitest|just test|uv run pytest|python3? -m pytest)/.test(cmd))
    return { label: file ? `Ran the tests in ${basename(file)}` : 'Ran the tests', icon: Play }
  if (/^git commit/.test(cmd)) {
    const message = cmd.match(/-m\s+["']([^"']+)["']/)?.[1]
    return { label: message ? `Saved changes: "${clip(message, 50)}"` : 'Saved the changes', icon: Save }
  }
  if (/^git (add|stage)\b/.test(cmd)) return { label: 'Staged the changes', icon: Save }
  if (/^git push\b/.test(cmd)) return { label: 'Pushed the changes', icon: Save }
  if (/^git (status|diff|log|show)/.test(cmd)) return { label: 'Checked recent changes', icon: GitCompare }
  if (/^git (checkout|switch|branch|worktree)/.test(cmd)) return { label: 'Switched code branches', icon: GitCompare }
  if (/^gh pr\b/.test(cmd)) return { label: 'Checked the pull request', icon: GitCompare }
  if (/^(npm|pip3?|uv|apt(-get)?|pnpm|yarn|brew) (install|add|sync|i|ci)\b/.test(cmd)) return { label: 'Installed packages', icon: Package }
  if (/^(npm|pnpm|yarn) run build|^vite build|^just build|^go build/.test(cmd)) return { label: 'Built the app', icon: Package }
  if (/^(rg|grep|ag|ack)\b/.test(cmd)) return { label: text ? `Searched the code for "${clip(text, 40)}"` : 'Searched the code', icon: Search }
  if (/^(git ls-files|ls|find|tree|fd)\b/.test(cmd)) return { label: 'Looked through the project files', icon: FolderSearch }
  if (/^sed -i\b|^perl -pi\b/.test(cmd)) return { label: file ? `Edited ${basename(file)}` : 'Edited a file', icon: PencilLine }
  if (/^(cat|tee)\b.*(>|<<)/.test(cmd)) return { label: file ? `Wrote ${basename(file)}` : 'Wrote a file', icon: PencilLine }
  if (/^(head|tail|cat|sed -n|less|wc|nl|bat)\b/.test(cmd)) return { label: file ? `Read ${basename(file)}` : 'Read a file', icon: FileText }
  if (/^(curl|wget|http)\b/.test(cmd)) {
    const host = cmd.match(/https?:\/\/([^/:\s'"]+)/)?.[1]
    return { label: host ? `Called ${host}` : 'Called a web address', icon: Globe }
  }
  if (/^(python3?|node|bash|sh|go run)\s+[\w./-]+\.(py|js|mjs|ts|sh|go)\b/.test(cmd) && file) return { label: `Ran ${basename(file)}`, icon: Play }
  // The agent's own one-line description beats a bare program name.
  if (description) return { label: description.replace(/\.$/, ''), icon: SquareTerminal }
  return { label: /^[A-Za-z][\w.-]*$/.test(first) ? `Ran ${first}` : 'Ran a command', icon: SquareTerminal }
}

function describeDone(step: Step): Omit<StepDescription, 'activeLabel'> {
  if (step.type === 'thinking') {
    const title = step.text ? thoughtParts(step.text).title : ''
    return { label: title || 'Thought it through', icon: Brain }
  }
  const input = obj(step.input)
  const path = str(input.file_path) || str(input.notebook_path) || str(input.path)
  switch (step.name) {
    case 'Bash':
      return describeCommand(str(input.command), str(input.description))
    case 'BashOutput':
      return { label: 'Checked a background command', icon: SquareTerminal }
    case 'KillShell':
      return { label: 'Stopped a background command', icon: SquareTerminal }
    case 'Read':
      return { label: path ? `Read ${basename(path)}` : 'Read a file', icon: FileText }
    case 'Edit':
    case 'MultiEdit':
    case 'NotebookEdit':
      return { label: path ? `Edited ${basename(path)}` : 'Edited a file', icon: PencilLine }
    case 'Write':
      return { label: path ? `Wrote ${basename(path)}` : 'Wrote a file', icon: PencilLine }
    case 'Grep':
      return { label: input.pattern ? `Searched the code for "${clip(str(input.pattern), 40)}"` : 'Searched the code', icon: Search }
    case 'Glob':
      return { label: 'Looked through the project files', icon: FolderSearch }
    case 'WebFetch': {
      const host = str(input.url).match(/^https?:\/\/([^/:]+)/)?.[1]
      return { label: host ? `Read ${host}` : 'Read a web page', icon: Globe }
    }
    case 'WebSearch':
      return { label: input.query ? `Searched the web for "${clip(str(input.query), 40)}"` : 'Searched the web', icon: Globe }
    case 'Task':
    case 'Agent':
      return { label: input.description ? `Ran a helper: ${clip(str(input.description), 50)}` : 'Ran a helper agent', icon: Bot }
    case 'TodoWrite':
      return { label: 'Updated the plan', icon: ListChecks }
    case 'Skill':
      return { label: input.skill ? `Used the ${str(input.skill)} skill` : 'Used a skill', icon: BookOpen }
    case 'AskUserQuestion':
      return { label: 'Asked you a question', icon: BookOpen }
    case 'ExitPlanMode':
      return { label: 'Proposed a plan', icon: ListChecks }
  }
  if (step.name.startsWith('mcp__')) {
    const [, server, ...rest] = step.name.split('__')
    return { label: `Used ${server.replace(/[-_]/g, ' ')}: ${rest.join(' ').replace(/[-_]/g, ' ')}`, icon: Plug }
  }
  return { label: `Used ${step.name}`, icon: FolderOpen }
}

// Past tense once a step is done, "-ing" while it runs.
const ACTIVE: [RegExp, string][] = [
  [/^Ran the tests/, 'Running the tests'],
  [/^Ran a helper/, 'Running a helper'],
  [/^Ran /, 'Running '],
  [/^Read /, 'Reading '],
  [/^Edited /, 'Editing '],
  [/^Wrote /, 'Writing '],
  [/^Searched /, 'Searching '],
  [/^Looked through /, 'Looking through '],
  [/^Saved /, 'Saving '],
  [/^Staged /, 'Staging '],
  [/^Pushed /, 'Pushing '],
  [/^Checked /, 'Checking '],
  [/^Switched /, 'Switching '],
  [/^Installed /, 'Installing '],
  [/^Built /, 'Building '],
  [/^Called /, 'Calling '],
  [/^Stopped /, 'Stopping '],
  [/^Used /, 'Using '],
  [/^Updated /, 'Updating '],
  [/^Asked /, 'Asking '],
  [/^Proposed /, 'Proposing '],
  [/^Thought it through/, 'Thinking'],
]

export function describeStep(step: Step): StepDescription {
  const done = describeDone(step)
  const rule = ACTIVE.find(([pattern]) => pattern.test(done.label))
  return { ...done, activeLabel: rule ? done.label.replace(rule[0], rule[1]) : done.label }
}

// What kind of work a step is, for the live row's icon.
export type StepMode = 'thinking' | 'reading' | 'looking' | 'changing'

export function stepMode(step: Step): StepMode {
  if (step.type === 'thinking') return 'thinking'
  const { label } = describeStep(step)
  if (/^(Read |Used the .* skill)/.test(label)) return 'reading'
  return /^(Saved|Staged|Pushed|Edited|Wrote|Built|Installed|Ran the tests|Switched)/.test(label) ? 'changing' : 'looking'
}

export function stepFailed(step: Step): boolean {
  return step.type === 'tool' && (step.status === 'error' || step.status === 'denied')
}

export function stepRunning(step: Step): boolean {
  return step.type === 'tool' ? step.status === 'input' || step.status === 'running' : !step.done
}
