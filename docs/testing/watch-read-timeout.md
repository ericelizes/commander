# Watch read deadlines

The minute watch uses a three-second `Source.WatchSnapshot` context. Its
independent state reader prevents waiting behind API presentation work, but a
cold or invalidated graph still needs real database reads. One slow read is not
evidence of a persistent outage.

`SnapDegraded.Timeout` carries deadline classification from the PostgreSQL
load's wrapped error/context, without parsing display text. Joined failures are timeouts only when every
underlying failure is a deadline miss; concrete project errors take priority.
The watch suppresses
the first two consecutive deadline failures and alerts on the third (normally
about two minutes after the first miss). Healthy snapshots reset the streak.
Every failed read still stops check-ins and issue processing for that pass;
there is no synthetic healthy or "all on track" result. The streak is in memory
and resets when the daemon restarts.

Missing/closed/locked databases, schema errors and other concrete read errors
alert on the first failure. A concrete error after a timeout alert bypasses its
repeat window once, so the new evidence is promptly visible. Mail subjects and
bodies distinguish timeout from error. Timeouts advise inspecting latency/load,
without suggesting schema upgrades. Existing thirty-minute repeated-alert
suppression and recovery cleanup remain in effect.

## Slow beads phase investigation

Read-only production measurements on November 12, 2026 used `psql` with
`default_transaction_read_only=on` and a fifteen-second statement timeout. No
schema/data/configuration changes, installation or restart occurred.

`objectiveRowsTimed` in `internal/livedata/livedata.go` rebuilds changed project
graphs inside a repeatable-read transaction. `projectObjectiveRows` performs four
listings (objectives, gates, tasks and operational beads), then hydrates parents,
dependencies and comments. Closed records are included before folding the graph.
The shared cache avoids this work only while the project revision is unchanged.
`commander_state_event` in `internal/store/postgres_schema.sql` increments that
revision on bead-task, dependency and comment writes, rather than only parent
edge changes. Consequently progress/brief changes and report comments can
invalidate the graph cache even when its topology is unchanged. Agent-state
writes alone do not increment this revision. Narrowing invalidation requires
preserving the dynamic task fields and comment-derived facts currently
projected by the reader.

The live projects contained 543, 6,556 and 3,602 beads. A Commander objective
listing using `pgBeadColumns` took 71.746 ms in EXPLAIN ANALYZE, versus 2.405 ms
for basic id/title/labels/parent fields. The full projection includes correlated
dependency/comment counts, recursive planning-owner resolution and execution
capacity/question checks for each row. Subsequent task listing plans took
398.306 ms (Commander) and 404.323 ms (Vibe Apps). Vibe Apps has 7,767 comments,
approximately 15 MB of text; Commander has 4,248 comments, about 7.2 MB. The
Vibe Apps comment query's server-side sort/read alone took 97.240 ms.

A read-only wall-time probe executing four listings plus all project comment
transfer/rendering to `/dev/null` took 0.264 s for RCM, **3.054 s for Vibe Apps**,
and 1.352 s for Commander. This is a diagnostic approximation: it uses psql
formatting and all project comments rather than the application's selected-ID
hydration, and excludes application graph assembly. It establishes that the
existing broad graph read/hydration can exceed two seconds on the current data;
it does not prove which query consumed the exact historical 2.309-second phase.
No historical query trace is available. Projects are read concurrently, so their
costs should not be summed to estimate snapshot latency.

No small safe query-only fix was identified. Removing fields/comments or
splitting the planning/capacity projection changes authoritative graph semantics
and cache invalidation requirements. That optimization is left for separate
work; this change addresses the false-alert policy without increasing the read
deadline or presenting stale state as healthy.

## Coverage

- `TestWatchTimeoutStreak`, driven by
  `tests/evals/scenarios/watch-read-timeout.json`: isolated miss, recovery/reset,
  sustained misses, no check-in on failed state, timeout-specific mail, immediate
  concrete error after a timeout alert.
- `TestReadErrorAfterOneTimeoutAlertsImmediately`: a schema error following one
  deadline miss alerts without waiting for the timeout threshold.
- `TestPostgresWatchTimeoutClassification`: actual expired-context and closed
  database reads through the independent state reader on private PostgreSQL.
- `TestDeadlineOnlyPreservesConcreteProjectErrors`: wrapped/joined deadlines
  and concrete errors mixed with deadline misses.
- Existing unreadable-state/store-error tests retain immediate-error coverage.

Execution logs are in the worktree's private
`.commander/watch-timeout-evidence/` directory. Full-suite and detached cockpit
outcomes are recorded in the assigned bead's final report.

The initial full Go invocation passed all non-eval packages, including the
private PostgreSQL store suite, but its eval package reached Go's default
10-minute package timeout while `TestCommanderEvalScenarios/stale-manager-route`
was running. No preceding assertion failure was reported. An extended-budget
serial rerun is recorded separately in the final bead report.

Renderer validation found an existing stable failure in
`app/test/jurisdiction-owner.test.tsx:20`: it expects a default Commander owner
label that the current Navigator hides when root rows have one distinct owner.
The full run passed 104 files / 1,029 tests and failed this one test; an isolated
rerun reproduces it. The app tree is unchanged from main. Typecheck passed.
Detached cockpit smoke passed and rendered `(no projects yet)` / `Tell Commander
to add a project`, then tore down. No installed-live behavior is claimed.

The extended serial run (`go test -p 1 -timeout 30m ./...`, with the required
identity variables cleared and private PostgreSQL enabled) passed on main base
`11abef31` plus this fix. The eval package completed in 732.299 seconds.
Subsequent main integration and its checks are recorded in the final bead report.

After rebasing onto main `f738b5b1`, targeted Go integration checks passed across
daemon, livedata, nextactor, supervision, overview, API and CLI, along with a
fresh build. The repeated renderer run passed 1,027 tests and failed three in
two files: the ownership-label assertion above and two overview expectations
for `Earlier recap` / `Writing the recap…`, whose text changed in incoming main.
This change has no app edits; app files match the parent main commit. These
renderer failures are preserved as baseline limitations rather than repaired
within this watch-policy task.
