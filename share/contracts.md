# External CLI Contracts

**Single source of truth for every external tool commander invokes.**
Verified 2026-05-08 (macOS Darwin arm64). Doc-verified shapes; live-run verification documented per-spike.

Every CLI invocation in commander's code MUST match a shape documented here.
`tests/integration/` verifies these contracts hold; drift fails the build.
When upgrading any tool, re-run the integration suite before merging.

---

## Versions pinned

| Tool       | Version          | Source                                | Min required |
|------------|------------------|---------------------------------------|--------------|
| `go`       | 1.26.3           | `brew install go`                     | 1.22         |
| `opencode` | 1.14.41          | `npm install -g opencode-ai`          | 1.14.0       |
| `bd`       | 1.0.2 (a3f834b3) | cargo                                 | 1.0.0        |
| `am`       | 0.2.47           | cargo / homebrew                      | 0.2.40       |
| `docker`   | 29.4.1           | Docker Desktop (macOS)                | 20.10        |
| `tmux`     | 3.6a             | homebrew                              | 3.3          |
| `sqlite3`  | 3.51.0           | macOS system                          | 3.35         |
| `jq`       | ≥ 1.6            | homebrew                              | 1.6          |
| `uuidgen`  | system           | macOS / util-linux on Linux           | —            |

**Package install footgun:** npm package is `opencode-ai`, binary is `opencode`.

---

## opencode

### opencode run — primary invocation

```bash
opencode run \
  --session <ses_sample_5> \
  --agent <agent-name> \
  --format json \
  [--model <provider/model>] \
  [--attach http://localhost:<port>] \
  "<initial-prompt>"
```

**Verified flags on `opencode run`:**

| Flag | Short | Description |
|------|-------|-------------|
| `--session` | `-s` | Session ID (`ses_`-prefixed for ID lookup; else title search) |
| `--format` | | `text` (default) or `json` (nd-JSON stream) |
| `--agent` | | Agent name from `.opencode/agents/` or `opencode.json` |
| `--model` | `-m` | Model in `provider/model` format |
| `--attach` | | Connect to running `opencode serve` |
| `--continue` | `-c` | Resume last session |
| `--fork` | | Fork session when continuing |
| `--file` | `-f` | Attach files to message |
| `--title` | | Set session title |
| `--thinking` | | Display thinking blocks |
| `--variant` | | Model variant |
| `--dangerously-skip-permissions` | | Skip permission prompts |
| `--dir` | | Working directory |

**NO `--system-prompt` flag.** Layer differentiation uses `--agent <name>` only.

---

### CRITICAL — Session ID format AND lifecycle

opencode session IDs use format `ses_sample_6` (`ses_` prefix + ULID-derived alphanumeric, ~26 chars total).

**LIVE-VERIFIED 2026-05-08 — sessions cannot be pre-minted.** Passing `--session ses_<new-id>` for an ID that does not yet exist returns `NotFoundError: Session not found: ses_sample_7`. The only way to create a session is to invoke `opencode run` WITHOUT `--session`, then capture the `sessionID` field that appears on every nd-JSON event in the stream.

`--session <value>` behavior:
- `ses_`-prefixed AND already exists → ID lookup (resume that session)
- `ses_`-prefixed AND does not exist → **error**, NOT auto-creation
- Otherwise (any non-`ses_` value) → title search via GET /session/?search= API

**Commander's spawn flow MUST be capture-then-resume:**

```bash
# Step 1: First invocation creates session; commander captures the ID.
BOOTSTRAP_OUT=$(opencode run --format json "<bootstrap-prompt>")
SESSION_ID=$(echo "$BOOTSTRAP_OUT" | grep '^{' | jq -r '.sessionID' | head -1)
echo "$SESSION_ID" > <project>/.commander/operators/<branch>.session-id

# Step 2: All subsequent invocations resume by ID.
opencode run --session "$SESSION_ID" --format json "<follow-up>"
```

This affects U7 (project activation Lead spawn), U9 (code operator spawn), U15 (knowledge operator spawn), and the prior plan-sketch that pre-generated UUIDs via `openssl rand` was wrong.

**`--continue` flag** resumes the most-recent session. Useful for re-attaching a single operator without persisting the session ID file.

---

### opencode run --format json — nd-JSON event stream (U10 load-bearing)

`opencode run --format json` writes **one JSON object per line** to stdout as the run progresses. Live, pipe-able.

**Event types:**

| Type | When | Token data |
|------|------|-----------|
| `step_start` | Beginning of each agent step | No |
| `text` | Model text output chunk | No |
| `tool_use` | Tool invocation | No |
| `tool_result` | Tool execution result | No |
| `step_finish` | Step completion | **YES — load-bearing** |
| `error` | Session failure | No |

**`step_finish` schema (load-bearing for U10) — LIVE-VERIFIED 2026-05-08:**

```json
{
  "type": "step_finish",
  "timestamp": 1767225600000,
  "sessionID": "ses_sample_1",
  "part": {
    "id": "prt_sample_2",
    "type": "step-finish",
    "reason": "stop",
    "snapshot": "0000000000000000000000000000000000000000",
    "messageID": "msg_sample_3",
    "sessionID": "ses_sample_1",
    "cost": 0,
    "tokens": {
      "total": 125,
      "input": 100,
      "output": 3,
      "reasoning": 12,
      "cache": {
        "read": 0,
        "write": 0
      }
    }
  }
}
```

**Note on `total`:** opencode emits a precomputed `tokens.total` that may differ from `input+output` (counts cache and reasoning). U10 uses `input + output` for ceiling math (matches what gets billed cumulatively per turn).

**Note on `cost: 0`:** observed in test runs with cached/free model paths. U10 must accept zero-cost events as valid (do not treat as missing field).

**U10 watchdog key fields:**
- `part.cost` — USD cost for this step (float). Direct; no rate table needed.
- `part.tokens.{input,output}` — per-step token counts
- `part.tokens.reasoning` — reasoning tokens (0 for non-thinking models)
- `part.tokens.cache.{read,write}` — cache tokens

**CRITICAL — No model field in step_finish.** Record model at spawn time in `agent_runs.model` from the `--model` flag. Never read from stream.

**U10 accumulation pattern (Go):**

```go
// For each line from docker logs -f <container>:
var event struct {
    Type string `json:"type"`
    Part struct {
        Cost   float64 `json:"cost"`
        Tokens struct {
            Input  int `json:"input"`
            Output int `json:"output"`
        } `json:"tokens"`
    } `json:"part"`
}
if event.Type == "step_finish" {
    run.USDUsed    += event.Part.Cost
    run.TokensUsed += event.Part.Tokens.Input + event.Part.Tokens.Output
    if run.USDUsed >= ceiling.MaxUSD {
        kill(run, "usd ceiling")
    }
}
```

**jq filters for stream processing:**

```bash
opencode run ... | jq -r 'select(.type=="text") | .part.text'                 # text
opencode run ... | jq 'select(.type=="step_finish") | .part.cost'             # per-step USD
opencode run ... | jq 'select(.type=="step_finish") | .part.tokens'           # per-step tokens
```

---

### opencode stats

**Text output only. No `--json` flag (doc-verified 2026-05-08).**

```bash
opencode stats [--days N] [--tools N] [--models] [--project <path>]
```

| Flag | Description |
|------|-------------|
| `--days N` | Filter to last N days |
| `--tools N` | Top N tools |
| `--models` | Per-model breakdown |
| `--project <path>` | Filter by project path |

**Commander usage:** display-only in Lead/Commander metrics panes. NEVER used for ceiling enforcement.

---

### opencode serve + opencode run --attach

**U7 architecture decision:** PENDING live spike result from `test-opencode-serve-attach.sh`.

```bash
opencode serve --port 9997
opencode run \
  --attach http://localhost:9997 \
  --session ses_<operator-id> \
  --format json \
  "<prompt>"
```

`opencode attach` is TUI-only. Non-interactive programmatic use requires `opencode run --attach <url>`.

**serve flags:** `--port`, `--hostname`, `--mdns`, `--mdns-domain`, `--cors`

**U7 decision gates:** If `test-opencode-serve-attach.sh` passes (multi-client, no cross-talk) → use serve+attach. Otherwise spawn-per-operator.

---

### opencode agent — layer differentiation

Commander uses `--agent <name>` exclusively. No `--system-prompt`.

```bash
opencode run --session ses_sample_8 --agent commander          --format json "<msg>"
opencode run --session ses_sample_8 --agent lead               --format json "<msg>"
opencode run --session ses_sample_8 --agent operator-code      --format json "<msg>"
opencode run --session ses_sample_8 --agent operator-knowledge --format json "<msg>"
```

Agent definition files (checked into commander repo, volume-mounted into Docker):

```
.opencode/agents/
  commander.md          ← Commander layer
  lead.md               ← Lead layer
  operator-code.md      ← Operator for code-type
  operator-knowledge.md ← Operator for knowledge-type
```

Format (`operator-code.md`):

```markdown
---
description: "Operator agent for code-type projects"
model: "anthropic/claude-sonnet-4-5"
---

You are an Operator agent in the commander orchestration system...
```

`opencode agent` subcommands: `list`, `create`.

Per-session context (Task description, prior state) is injected via the bootstrap message content, NOT via runtime flag.

---

### opencode session eviction semantics

PENDING live verification by `test-opencode-eviction.sh`. When session file is deleted and same `ses_` ID is reused, opencode behavior is one of:
- `fresh-session-same-id` — creates fresh context with same ID (most likely)
- `error-on-evicted` — errors on evicted ID
- `cached-elsewhere` — context stored beyond filesystem

U7 fallback chain design depends on observed behavior.

---

## bd — Beads

Version: 1.0.2 (a3f834b3).

### bd set-state — write side

```bash
bd set-state <issue-id> <dimension>=<value> [--reason "<text>"]
```

**Critical:** invocation uses `=`, stored label uses `:`.
`bd set-state <id> cmdr=brainstorming` → stores label `cmdr:brainstorming`.

Commander state writes:

```bash
bd set-state <id> cmdr=brainstorming  --reason "operator spawned"
bd set-state <id> cmdr=planning       --reason "plan phase"
bd set-state <id> cmdr=executing      --reason "operator working"
bd set-state <id> cmdr=reviewing      --reason "review phase"
bd set-state <id> cmdr=done
bd set-state <id> cmdr=blocked        --reason "<context>"
bd set-state <id> cmdr=needs-input    --reason "<question>"
bd set-state <id> cmdr=idle
bd set-state <id> cmdr=working        --reason "in progress"
```

**Namespace isolation (Q14):** `bd set-state` transitions ONLY the `cmdr:` dimension label. The `kind:task` discriminator label is NOT touched. Verified by `test-bd-cmdr-namespace.sh`.

### bd list

```bash
bd list [--label <LABEL>] [--label-any <LABEL>...] \
        [--status <STATUS>] [--type <TYPE>] \
        [--limit N] [--json] [--tree]
```

- `--label` / `-l` — AND semantics
- `--label-any` — OR semantics
- `-C <dir>` — change to project dir

```bash
bd -C /code/<project> list --label cmdr:brainstorming --json
bd -C /code/<project> list --label kind:task --json
bd -C /code/<project> list --label kind:operational --json
```

### bd q — quick capture (no --parent)

```bash
bd q "<title>" [--labels <labels>] [--priority N] [--type <type>]
```

Returns issue ID only. **No `--parent` flag** in bd 1.0.2.

### bd create — full creation with --parent (PRIMARY operational bead path)

```bash
bd create "<title>" \
  [--parent <parent-id>] \
  [--labels <labels>] \
  [--priority N] \
  [--type <type>] \
  [--silent]
```

```bash
# Silent (output = ID only):
OP_ID=$(bd create "analyze requirements" \
  --parent "$TASK_ID" --labels kind:operational --silent)

# JSON (parse .id):
OP_ID=$(bd create "analyze requirements" --parent "$TASK_ID" --labels kind:operational \
  | jq -r '.id')
```

### bd link — positional args, NOT --from/--to

```bash
bd link <id1> <id2> [--type <type>]
```

**VERIFIED DISCREPANCY from plan:** plan assumed `--from <a> --to <b>`. Actual: positional.

Available types: `blocks` (default), `tracks`, `related`, `parent-child`, `discovered-from`.

```bash
# Fallback if bd create --parent unavailable:
OP_ID=$(bd q "analyze requirements" --labels kind:operational)
bd link "$TASK_ID" "$OP_ID" --type parent-child
```

`internal/beads/link.go` uses positional args.

### br dep list — the edges `br list` only counts

`br list --json` carries `dependency_count` (parent-child included), not the
edges. `br dep list <id> --json` returns them (verified br 0.1.x, 2026-10-21):

```json
[{"issue_id":"p-1.2","depends_on_id":"p-1.1","type":"blocks","title":"Build","status":"open","priority":2}]
```

`internal/beads/deps.go` reads it; livedata fetches it only for steps with
more edges than their parent-child one, and keeps `type: "blocks"` as the
step's `blocked_by`. Record one with `br dep add <issue> <depends-on>`.

---

### Approval gates — labels `gate` + `approver:<who>`, edges via `br dep add`

A gate is an objective bead labelled `gate`, with one `approver:<who>` label
per approver (`user`, a GitHub login, a name; dots become dashes). It sits
between work with `blocks` edges: `br dep add <gate> <work before>` and
`br dep add <work after> <gate>` (`cmdr internal-create-objective --gate
--approver --after --before` does all of it). Approved = the bead is closed.
The app reads the next unapproved gate, the leaf beads it waits on (through
containers and other work, stopping at an approved gate) and how many are done.

## am — Agent Mail

Version: 0.2.47.

### Project topology

- `commander` am-project: Commander↔Lead cross-tier traffic
- Per-project am-project: Lead↔Operator in-project traffic

### am agents register

```bash
am agents register \
  --project <PROJECT_KEY> \
  --program <PROGRAM> \
  --model <MODEL> \
  --name <NAME> \
  [--json]
```

**Registration program:** Commander uses `--program opencode-ai`.

```bash
# Commander tier
am agents register --project commander --program opencode-ai \
  --model sonnet --name chief --json >/dev/null

# Lead tier (per project)
am agents register --project commander --program opencode-ai \
  --model sonnet --name lead-<project> --json >/dev/null

# Operator tier
am agents register --project <project-key> --program opencode-ai \
  --model sonnet --name operator-<flat-branch> --json >/dev/null
```

Idempotent: re-registering same name updates, does not duplicate.

### am mail send

```bash
am mail send \
  --project <PROJECT_KEY> --from <SENDER> --to <TO> \
  --subject <SUBJECT> --body <BODY> \
  [--importance low|normal|high|urgent] [--ack-required] [--json]
```

### am mail inbox

```bash
am mail inbox \
  --project <PROJECT_KEY> --agent <AGENT_NAME> \
  --since <ISO-8601> [--limit N] [--include-bodies] [--json]
```

**Verified discrepancy:** `--since` requires ISO-8601, not duration strings.

```bash
if [[ "$(uname)" == "Darwin" ]]; then
  SINCE=$(date -u -v-24H +%FT%TZ)
else
  SINCE=$(date -u -d '24 hours ago' +%FT%TZ)
fi
```

### am check-inbox

```bash
am check-inbox --project <P> --agent <A> --rate-limit 0 [--json]
```

**Verified discrepancy:** default `--rate-limit` is 120s. Always pass `--rate-limit 0`.

---

## Docker

Version: 29.4.1 (Docker Desktop macOS).

### Code-type operator spawn

```bash
docker run --rm -d \
  --name cmdr-<project>-<flat-branch>-<short-id> \
  --label commander.project=<project> \
  --label commander.task=<task-bead-id> \
  --label commander.run-id=<sqlite-run-id> \
  -v <project-root>:/workspace \
  -v <project-root>/.worktrees/<branch>:/worktree \
  -v <project-root>/.beads:/workspace/.beads \
  -v <project-root>/.commander:/workspace/.commander \
  -v ~/.config/opencode:/root/.config/opencode:ro \
  -v <commander-repo>/.opencode/agents:/workspace/.opencode/agents:ro \
  <network-flags-per-OS> \
  commander/operator:latest \
  opencode run --session ses_<id> --agent operator-code --format json "<bootstrap>"
```

### Network flags by OS

**macOS (Docker Desktop):** `host.docker.internal` resolves automatically. No `--add-host` needed.

**Linux (Docker Engine ≥20.10):**

```bash
--add-host=host.docker.internal:host-gateway
```

`internal/docker/spawn.go`:

```go
if runtime.GOOS != "darwin" {
    args = append(args, "--add-host=host.docker.internal:host-gateway")
}
```

### bd volume mount + WAL behavior

Mount: `-v <project>/.beads:/workspace/.beads`

SQLite WAL allows concurrent readers + one writer. Host/container writes are short-lived; deadlock probability low. If `test-docker-bd-mount.sh` detects WAL contention, fall back to ephemeral bd inside container syncing back on Task close.

### bd binary inside container — DO NOT volume-mount the host's bd

**LIVE-VERIFIED 2026-05-08:** the macOS `bd` binary cannot be volume-mounted into a Linux Alpine container. The host bd is a Mach-O binary and produces `cannot execute: required file not found` errors when invoked from Linux. Commander's operator Docker image MUST install its own Linux bd binary at build time (e.g., `cargo install` or download a Linux-built release).

```dockerfile
# operator.Dockerfile
FROM node:20-alpine
RUN apk add --no-cache bash git jq curl ca-certificates
# Install Linux bd binary (do NOT bind-mount the host bd):
RUN curl -fsSL <bd-linux-release-url> -o /usr/local/bin/bd && chmod +x /usr/local/bin/bd
RUN npm install -g opencode-ai
# am Linux binary similarly
```

### Orphan reconciliation

cmdr launch + every 5min in watchdog:

```bash
docker ps --filter label=commander.project --format json
# For each container, check agent_runs by container_id.
# Container without matching `running` row → orphan → kill + log to cost_events.
```

---

## git worktree

```bash
git -C <project-root> worktree add .worktrees/<branch> -b <branch>
git -C <project-root> worktree add .worktrees/<branch> -B <branch>
git -C <project-root> worktree list --porcelain
git -C <project-root> worktree remove .worktrees/<branch>

echo ".commander/" >> <project-root>/.git/info/exclude
echo ".worktrees/" >> <project-root>/.git/info/exclude
```

Knowledge-type operators: no worktree, commit directly to main.

---

## Decisions locked by U1

### Decision 1 — U7: serve-vs-spawn

**Status: CONDITIONAL on `test-opencode-serve-attach.sh` live result.**

Default for U7 implementation start: spawn-per-operator (conservative). Switch to serve+attach only after spike confirms no cross-talk.

### Decision 2 — U10: cost ceiling architecture

**Status: LOCKED.**

`opencode run --format json` nd-JSON event stream exclusively. Consume `step_finish.part.cost` (direct USD) + `step_finish.part.tokens.{input,output}`. Pre-emptive halt before next call when ceiling near. `opencode stats` is display-only.

### Decision 3 — `bd create --parent`

**Status: LOCKED. Primary path is `bd create --parent <id>`.**

Fallback: `bd q` + `bd link <parent> <child> --type parent-child` (positional args).

### Decision 4 — `--agent` vs `--system-prompt`

**Status: LOCKED.** Use `--agent <name>` with prompt files at `.opencode/agents/`. No runtime `--system-prompt` flag exists. Dynamic context via bootstrap message.

### Decision 5 — Session ID lifecycle

**Status: LOCKED. LIVE-VERIFIED 2026-05-08.**

Sessions are **captured-then-resumed**, never pre-minted:
1. Spawn flow: `opencode run --format json "<bootstrap>"` (no `--session`)
2. Capture `sessionID` from any nd-JSON event
3. Persist to `<project>/.commander/operators/<branch>.session-id`
4. Resume: `opencode run --session <captured-id> --format json "<msg>"`

`opencode run --session ses_<new-id>` errors with `NotFoundError`. NEVER pre-mint.

### Decision 6 — bd binary inside operator container

**Status: LOCKED. LIVE-VERIFIED 2026-05-08.**

Operator Docker image installs its own Linux bd binary at build time. Do NOT volume-mount the host's macOS bd — Mach-O binaries cannot run on Linux. Same applies to am.

### Decision 7 — am cross-project pattern (deferred verification)

**Status: doc-verified; live verification deferred.**

The cross-project check requires an isolated Agent Mail service; an existing service may hold its mailbox activity lock. Commander's `--program opencode-ai` contract is covered by the wrapper integration test under controlled conditions.

---

## Verified contract discrepancies

| Original assumption | Actual (verified 2026-05-08) | Impact |
|---|---|---|
| Pre-mint session ID, pass via `--session` | Sessions cannot be pre-minted; must be captured from event stream | U7/U9/U15 spawn = capture-then-resume |
| Volume-mount host bd into container | Mach-O binary fails on Linux | Image installs Linux bd at build time |
| `--format json` = single final blob | nd-JSON streaming (one line per event) | U10 viable; pipe line-by-line |
| `step_finish` has `usage.model` field | No model field in tokens | Record model at spawn time |
| `opencode stats --json` for structured data | No `--json` flag (text only) | U10 never uses stats for enforcement |
| `--system-prompt` flag on `opencode run` | Flag does not exist | Use `--agent <name>` + prompt files |
| `bd link --from <a> --to <b>` | Positional args `bd link <id1> <id2>` | `internal/beads/link.go` uses positional |
| `--program claude-code` for am register | `--program opencode-ai` | All am register calls |
| `opencode attach` for non-interactive | TUI only; use `opencode run --attach <url>` | U7 serve-mode wiring |

---

## Drift policy

When any verified shape changes after a tool upgrade:

1. Re-run `tests/integration/` — failure flags drift immediately.
2. Update this file with new shape, dated.
3. Update commander code referencing old shape.
4. Commit: `chore(contracts): update <tool> shape after <version-bump>`.

Commander does not ship invocations not documented here.
