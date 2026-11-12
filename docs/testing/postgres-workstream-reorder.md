# PostgreSQL workstream reorder regression

`MoveWorkstream` now uses `database/sql` transactions. SQLite's existing
`_txlock=immediate` connection setting preserves its write-lock behavior.
PostgreSQL takes an exclusive table lock before reading the fleet-wide ranks;
`SetWorkstreamOrder` takes the same lock before replacement. This also serializes
moves when there are no rank rows to lock. Ordinary rank readers remain allowed.

Regression coverage:

- `TestPostgresWorkstreamMovesCompose` moves `c` to first, then `d` to rank 2
  using the original caller order, and checks returned and persisted ranks.
  Before the fix it failed with `syntax error at or near "IMMEDIATE" (42601)`.
- `TestPostgresWorkstreamConcurrentMovesCompose` checks that concurrent moves
  produce one of the two valid serial results, with empty and populated ranks.
- `TestPostgresWorkstreamReorderScenario` consumes
  `tests/evals/scenarios/postgres-workstream-reorder.json`, selects an isolated
  PostgreSQL schema through `state.postgres`, and exercises both
  `tui.MoveWorkstreamTo` and `tui.PlaceWorkstreamAt`, the CLI placement paths.

SQLite SQL audit of `internal/store` (Go and embedded SQL):

- Fixed: the shared `MoveWorkstream` path's raw `BEGIN IMMEDIATE`.
- Retained: `store.go` SQLite-only open/migrate paths use connection pragmas,
  `PRAGMA journal_mode = WAL`, and `PRAGMA user_version`. PostgreSQL selection
  returns through `OpenPostgres` before reaching these paths.
- Retained: `provenance.go` reads `PRAGMA user_version` only after selecting the
  SQLite read-only compatibility backend; PostgreSQL selection returns earlier.
- Retained: `postgres_migrate.go` reads `PRAGMA user_version` from the explicitly
  opened read-only SQLite import source, never from the PostgreSQL destination.
- Retained: frozen SQLite migrations `0001` through `0022` set
  `PRAGMA user_version`; `0011_chat_costs.sql` and `0013_usage_turns.sql` use
  `AUTOINCREMENT`. Legacy tables also rely on SQLite `INTEGER PRIMARY KEY`
  rowid allocation. These migrations are exclusive to the legacy SQLite backend.
- SQLite fixture tests query `sqlite_master` and pragmas intentionally.
  `objective_tasks.go` mentions `INSERT OR IGNORE` in a comment; its executed
  statement uses portable `ON CONFLICT DO NOTHING`.

No additional SQLite-only SQL was found on shared PostgreSQL runtime paths.
No applied migration was modified. This change requires no installation or
service restart to validate in the private test database.

Local verification evidence (private PostgreSQL at localhost:55432): the dedicated
store run passed 238 tests/subtests, with three opt-in rehearsal/load/diagnostic
tests skipped. The initial targeted rank and interaction run passed 11
tests/subtests across two packages. The detached `cockpit-test.sh text` smoke
rendered `(no projects yet)`, `FLEET`, and `no operators running`; it was then
shut down. That smoke verifies isolated rendering, while the PostgreSQL scenario
above verifies reordering. No installed binary, daemon, or main branch changed.

The broader `go test -p 1 ./... -json` run recorded 5,648 passing tests/subtests
and 25 skips, but did **not** pass: `internal/livedata` failed
`TestLoadUnderChurnScenario` after one retained last-good load (expected zero),
and `tests/evals` hit Go's default ten-minute package timeout while running
`TestCommanderEvalScenarios/takeover-sweep`. Package outcomes were 61 passed,
7 skipped/no-test packages, and 2 failed. These are reported as validation
limitations; no unrelated production code was changed to address them.
A targeted rerun of `TestLoadUnderChurnScenario` then passed (1 test, no code
changes); the original failure remains recorded in `.scratch/all.json` and the
rerun in `.scratch/churn-retry.json`.
