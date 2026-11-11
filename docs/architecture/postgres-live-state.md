# PostgreSQL live state

When `COMMANDER_HOME/state.postgres` exists, it selects one canonical PostgreSQL
backend for the bead graph and Commander runtime records. The file must be a
regular private file (0600) containing a PostgreSQL URI. Keep it out of git,
logs, command arguments and screenshots. An invalid or unavailable configured
backend fails closed; it never selects the retired SQLite/Dolt stores.

Existing installations without the marker retain their original backend. The
Postgres implementation supports Commander's `beads.Client` contract; it does
not implement arbitrary upstream `bd` SQL, Dolt branches or full upstream CLI
parity. `cmdr bead --project-dir PATH` supplies reads and attributed comments.
Task/approval mutations continue through Commander's existing guarded doors.
Postgres dispatch uses host Claude/Codex Operators, including when Docker is
available. The old Docker/opencode launch path is not a Postgres backend.

## Data and interfaces

- `bead_tasks`, `bead_dependencies`, `bead_comments`: one task hierarchy, labels,
  ordering, assignees and comments, with complete imported source JSON retained.
- `agent_runs` plus `bead_tasks.active_run_id`: an atomic exclusive run claim.
  The claim commits before starting the worker. Failed launches close the run.
- `agent_state`: observed turn/tool state. Operator hooks carry immutable run ID
  and session; updates from replaced runs are rejected under the claim lock.
  Presence alone never establishes a working turn. Claim replacement invalidates
  prior activity and summaries. A long tool remains working until a turn event
  or process evidence says otherwise; ten minutes of silence is not an idle turn.
- `run_observations`, `native_presence`: process observations persisted by one
  bounded sampler. Database reads supply the UI runtime set. Missing, failed or
  expired observations mean unknown, not stopped. Row, nested-workstream and
  fleet working counts use the same observed execution set.
- `state_events`, `event_deliveries`: transactional outbox and committed replay
  positions. Database sequence allocation is not commit order; delivery positions
  are allocated after commit. LISTEN/NOTIFY wakes refreshes but is not the log.
- `agent_sessions`, `classification_jobs`, `classification_usage`: bounded source
  evidence, coalesced interpretation and durable allowance accounting. Derived
  bead status is fenced against the current session, source cursor and run.

The browser reads an initial API snapshot, then receives fresh frames over the
existing authenticated WebSocket. Reconnect reloads current database state.
`GET /api/state/events?since=N&generation=G` also exposes durable replay; a
changed database generation or expired cursor returns 409 with `resync:true`.
The browser does not poll every ten seconds. A 30-second backend reconciliation
recovers a lost notification and time-based transitions. Unavailable state hides
previous rows and reports unknown counts while independent chat remains usable.

Each task graph read uses a read-only repeatable-read transaction. A graph may
be reused in memory only after its database revision is read in that transaction
and matches. Runtime state is read again; graph read errors never reuse old rows.
Mutable workflow/PR enrichment is copied for each frame. This avoids rebuilding
unchanged task structure on every runtime event without a saved-board fallback.

A bounded publisher drains the outbox independently of API consumers. Replay
history is retained for 48 hours; pruning advances the replay floor. Bead,
comment, approval, decision, run and chat history are not pruned by that job.

## Cheap interpretation

Classification uses connected normal subscription accounts: Codex Luna at low
reasoning, then available Claude Haiku at low effort. No API-key billing or
premium-model fallback is enabled. Tools, MCP, hooks and repository instructions
are disabled in the classifier child. Output is bounded structured text; it
cannot approve, close, deploy, change claims or manufacture runtime activity.
Recorded gates, waits and needs-user decisions retain their authoritative text.

Two workers coalesce evidence by role. Requests reserve allowance before launch;
unknown outcomes keep their reservation. The default ceiling is 500 million
input+output tokens per UTC month, configurable through
`COMMANDER_STATUS_MONTHLY_TOKENS` (0 disables requests). This is a local token
allowance, not a provider dollar-price guarantee. Actual consumption is recorded
in `classification_usage` and the existing usage ledger. There is no fixed
100-checks/day cap. Failed/exhausted accounts cool down; jobs wait when no cheap
account is available. Existing allowance limits still apply.

Native chats provide normalized evidence. New host Operators use activity-only
hooks, alongside existing mail/guard hooks. Surviving pre-cutover host Operators
without run-aware launchers have their existing stdout normalized through a
bounded, run-fenced compatibility reader until they finish. They are not killed
or restarted to migrate state.

## Migration and cutover

Prepare and rehearse while the old system runs; the rehearsal is not the final
cutover export. All paths below are private operational paths, never git assets.

1. Build the candidate CLI. `cmdr storage postgres prepare --source OLD/state.db
   --dir NEW_PRIVATE_DIR` takes a read-only SQLite backup and fresh full Beads
   exports for every registered project. Cached `.beads/issues.jsonl` is not a
   migration source. Save complete Dolt source backups as well to retain its
   non-task tables and history.
2. Provision a separate local Postgres database with durable storage, private
   credentials and backups. A DSN may instead select a remote Postgres server.
   Do not use the disposable integration-test database for live service.
3. `cmdr storage postgres migrate --dir PRIVATE_DIR --dsn-file PRIVATE_CANDIDATE`
   imports into an empty candidate only. It preserves identities and advances
   sequences. SQLite table counts and full-row digests must match. Bead IDs,
   edges and comments are preserved; unknown source fields remain in source JSON.
   Conflicting parents, missing targets or ambiguous running claims stop import.
   Legacy cost history uses project ID 0 for fleet events; its generated nullable
   project reference preserves 0 while enforcing positive project references.
4. Final cutover briefly quiesces Commander state writers, fences legacy `bd`
   entry points and freezes old stores, takes a fresh consistent export/backup,
   imports a fresh live candidate, verifies parity, then atomically selects the
   private `state.postgres` file. Install the one merged revision and restart
   cmdrd, leaving the independent Operator tmux keeper running. New/native chats
   resume through the existing chat ownership mechanism.
5. Verify live API availability, current claims/process observations, counts,
   an actual streamed state transition, reconnect/failure behavior, classification
   accounting and the retained approval/hold records. Keep the old stores fenced.

Before selection, aborting leaves the old backend authoritative. After any new
Postgres write, rolling back to an old SQLite/Dolt snapshot would lose work.
Preserve Postgres writes and backups; use the previous compatible binary against
Postgres or repair forward. Backend rollback requires an explicit reconciled
reverse migration and is not an automatic file rename.

## Boundaries and verification

This is one VM and one Postgres instance. It removes SQLite's single-writer
constraint and per-agent Beads subprocess scans; it is not high availability or
multi-region replication. Project locks protect local hierarchy edits. Qualified graph/custody writes
also serialize installation-wide through migration 5's graph lock. Store.WithTx
acquires that lock before callbacks can take project, mode, slot or row locks;
statement triggers retain the same serialization for direct writes. This also
serializes unrelated Store.WithTx callbacks, a deliberate conservative boundary.
Read-only repeatable-read snapshots and direct accounting-only writes keep their
existing paths. Small connection pools, bounded event batches and bounded
classifier children protect the VM.

Mac acceptance is a fresh launch boundary. Within the token-install transaction,
it rechecks current task/ancestor restrictions, active project, exact running
canonical run/claim and orchestration mode. Active mode additionally requires the
same enabled bound slot and current plan revision/responsibility epoch; binding
stores the post-binding epoch. A shadow offer cannot become an active claim, and
an active claim cannot launch after a change to shadow/off. Token generation runs
only after this fence; a refusal stores and returns no token. Each token request
revalidates current authority too. An offered run never proves a surviving
physical process. These checks do not implement active remote scheduling.

`COMMANDER_POSTGRES_TEST_DSN` accepts only the disposable `commander_trial`
database and creates a unique schema for each test. Unit, interaction and load
fixtures start no paid agents. The explicit load scenario uses 1,000 synthetic
executions and targets 100 runtime writes/second with p95 reads below 200 ms.
It does not claim 1,000 model processes fit on the VM. Run the isolated cockpit
script and UI checks as well as the Go suite before shipping.

## Runtime integrity upgrade

Schema 3 upgrades existing schema 2 in place; fresh installations traverse the
same numbered migrations. Shipped SQL remains frozen. The new version adds
run-bound classification evidence and attested planning-runtime retirement
receipts, preserves task/decision/run history and progress, and resolves only
historical supervision jobs without pending saved answers. An installed schema
is never repaired by silently skipping required columns or falling back to files.

The explicit restored-production rehearsal accepts only a dedicated
commander_rehearsal_* database on the separate localhost trial server (55432).
It fingerprints pre-existing durable facts before/after migration and checks
consolidation separately, allowing only its intended planning-owner/epoch changes.
No model runtime starts during this rehearsal.
