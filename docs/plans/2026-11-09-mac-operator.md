# A governed Operator on the user's Mac (design)

Task: commander-il9.206.1.1 (design only, no code). Workstream commander-il9.206; the user
approved it at its Workflow gate: build an Operator that runs on the user's Mac as a governed
workflow, not a free agent, first slice read-only.

Why: some work needs what only the Mac has (the user's own sign-ins to read-only production
tools, files, a browser session). Today every Operator runs on cmdrd's machine. Current users:
an approved read-only production audit that waits on this workstream, and a second task held
until the user has seen a Mac run work.

## 1. Principles

1. **The Mac pulls; the server never pushes a command.** cmdrd has no way into the Mac: no
   inbound listener, no SSH key, no stored credential. The desktop app on the Mac connects
   out to cmdrd (as it already does for the snapshot and `/api/app/connect`) and asks for
   work. The same direction the app's remote install uses (`app/electron/remote-update.ts`:
   Mac -> server over SSH), never the reverse.
2. **The server names a run, the Mac decides what runs.** An offer carries ids, not argv.
   The claude command line, its flags, hooks, working directory and allowed tools are built
   by the app's main process from a template compiled into the app plus a policy file the
   user owns on the Mac. Nothing in an offer can widen them.
3. **A person starts every run.** Each run opens a native dialog on the Mac (Cancel default),
   like cmdrd's install dialog. In the first slice there is no standing approval.
4. **Same contract as a host Operator.** Bead brief, operator guard, prod-gate rule, no
   installs, final report on the bead through the write door, mail with the Manager,
   visible live/dead on the board.

## 2. How a step names the Mac

- **A place** is a paired device: `mac:<name>` (name = a short slug the user picks at pairing,
  e.g. `mac:laptop`). Stored in state.db (`places`: name, kind `mac`, device public key,
  paired_at, paired_by, revoked_at; new migration).
- **Pairing** needs two acts by the user, never an agent: the app's Settings > Places shows
  "Pair this Mac", generates an Ed25519 key pair (private key in the macOS Keychain,
  non-exportable) and shows a one-time code; the user runs `cmdr place pair <code> --name
  laptop` in their own terminal on the server. The verb refuses any caller with
  `COMMANDER_ROLE`/`COMMANDER_MANAGER_ROLE` and is not in the `/api/cmd` allow-list.
  `cmdr place revoke <name>` (same rule) ends it; a revoked place gets no offer and its
  running run is stopped.
- **A step opts in** with label `run-on:mac:<name>`, set by `internal-set-objective --bead S
  --run-on mac:<name>|none` (Managers, Commander, user; Operators refused). `run-on:` joins
  `reservedLabelPrefixes`, so `internal-label` refuses it. The door check refuses a
  `run-on:` naming an unknown or revoked place.
- **Spawn:** `internal-spawn-operator --step S` on a step with `run-on:` (or `--place
  mac:<name>`, which must match the label) takes the place path: it creates the task bead and
  an `agent_runs` row with new column `place` and status `offered`, no worktree, no tmux, no
  launcher. Refused unless: the place is paired and not revoked; a closed user gate covers the
  step (`livedata.UserGatesOver` / `coveringApproval`, no warn-only mode here); with
  `--prod-gate G` the existing prod-gate rules hold (closed `kind:gate`, approver user,
  description = scope, linked); the place has no other offered or running run (one at a
  time in the first slice). The workstream cap counts place runs too.

## 3. How the Mac runs it

**Channel.** The app's main process (not the page) holds `GET /api/places/connect` (a
websocket like `/api/app/connect`), authenticated by the api token AND a signature over a
server nonce with the device key, so a copied api token alone cannot pose as the Mac. Over
it cmdrd sends only:

| From cmdrd | Contents | Effect on the Mac |
|---|---|---|
| `offer` | run id, project, task bead id, step id, prod-gate id, brief sha256 | app fetches the run, shows the dialog |
| `stop` | run id, who asked | app ends the run's process group |
| `mail` | run id (signal only) | the hook reads mail at the next tool call |

From the Mac: `hello` (device, app and cmdr versions), `accept`/`decline`, `heartbeat`,
`exit`.

**Fetch and confirm.** On an offer the app GETs `/api/places/runs/<id>`: task title, step
title, brief (the task bead's description), prod-gate description, Manager role, a run token.
It checks the brief's sha256 against the offer, then shows the native dialog: place, step,
task, the brief (scrollable), the prod-gate scope if any, the read-only policy in force,
buttons Cancel (default, Esc) / Run on this Mac, timeout 10 min = Cancel. A decline or
timeout is reported; cmdrd settles the run `declined`, hands the step back to the Manager
(mail, no retry). A brief edited after the offer changes the hash: the app refuses and cmdrd
must re-offer.

**Launch.** Built in main from a fixed template:

```
env -u ANTHROPIC_API_KEY -u CLAUDE_CONFIG_DIR \
  COMMANDER_ROLE=<operator role> COMMANDER_MANAGER_ROLE=<manager role> \
  COMMANDER_PLACE_RUN=<run id> COMMANDER_PLACE_TOKEN=<run token> COMMANDER_PROD_GATE=<id?> \
  claude -p --output-format stream-json --session-id <uuid> \
    --disallowedTools Edit,Write,MultiEdit,NotebookEdit,<agent-mail MCP> \
    --settings <place operator settings> --append-system-prompt <role prompt> <bootstrap>
```

- `claude` is the user's own install on their own login (default `~/.claude`). The server
  never sees or holds the Mac's Claude credentials; the Mac never receives the server's.
- cwd = a fresh run directory `~/Library/Application Support/Commander/runs/<run id>/` (0700,
  created by main, kept after the run for the user). It is also where the agent writes its
  report draft: a private path, never a fixed `/tmp` file.
- Started with `detached` (own process group); main keeps the pid. Quitting the app asks
  "stop the run on <step>?"; Cancel keeps the app open. The first slice does not let a run
  outlive the app.
- Runtime: Claude only. Codex is refused for place runs in the first slice (no mail hook).
- The role prompt is the one in `prompts/operator-code.md`, embedded in the Mac's cmdr build,
  plus a place paragraph: "you run on the user's Mac, read-only; report with `cmdr place
  report`". The bootstrap prompt is built by main from the fetched bead fields, the same
  shape as `spawn.hostBootstrapPrompt` (task id, Manager role, PRODUCTION GATE paragraph when
  set).

**Mac-side cmdr.** The Mac needs the same `cmdr` revision as the server (the app checks
`cmdr --version` against cmdrd's `VERSION` in `hello`; skew = refuse with "update cmdr on
this Mac"). It provides the guard and the place verbs; it opens no state.db and no beads db.

## 4. Same guards

PreToolUse runs `cmdr internal-operator-guard --role R --place read-only` locally, before
every tool:

- Everything `operatorguard.Classify` refuses today (credentials, `/api/update`, tmux kills,
  wait labels, cockpit, bd/br writes and bypass names, root-wide scans).
- **Read-only mode** (new, Mac only): a Bash line passes only when it is a pure read per
  `managerguard/reads.go` (no interpreters, no redirects to files except into the run
  directory, no `>(...)`, git read subcommands only) OR its first word is a CLI on the
  user's local allow-list with a read subcommand from that list. Refused outright: package
  installs (brew/npm/pip/gem/cargo install, `softwareupdate`), `sudo`, `launchctl`,
  `defaults write`, `security` (Keychain), `ssh`/`scp`/`rsync`, `curl`/`wget` with any method
  other than GET or with a body, writes to any path outside the run directory, `claude`,
  `codex`, `osascript`, `open`.
- Reads of `~/.ssh`, `~/.aws/credentials`, the Keychain, browser profiles, the Commander
  device key and the policy file are refused by path.
- **The local policy file** `~/.commander/place-policy.toml` on the Mac (allowed CLIs and
  their read subcommands, readable directories) is written by the user only. The guard
  refuses any command naming it (as `legacy-owner-aliases` is protected), and the server has
  no field that adds to it: an offer can only narrow (`run-on` selects the mode; there is no
  "write" mode yet).
- `--disallowedTools Edit,Write,...` removes the edit tools; the guard is the line for Bash.
- Production: without a prod-gate on the run, the bootstrap says production is forbidden and
  the allow-list's production CLIs are refused (they are marked `production = true` in the
  policy). With one, those CLIs' read subcommands pass; the gate description is quoted in the
  bootstrap and was shown in the dialog.

The guard fails closed in read-only mode (a classifier error refuses; host Operators' guard
fails open). Refusals are logged on the Mac (`~/.commander/logs/place-refusals.jsonl`) and
sent in the next heartbeat so the Manager sees them on the bead.

## 5. Reporting and mail

The run token (32 random bytes, cmdrd stores only its hash, valid for this run until it
ends, at most 12 h) authorizes exactly these, all under `/api/places/runs/<id>/`:

- `POST comment` -> cmdrd writes the comment on the run's task bead through the write door
  with `COMMANDER_ROLE` = the run's operator role (door checks apply). `cmdr place report
  --file <path in run dir>` and `cmdr place comment --text` wrap it. Each comment carries a
  client id so a retried send is not written twice.
- `POST mail` -> to the run's Manager only (the server picks the recipient; `--to` is not a
  field). `cmdr place mail --subject --body`.
- `GET mail` -> the run role's unread mail, read by cmdrd from am for it; `cmdr
  internal-mail-hook --place` (PostToolUse + Stop, mail only, as for Claude host Operators)
  calls it, so a Manager's direction arrives at the next tool call with the same `> `
  quoting and authority line.
- `POST heartbeat`, `POST exit`.

A run token cannot read other beads, write other beads, spawn, mail anyone else or reach
`/api/cmd`, `/api/write` or `/api/update`. The final report is the last `cmdr place report`
comment; the settle reads it exactly as `readReport` does for host Operators.

## 6. What the Manager sees

- **Live/dead.** The app heartbeats every 30 s per run: pid alive, last tool call time,
  tokens, refusals since last beat, usage events. `spawn.LiveRuns` treats a place run with a
  heartbeat in the last 2 min as live. 2-15 min without one = `unreachable` (Mac asleep,
  offline, app quit uncleanly): the board reads "Operator on mac:laptop unreachable N min",
  not Broken, and the Manager is mailed once. Over 15 min, or an `exit` without a report =
  died: settled like a host Operator (bead open, owner none, `operator-died`, Manager
  mailed) but **never crash-restarted** (a restart needs the user's dialog). `exit` with a
  report = finished, normal settle (`Manager to close`).
- **Board and app:** step owner reads "Operator working on mac:laptop"; snapshot
  `SnapLive` gains `place` per run; Operators tier shows the place instead of a worktree.
- **Usage limits:** main reads claude's stream-json `rate_limit_event` and usage-limit
  messages and forwards them in the heartbeat; cmdrd records them in `chat/usage.json` under
  account `mac:<name>` (shown in Settings > Usage as the Mac's Claude identity, no meters
  for others). A usage stop ends the run as `stopped` with reason `usage_limit`; no
  automatic account or runtime move (the Mac has one login); the Manager is mailed.
- **Stop:** `internal-mail --interrupt` to a place run sends `stop` (allowed from the
  server: it only takes power away); the app kills the process group, posts `exit`, the
  settle reads it as `stopped`. The user can also stop it from the app.

## 7. First slice

Does: one read-only task, end to end. A paired Mac; a step labelled `run-on:mac:<name>`
under a closed user gate; the Manager spawns; the dialog; claude runs read-only with the
policy's allowed CLIs; mail reaches it; it writes its report into the run dir and posts it
with `cmdr place report`; the run settles; the board showed it live throughout. Rehearsal
task first (list and summarize files in a directory the policy allows, no production), then
the approved read-only production audit under its prod-gate.

Must refuse:
- a spawn for a step without `run-on:`, an unpaired/revoked place, no closed covering user
  gate, a second run on the same place, Codex runtime;
- an offer whose brief hash does not match, any run the user did not confirm, a resume of an
  ended run (a new run means a new dialog);
- any edit tool, any Bash outside read-only mode, installs, sudo, Keychain, SSH, non-GET
  network, interpreters, nested agents, reads of credential paths, writes outside the run dir;
- production CLIs without a prod-gate on the run;
- any run-token call outside its run's bead and Manager;
- a Mac cmdr whose revision differs from cmdrd's.

## 8. Security boundary

The server must never gain a way to run arbitrary commands on the Mac without a step and a
gate. What the server can send is ids, a stop, a mail signal, and text (brief, mail) the
agent reads. What bounds execution is on the Mac: the per-run dialog, the fixed launch
template, the local guard compiled into the Mac's cmdr, the user's policy file, and the
absence of edit tools. Consequences:

- A compromised cmdrd (or a same-user agent on the server holding the api token) can at most
  offer runs the user must click, and steer an approved run through brief or mail text within
  the read-only allow-list. It cannot add a CLI, a write, a directory or a flag.
- No server-held secret runs anything on the Mac: the api token cannot sign the device
  challenge, the run token only reaches server endpoints, and the device key never leaves
  the Mac's Keychain.
- The Mac holds no beads database and no server credential beyond the api token the app
  already has.

## 9. Risks

- **Prompt injection is the main one.** Brief and mail text steer the agent; the guard and
  allow-list are what stop it, and they are classifiers (speed bumps against a determined
  adversary, as on the server). Keep the allow-list short; prefer narrow CLIs over generic
  ones.
- **Exfiltration of Mac data.** Anything the agent reads can go into its report, which goes
  to the server. Bounded by the readable directories in the policy and the credential-path
  refusals; the dialog shows the brief so the user sees what it is asked to read.
- **"Read" CLIs with write subcommands** (cloud CLIs, `gh api -X`). The policy lists
  subcommands, not just binaries; flags that change method are refused.
- **Same-user processes on the Mac** can edit the policy file or the guard binary; this is
  the same-UID trust boundary the server has, not a sandbox.
- **Sleep and network loss** stall runs; heartbeats make them visible, not resilient.
- **Version skew** between Mac and server cmdr: refused at `hello`.
- **Cost:** the run uses the user's personal Claude plan on the Mac; the Manager sees usage
  only through heartbeats.
- **Lost report:** a report sent but unanswered may have landed; the client id makes the
  retry safe.

## 10. Build order

1. Server registry: `places` migration, `cmdr place pair|revoke|list` (user only), `run-on:`
   label verb, door check and reserved prefix. Unit tests.
2. Server channel: `/api/places/connect` with device challenge, run offers, run tokens and
   the scoped endpoints (comment through the write door, mail send/read, heartbeat, exit,
   stop). Eval with a fake Mac client (`tests/evals/fakeplace`).
3. Spawn and lifecycle: `internal-spawn-operator` place path and its refusals, `agent_runs.
   place`, liveness from heartbeats, unreachable/died/finished settles without crash
   restart, board and snapshot text, usage records. Golden board fixture.
4. Mac cmdr: `cmdr place report|comment|mail`, `internal-mail-hook --place`,
   `internal-operator-guard --place read-only` with the policy file. Unit tests run on Linux
   too (pure classifiers).
5. App main: pairing (Keychain key), offer dialog, fixed launcher, process group,
   heartbeats, stop, usage forwarding, quit prompt. Tests with a fake claude and fake cmdrd
   (`electron/place-runner.test.ts`); needs `cmdr app install` to take effect.
6. First slice live: rehearsal task, then the approved read-only audit with its prod-gate.
   Install and pairing are the user's acts.
7. Review (separate review Operator, full depth: auth and infrastructure), then land.
