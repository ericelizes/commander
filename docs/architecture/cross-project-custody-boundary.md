# Cross-project custody boundary (source unit 1)

This document describes the first source unit. The subsequent provisional PG
lifecycle candidate is documented in [qualified PostgreSQL lifecycle](qualified-postgres-lifecycle.md);
its migration remains release-blocked pending version coordination and independent review.

This candidate adds the custody boundary required before a PostgreSQL reader can
expose foreign dependency endpoints. It does **not** enable cross-project reads
or writes in the PostgreSQL adapter. Its local-only dependency schema, lookup,
serialization and cache behavior are unchanged. Contract A remains the intended
identity model: ID-only public graphs, qualified internal traversal, with
unambiguous referenced IDs across registered projects.

`beads.CrossProjectError` preserves an underlying error and survives wrapping.
The CLI closure preflight and HTTP intent planner refuse these errors instead
of taking their legacy local-read fail-open path. Local-only clients keep that
path. No holder, access, gate or timer is inferred from an error.

`beads.CustodySnapshotReader` is an optional backend capability. A supporting
reader must validate qualified provenance before flattening IDs and carry one
MVCC snapshot through the callback context. Nested callbacks must reuse it.
`holdread.Load`, CLI closure planning, HTTP intent planning and direct Manager
receipt settlement enter this boundary. The reader's `RequireLocalCustody`
validates the entire proposed receipt set in that context. The CLI and HTTP
planner call it before recording a leave-unheld exception or adding receipts;
direct receipt settlement checks again before queuing its first write. A
foreign source requires its owning Manager to establish genuine local custody
separately. These hooks grant no permission for foreign writes.

An implementation of this interface must return `CrossProjectError` for unknown,
ambiguous, missing, stale, unreadable or exhausted foreign provenance. Reader
wrappers must forward the capability and preserve its context. No current PG
reader implements it in unit 1. The interface is a prerequisite, not a proof of
transaction-time safety or a substitute for the write transaction's validation.

## Dependent source unit 2

Before enabling foreign edges, complete all of the following together:

- Version and backfill `target_project_id`; retain source FK, replace target FK,
  enforce local parents. Exercise migration in an isolated approved trial only.
- Require expected registered endpoint projects and unambiguous referenced IDs
  inside the write transaction. Check actual source lineage/Manager or Operator
  custody and gate restrictions there; mutate source edges only.
- Serialize graph and identity mutations globally, including registration,
  import, creation and parent edits. Traverse qualified nodes with explicit
  size/depth refusal and test concurrent opposite links.
- Implement the snapshot capability across List(IDs), FreshShow, FreshDetails,
  Deps and Dependents. Do not redirect arbitrary missing local IDs. Preserve
  provenance through wrappers and every custody path, including paths without
  an intent, unsimulated operations, gate/close checks and handoff release paths.
- Audit legacy fail-open callers beyond the two planners; typed foreign failures
  cannot be discarded. Refuse target closure needing a foreign receipt even for
  the user. Revalidate in the mutation transaction to close read/write races.
- Validate display caches against qualified revisions plus identity/registration
  changes; invalidate on a newly introduced duplicate. Use the same validated
  snapshot for custody and blocker display, including folded DoneBlockers.

Required unit-2 evidence includes wrong projects, duplicate introduced after an
edge, deleted targets, nested source ownership, foreign gate refusal, global
cycles and races, read/custody errors, foreign receipt refusal, source-only
mutation and cache invalidation. Unit 1's tests use invented in-memory fixtures
and the real HTTP write door; they are not PG fixture evidence. Missing external
test access remains a separate unsolved prerequisite.

## Verification

Targeted tests: `go test -p 2 ./internal/api ./cmd/cmdr -run
'TestCrossProjectCustody|TestDoorIntent|TestDepRmRunsTheClosureCheck|TestHoldForManager'`.
The private-handler eval `TestCrossProjectCustodyScenario` loads
`tests/evals/scenarios/cross-project-custody-boundary.json` and exercises the
real HTTP write door with an isolated capability provider. It asserts zero
writes on typed read failures and foreign receipts, preservation of the local
receipt-before-close behavior, and reuse of the snapshot for nested reads.
Cockpit smoke checks cover startup and interaction; they cannot establish PG
cross-project behavior that this unit deliberately does not implement.
