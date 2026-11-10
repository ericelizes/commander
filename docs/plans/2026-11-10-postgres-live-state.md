# One authoritative Commander state service

Authorized scope: one PR, followed by migration and cutover on Eric's
workstation VM. Default to local PostgreSQL; the same DSN interface supports a
remote PostgreSQL server. Preserve all current workflow authorization and
review guards. Do not create a second task list or silently fall back to Dolt
or SQLite after cutover.

## Impact

The UI and CLI must agree on the same active run IDs, including nested
Managers. Runtime events update activity without a model call. Model workers
interpret only changed chat/tool evidence; unavailable classification never
blocks claims, activity, or streaming. Agents on other hosts use Commander’s
authenticated API rather than opening a shared database file.

## Implementation

1. Native PostgreSQL storage for existing Commander records and the required
   bead lifecycle, hierarchy, dependencies, labels, comments and ordering.
   Keep the existing `beads.Client` seam and guarded write door. Remove process
   spawning, export hydration and file-fingerprint caching from the PostgreSQL
   read path. Configured PostgreSQL failures must be explicit.
2. Transactional state events and replayable delivery. Distinguish database
   identity allocation from commit ordering: a transaction that commits late
   must not be skipped by a replay cursor. UI reads current state on connection
   and then consumes committed events. Reconnect replays or explicitly reloads.
3. Run-bound claims and observed runtime activity. Avoid conflating assignment
   with execution. Every count/spinner uses one normalized run set. Reject old
   runs' updates after replacement; reconciliation handles missed exit events.
4. Bounded classifier jobs using normal connected Codex/Claude accounts with
   Luna/Haiku at low effort. Coalesce new evidence, deduplicate work, retain
   evidence/cursor provenance and reject superseded results. Track tokens and
   provider allowances. No arbitrary 100-call daily ceiling and no paid API
   fallback. Model output cannot approve, deploy or close work.
5. Offline migration from a consistent SQLite backup and complete Dolt exports
   into an isolated PostgreSQL database. Preserve IDs, all rows and dependency
   edges, labels, comments, approvals, ranks and active run associations. Compare
   before cutover. Quiesce writers, take final backups, migrate, fence retired
   write paths, install the reviewed revision, and verify live behavior.

## Validation

Native Postgres integration tests must cover concurrent claims/writes, atomic
operations, rollback, migration on populated data, reconnect replay including
out-of-order transaction commits, worker recovery and account exhaustion.
Synthetic load target: 1,000 agents, 100 events/second; p95 board reads under
200 ms and normal runtime-event-to-UI delivery under one second. These are
acceptance targets, not measured capacity. Unit suite, interaction eval and
isolated cockpit verification are required. Never use real agent sessions as
test fixtures. Review the exact final commit and cut over once; do not install
partial implementations.

## Boundaries

No full upstream Beads parity, Dolt-style branching/merging, multi-region
active-active deployment, additional task store, Kafka or Redis. Current
backends remain available only before cutover and as retained recovery data;
PostgreSQL mode does not mix their writes. Post-cutover rollback must preserve
new writes, not simply restore an old snapshot.

Upstream Beads 1.3.1 rejects its removed PostgreSQL/MySQL/SQLite backends.
This work implements Commander’s required bead operations natively; it must
not claim unmodified upstream `bd` supports Postgres.
