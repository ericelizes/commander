// Fixture conversations (normalized events) for the gallery, screenshot tests
// and dev without cmdrd. Synthetic protocol examples live in app/src/features/chat/__tests__/testdata.
import type { ChatEvent, EventInput } from './events'

type E = EventInput

function build(evs: E[], session = 'fixture'): ChatEvent[] {
  return evs.map((e, i) => ({ ...e, seq: i + 1, session }) as ChatEvent)
}

const head = (now: number): E[] => [
  { kind: 'init', model: 'claude-opus-5-5', cwd: '/tmp/example/code/commander', permissionMode: 'default', ts: now - 600_000 },
  { kind: 'rate_limit', status: 'allowed', windows: { five_hour: { utilization: 0.42, resetsAt: Math.round(now / 1000) + 7400 }, seven_day: { utilization: 0.31 } } },
]

const LONG_ANSWER = `Here's how the cockpit's **event pipeline** fits together, and the one change I'd make.

## How it flows

1. \`cmdrd\` owns each Claude process and reads its stream-json stdout.
2. Every line is normalized into a \`ChatEvent\` with a monotonic \`seq\`.
3. The app subscribes over a WebSocket with \`?since=<seq>\`, so a reload resumes without gaps.

\`\`\`go
func (s *Session) pump(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		ev, err := normalize(sc.Bytes())
		if err != nil {
			continue // unknown event types are skipped, never fatal
		}
		s.publish(ev)
	}
	return sc.Err()
}
\`\`\`

| Event | Source | Rendered as |
|---|---|---|
| \`text_delta\` | \`stream_event\` | streaming markdown |
| \`tool_start\` | \`content_block_start\` | collapsible tool card |
| \`permission_request\` | \`control_request\` | inline approval card |
| \`turn_result\` | \`result\` | turn footer + cost |

> Cost is cumulative per session, so the header shows the latest \`total_cost_usd\` and each turn footer shows the delta.

The one change: buffer deltas per animation frame on the client. Without it a fast stream re-renders per token. See the [stream-json notes](https://docs.claude.com/en/docs/claude-code/sdk) for the full event list.`

export function longAnswer(now = Date.now()): ChatEvent[] {
  return build([
    ...head(now),
    { kind: 'user_message', id: 'u1', text: 'Walk me through how chat events get from Claude to the app. Keep it tight.', ts: now - 240_000 },
    { kind: 'status', state: 'requesting', ts: now - 239_000 },
    { kind: 'thinking', message: 'm1', index: 0, ts: now - 238_000 },
    { kind: 'thinking', message: 'm1', index: 0, done: true, durationMs: 2140, tokens: 412, text: 'The user wants the pipeline. Cover cmdrd ownership, normalization, seq-based resume, then the client batching.', ts: now - 236_000 },
    { kind: 'text_final', message: 'm1', index: 1, text: LONG_ANSWER, ts: now - 230_000 },
    { kind: 'turn_result', subtype: 'success', isError: false, durationMs: 11_420, costUsd: 0.0412, usage: { input: 8, output: 1120, cacheRead: 42_000, cacheCreate: 3100 }, contextWindow: 1_000_000, ts: now - 228_000 },
    { kind: 'context', tokens: 46_200, window: 1_000_000 },
    { kind: 'user_message', id: 'u2', text: 'And the reconnect path?', ts: now - 20_000 },
    { kind: 'status', state: 'requesting', ts: now - 19_000 },
    { kind: 'text_delta', message: 'm2', index: 0, text: 'On reconnect the client re-opens the socket with `?since=` set to the last `seq` it applied. ', ts: now - 15_000 },
    { kind: 'text_delta', message: 'm2', index: 0, text: 'cmdrd replays everything after that from its ring buffer, then continues live, so', ts: now - 14_000 },
  ])
}

export function toolHeavy(now = Date.now()): ChatEvent[] {
  const t = (s: number) => now - s * 1000
  return build([
    ...head(now),
    { kind: 'user_message', id: 'u1', text: 'TestSnapshotRanks is failing on main. Find out why and fix it.', ts: t(95) },
    { kind: 'status', state: 'requesting', ts: t(94) },
    { kind: 'text_final', message: 'm1', index: 0, text: "I'll find the test and run it first.", ts: t(92) },
    { kind: 'tool_start', message: 'm1', index: 1, id: 'g1', name: 'Grep', ts: t(91) },
    { kind: 'tool_input', id: 'g1', input: { pattern: 'func TestSnapshotRanks', path: 'internal', output_mode: 'files_with_matches' } },
    { kind: 'tool_result', id: 'g1', output: 'internal/livedata/snapshot_test.go', isError: false, structured: { mode: 'files_with_matches', numFiles: 1, filenames: ['internal/livedata/snapshot_test.go'] }, ts: t(90.6) },
    { kind: 'tool_start', message: 'm2', index: 0, id: 'r1', name: 'Read', ts: t(89) },
    { kind: 'tool_input', id: 'r1', input: { file_path: '/tmp/example/code/commander/internal/livedata/snapshot_test.go' } },
    { kind: 'tool_result', id: 'r1', output: '…', isError: false, structured: { type: 'text', file: { filePath: 'internal/livedata/snapshot_test.go', content: 'func TestSnapshotRanks(t *testing.T) {\n\tsnap := build(t, fixture("ranks"))\n\twant := []string{"api", "web", "infra"}\n\tif got := names(snap.Workstreams); !slices.Equal(got, want) {\n\t\tt.Fatalf("ranks = %v, want %v", got, want)\n\t}\n}', numLines: 88, startLine: 1, totalLines: 88 } }, ts: t(88.8) },
    { kind: 'tool_start', message: 'm2', index: 1, id: 'r2', name: 'Read', ts: t(88.7) },
    { kind: 'tool_input', id: 'r2', input: { file_path: '/tmp/example/code/commander/internal/livedata/snapshot.go' } },
    { kind: 'tool_result', id: 'r2', output: '…', isError: false, structured: { type: 'text', file: { filePath: 'internal/livedata/snapshot.go', content: '…', numLines: 214 } }, ts: t(88.5) },
    { kind: 'tool_start', message: 'm3', index: 0, id: 'b1', name: 'Bash', ts: t(86) },
    { kind: 'tool_input', id: 'b1', input: { command: 'go test ./internal/livedata -run TestSnapshotRanks', description: 'Run the failing test' } },
    { kind: 'tool_result', id: 'b1', output: 'Exit code 1\n--- FAIL: TestSnapshotRanks (0.01s)\n    snapshot_test.go:42: ranks = [web api infra], want [api web infra]\nFAIL', isError: true, structured: { stdout: '--- FAIL: TestSnapshotRanks (0.01s)\n    snapshot_test.go:42: ranks = [web api infra], want [api web infra]\nFAIL\nFAIL\tgithub.com/user/commander/internal/livedata\t0.214s', stderr: '', interrupted: false }, ts: t(82.9) },
    { kind: 'thinking', message: 'm4', index: 0, done: true, durationMs: 3800, tokens: 640, text: 'The sort is by updated_at, not rank. sort.Slice is also unstable: equal ranks reorder.', ts: t(80) },
    { kind: 'text_final', message: 'm4', index: 1, text: 'The order comes from `updated_at`, not `rank`, and `sort.Slice` is unstable for ties. Fixing both:', ts: t(78) },
    { kind: 'tool_start', message: 'm4', index: 2, id: 'e1', name: 'Edit', ts: t(77) },
    { kind: 'tool_input', id: 'e1', input: { file_path: '/tmp/example/code/commander/internal/livedata/snapshot.go', old_string: '\tsort.Slice(ws, func(i, j int) bool {\n\t\treturn ws[i].UpdatedAt.After(ws[j].UpdatedAt)\n\t})', new_string: '\tsort.SliceStable(ws, func(i, j int) bool {\n\t\tif ws[i].Rank != ws[j].Rank {\n\t\t\treturn ws[i].Rank < ws[j].Rank\n\t\t}\n\t\treturn ws[i].UpdatedAt.After(ws[j].UpdatedAt)\n\t})' } },
    { kind: 'tool_result', id: 'e1', output: 'The file has been updated.', isError: false, ts: t(76.5) },
    { kind: 'tool_start', message: 'm5', index: 0, id: 'b2', name: 'Bash', ts: t(75) },
    { kind: 'tool_input', id: 'b2', input: { command: 'go test ./internal/livedata/...', description: 'Re-run the package tests' } },
    { kind: 'tool_result', id: 'b2', output: 'ok  \tgithub.com/user/commander/internal/livedata\t0.231s', isError: false, structured: { stdout: 'ok  \tgithub.com/user/commander/internal/livedata\t0.231s', stderr: '', interrupted: false }, ts: t(71.2) },
    { kind: 'tool_start', message: 'm5', index: 1, id: 'td', name: 'TodoWrite', ts: t(70) },
    { kind: 'tool_input', id: 'td', input: { todos: [{ content: 'Reproduce TestSnapshotRanks failure', status: 'completed' }, { content: 'Sort workstreams by rank, stable', status: 'completed' }, { content: 'Run the full test suite', status: 'in_progress', activeForm: 'Running the full test suite' }] } },
    { kind: 'tool_result', id: 'td', output: 'ok', isError: false, ts: t(70) },
    { kind: 'user_message', id: 'mail1', text: '[mail] from manager (web): rank order also feeds the NEEDS YOU list, check that view too.', ts: t(60) },
    { kind: 'tool_start', message: 'm6', index: 0, id: 'b3', name: 'Bash', ts: t(4) },
    { kind: 'tool_input', id: 'b3', input: { command: 'go test ./...', description: 'Run the full suite' } },
  ])
}

export function permissionPrompt(now = Date.now()): ChatEvent[] {
  const t = (s: number) => now - s * 1000
  return build([
    ...head(now),
    { kind: 'user_message', id: 'u1', text: 'Clean the build cache and rebuild cmdr.', ts: t(40) },
    { kind: 'status', state: 'requesting', ts: t(39) },
    { kind: 'text_final', message: 'm1', index: 0, text: "I'll check what's in the cache first.", ts: t(37) },
    { kind: 'tool_start', message: 'm1', index: 1, id: 'b1', name: 'Bash', ts: t(36) },
    { kind: 'tool_input', id: 'b1', input: { command: 'du -sh ~/.cache/go-build', description: 'Size of the Go build cache' } },
    { kind: 'permission_request', id: 'p1', toolUseId: 'b1', tool: 'Bash', input: { command: 'du -sh ~/.cache/go-build', description: 'Size of the Go build cache' }, suggestions: [{ type: 'addRules', rules: [{ toolName: 'Bash', ruleContent: 'du:*' }], behavior: 'allow', destination: 'localSettings' }], ts: t(35.9) },
    { kind: 'request_resolved', id: 'p1', behavior: 'allow', ts: t(33) },
    { kind: 'tool_result', id: 'b1', output: '1.9G\t/tmp/example/.cache/go-build', isError: false, structured: { stdout: '1.9G\t/tmp/example/.cache/go-build', stderr: '', interrupted: false }, ts: t(32.6) },
    { kind: 'text_final', message: 'm2', index: 0, text: 'The cache is 1.9 GB. Cleaning it and rebuilding:', ts: t(30) },
    { kind: 'tool_start', message: 'm2', index: 1, id: 'b2', name: 'Bash', ts: t(29) },
    { kind: 'tool_input', id: 'b2', input: { command: 'go clean -cache && go build -o ~/.local/bin/cmdr ./cmd/cmdr', description: 'Clear the Go build cache and rebuild cmdr' } },
    {
      kind: 'permission_request',
      id: 'p2',
      toolUseId: 'b2',
      tool: 'Bash',
      input: { command: 'go clean -cache && go build -o ~/.local/bin/cmdr ./cmd/cmdr', description: 'Clear the Go build cache and rebuild cmdr' },
      description: 'Clear the Go build cache and rebuild cmdr',
      suggestions: [
        { type: 'addRules', rules: [{ toolName: 'Bash', ruleContent: 'go clean:*' }], behavior: 'allow', destination: 'localSettings' },
        { type: 'setMode', mode: 'acceptEdits', destination: 'session' },
      ],
      blockedPath: '/tmp/example/.local/bin/cmdr',
      ts: t(28.8),
    },
  ])
}

export function subAgent(now = Date.now()): ChatEvent[] {
  const t = (s: number) => now - s * 1000
  const P = 'task1'
  return build([
    ...head(now),
    { kind: 'user_message', id: 'u1', text: 'Audit every place we shell out to tmux and list the ones that ignore errors.', ts: t(70) },
    { kind: 'status', state: 'requesting', ts: t(69) },
    { kind: 'text_final', message: 'm1', index: 0, text: "That's a wide search, so I'll hand it to an Explore agent and review what it finds.", ts: t(66) },
    { kind: 'tool_start', message: 'm1', index: 1, id: P, name: 'Task', ts: t(65) },
    { kind: 'tool_input', id: P, input: { description: 'Find tmux calls that drop errors', subagent_type: 'Explore', prompt: 'Search the Go code for every exec of `tmux` (exec.Command, run helpers). For each, report file:line and whether the error is checked.' } },
    { kind: 'task', taskId: 'bg1', toolUseId: P, description: 'Find tmux calls that drop errors', status: 'running', ts: t(64) },
    { kind: 'text_final', message: 's1', index: 0, parent: P, text: "I'll grep for tmux invocations, then read each call site.", ts: t(62) },
    { kind: 'tool_start', message: 's1', index: 1, parent: P, id: 'sg1', name: 'Grep', ts: t(61) },
    { kind: 'tool_input', id: 'sg1', input: { pattern: 'exec.Command\\("tmux"', path: '/tmp/example/code/commander/internal', output_mode: 'files_with_matches' } },
    { kind: 'tool_result', id: 'sg1', parent: P, output: '7 files', isError: false, structured: { numFiles: 7, filenames: ['internal/layout/tmux.go', 'internal/spawn/host.go'] }, ts: t(60.5) },
    { kind: 'tool_start', message: 's2', index: 0, parent: P, id: 'sr1', name: 'Read', ts: t(58) },
    { kind: 'tool_input', id: 'sr1', input: { file_path: '/tmp/example/code/commander/internal/layout/tmux.go' } },
    { kind: 'tool_result', id: 'sr1', parent: P, output: '…', isError: false, structured: { file: { numLines: 311 } }, ts: t(57.8) },
    { kind: 'tool_start', message: 's2', index: 1, parent: P, id: 'sr2', name: 'Read', ts: t(57) },
    { kind: 'tool_input', id: 'sr2', input: { file_path: '/tmp/example/code/commander/internal/spawn/host.go' } },
    { kind: 'tool_result', id: 'sr2', parent: P, output: '…', isError: false, structured: { file: { numLines: 486 } }, ts: t(56.7) },
    { kind: 'tool_start', message: 's3', index: 0, parent: P, id: 'sg2', name: 'Grep', ts: t(3) },
    { kind: 'tool_input', id: 'sg2', input: { pattern: '_ = .*tmux', path: '/tmp/example/code/commander/internal' } },
    { kind: 'task', taskId: 'bg1', toolUseId: P, status: 'running', lastTool: 'Grep', ts: t(3) },
  ])
}

export function questionAndPlan(now = Date.now()): ChatEvent[] {
  const t = (s: number) => now - s * 1000
  return build([
    ...head(now),
    { kind: 'user_message', id: 'u1', text: 'Plan the migration of Managers to native chat.', ts: t(50) },
    { kind: 'status', state: 'requesting', ts: t(49) },
    { kind: 'tool_start', message: 'm1', index: 0, id: 'q1', name: 'AskUserQuestion', ts: t(45) },
    {
      kind: 'question',
      id: 'rq1',
      toolUseId: 'q1',
      questions: [
        {
          question: 'How should a Manager move from tmux to native chat?',
          header: 'Handover',
          options: [
            { label: 'Stop the TUI, resume headless', description: 'Same session id, history kept. Recommended.' },
            { label: 'Fresh session with a summary', description: 'Clean context, loses the exact transcript.' },
            { label: 'Run both side by side', description: 'Silently forks history (verified in the spike).' },
          ],
        },
      ],
      ts: t(44.9),
    },
  ])
}

export function planOnly(now = Date.now()): ChatEvent[] {
  const t = (s: number) => now - s * 1000
  return build([
    ...head(now),
    { kind: 'user_message', id: 'u1', text: 'Plan the migration of Managers to native chat.', ts: t(50) },
    { kind: 'tool_start', message: 'm1', index: 0, id: 'x1', name: 'ExitPlanMode', ts: t(45) },
    {
      kind: 'plan',
      id: 'rp1',
      toolUseId: 'x1',
      plan: '## Manager cutover\n\n1. Add a per-session lock in cmdrd (one live process per session id).\n2. `cmdr internal-handover <role>`: stop the TUI pane, resume headless with `--resume`.\n3. Flip the app pane to `<ChatPane>`; keep **Pop out to terminal** as the reverse path.\n4. Behind `[chat] native_managers = true`; default on after a week.\n\nRisk: a resumed session keeps its original role prompt until compaction.',
      ts: t(44.9),
    },
  ])
}

export const FIXTURES = { longAnswer, toolHeavy, permissionPrompt, subAgent, questionAndPlan, planOnly }
export type FixtureName = keyof typeof FIXTURES

// manyEvents makes a long synthetic session for the performance test.
export function manyEvents(n: number, now = Date.now()): ChatEvent[] {
  const out: E[] = [...head(now)]
  let i = 0
  while (out.length < n) {
    i++
    out.push({ kind: 'user_message', id: `u${i}`, text: `Question ${i}: what about item ${i}?`, ts: now - (n - out.length) * 50 })
    for (let k = 0; k < 6; k++) out.push({ kind: 'text_delta', message: `m${i}`, index: 0, text: `Token run ${k} for answer ${i}. ` })
    out.push({ kind: 'text_final', message: `m${i}`, index: 0, text: `Answer ${i}: done with \`code\` and **bold**.` })
    out.push({ kind: 'tool_start', message: `m${i}`, index: 1, id: `t${i}`, name: 'Bash' })
    out.push({ kind: 'tool_input', id: `t${i}`, input: { command: `echo ${i}` } })
    out.push({ kind: 'tool_result', id: `t${i}`, output: String(i), isError: false })
    out.push({ kind: 'turn_result', subtype: 'success', isError: false, durationMs: 1000, costUsd: i * 0.001 })
  }
  return build(out.slice(0, n))
}
