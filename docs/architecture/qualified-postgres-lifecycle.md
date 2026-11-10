# Qualified PostgreSQL dependency candidate

This is a source candidate for independent review. Do not install it or open a
configured live store to try it: `OpenPostgres` applies migrations. Schema 2 is the exact reserved Mac places migration from
`158f0064d08c0e5bfd815917c081ca22fa7b67ac`; schema 3 adds qualified dependencies.
Fresh databases and schema 1 upgrade through places 2 before qualified 3 in one
transaction under advisory migration lock `734031820`. Existing exact Mac 2
upgrades to 3 with all places, runs, comments, indexes and event triggers retained.
The store includes Mac claim/heartbeat/presence persistence, without enabling the
Mac execution service or app. Shared host/remote claims take the qualified graph
lock before task rows. Transactional custody honors remote heartbeat evidence
rather than local process absence. Release remains subject to independent review
and the [Mac release prerequisites](../releases/mac-schema-two-contract.md).
Old schema-1/2-only binaries reject 3 and are not recovery binaries.

The public `beads.Client` remains ID-only. Qualified `(project,id)` identities are
private to PostgreSQL graph validation. Cross-project references require each
reached foreign component's IDs to be unambiguous across registered projects.
Unrelated duplicate IDs remain valid storage. Missing, deleted, wrong-project or
ambiguous reached endpoints refuse; arbitrary absent IDs never redirect to the
first matching project.

The guarded command accepts explicit expected registered project slugs:

```
cmdr internal-dep --project SOURCE --target-project TARGET add SOURCE_ID TARGET_ID
cmdr internal-dep --project SOURCE --target-project TARGET rm SOURCE_ID TARGET_ID
```

These are syntax examples, not authorization for live writes. SOURCE's directory
must resolve to exactly one registered project. The write transaction resolves
both paths, checks the exact endpoint projects and globally counts their live
IDs, then checks the source's nearest registered Manager or active Operator run.
A user without an agent role retains user authority. Only `blocks` edges use this
route. Gate and track endpoints remain refused; the existing internal-gate route
is unchanged. The foreign task is never mutated by dependency add/remove.

The versioned migration backfills `target_project_id`, replaces the target FK,
requires non-null target identity and enforces local parent-child dependencies.
Local insert statements acquire their source project as the default target.
Database triggers serialize task/identity, dependency, project registration and
Manager lineage changes with the writer's global advisory transaction lock.
Run claims acquire that lock before the task row lock. Qualified traversal
refuses more than 4,096 nodes or 16,384 edges; exhaustion is never acyclicity.
Opposite dependency writes serialize before checking the qualified blocks graph.

Reads use repeatable-read MVCC and validate stored endpoint provenance before
flattening IDs. List, Show/FreshShow, Details/FreshDetails, Deps and Dependents
share this policy, including custody reader wrappers. A project-wide qualified
closure includes reachable foreign parents, children and dependents; local-only
projects retain their indexed reader path. Parent columns also supply traversal
edges, so missing redundant parent-child records cannot hide a foreign ancestor.

The write transaction validates the actual before/after qualified holding graphs
using the same holding predicate and label derivation as the CLI/API. This also
covers no-Intent and unsimulated requests. A target close, park or supersession
that newly leaves a foreign source unheld rolls back every mutation and event.
Its source owner must establish genuine source-local custody first. No foreign
receipt is generated or redirected. Existing preflight checks still refuse
foreign receipt sets before requesting writes.

A transactional global graph revision invalidates display reuse after foreign
state or identity/registration changes. The revision is read within the graph's
MVCC transaction. Foreign blocker completion is hydrated into DoneBlockers
without drawing the foreign project as local work. Ambiguity returns a typed
project read error and no cached rows; it cannot silently erase a wait.

## Verification

Native private `commander_trial` coverage:

- `internal/beads`: `TestPostgresQualified*`, including an interaction scenario
  in `tests/evals/scenarios/qualified-postgres-lifecycle.json` (close, park,
  supersede), wrong projects, later duplicates, tombstones, bounds, local-parent
  constraint, source authority, opposite-link race, wrappers and MVCC.
- `internal/livedata`: `TestPostgresQualifiedDisplayRevisionAndAmbiguity` checks
  foreign open/completed blockers, revision invalidation and no stale reuse.
- `internal/store`: `TestPostgresComposed*` verifies fresh/1/2 convergence, exact places SQL,
  populated upgrades, migration lock serialization and failed-upgrade rollback.
  `TestPostgresPlaceClaimAndPresence` and existing native claim/presence tests
  retain the Mac lifecycle contracts.
- `cmd/cmdr`: `TestPostgresQualifiedDependencyCommand` exercises actual add/remove
  command handlers with no retired `state.db` file.

The finite foreground controller is copied from the verified PostgreSQL 16.15
private delivery into this task's ignored `.commander/qualified-native` directory.
It verifies the retained initdb/postgres hashes, uses fresh private HOME and
COMMANDER_HOME plus a Unix-socket-only server, runs exact compiled test binaries,
checks schema cleanup, stops/waits for PostgreSQL, and removes database data.
No configured database, credentials, global service, real actor or paid agent is
used. Native integration tests skip without the explicit disposable DSN; skipped
suite tests are not native evidence. Cockpit verification uses the detached fake
runtime and establishes startup/interaction smoke, not PostgreSQL behavior.
