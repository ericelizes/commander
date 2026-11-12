# Watch state availability versus snapshot presentation

`daemon.New` supplied the watch with `Source.Last`. In PostgreSQL mode,
`Last` calls `Current` with a three-second context. `Current` and `build`
used the same `Source.mu` as API snapshot presentation. That mutex spans
mail callbacks, classification evidence, persisted board clocks and snapshot
callbacks. Waiting for it can spend the watch's entire deadline even when
PostgreSQL state is readable. `Current` then clears its canonical frame and
assigns `Current state is unavailable`. The API snapshot can finish healthy
on its independent, longer-lived context.

The live read-only evidence was `~/.commander/logs/cmdrd.err`: DEGRADED at
19:27, 19:28, 19:29 and 19:31 UTC on November 11, with repeated classifier
read-unavailable messages. These logs do not identify which optional operation
spent each individual deadline. The private PostgreSQL regression establishes
the causal failure without attributing every live stall to mail or a query.

`Source.WatchSnapshot` now uses a fresh projection builder with the same
independently synchronized graph cache and fresh required database reads.
It omits optional classification, persisted display-clock writes and presentation
callbacks. The daemon consumes its canonical projection, without rebuilding
runtime states or applying an unrelated API load's `LastLoadErr`. Terminal
`Last` and API presentation retain their existing enrichment.

`TestPostgresWatchReadsDuringSnapshotPresentation` consumes
`tests/evals/scenarios/watch-state-isolation.json` and runs against a unique
`commander_trial` schema. It holds a snapshot callback for four seconds:

- Negative control using the former shared `Current` builder fails after four
  seconds with `Reason:Current state is unavailable`.
- The independent watcher reads the current project during the blocked callback
  within its three-second budget, despite an unrelated saved load error.
- The API snapshot and the watch immediately after it finish healthy.
- Closing PostgreSQL makes the next watch projection degraded.

The 40P01 messages in the live log at 19:02 and 19:12 predate the 19:20 install.
They are reported by `SetAgentStateForRun` at its `FOR UPDATE OF b,r` claim
query (`internal/store/run_claims.go`). Those write transactions are separate
from the ordinary SELECTs used to read required snapshot/runtime state. This
change neither diagnoses the competing writer's lock order nor repairs those
deadlocks. They can contribute load, but are not required to reproduce this
false availability alarm.

No installation, restart, merge or live database mutation is part of this fix.

Verification for the candidate:

- Targeted livedata concurrency/state and daemon Commander/degraded tests pass
  with disposable PostgreSQL; the regression checks failure after DB close.
- All `internal/livedata` tests pass (23.374 seconds).
- Race checks for the new regression and concurrent changing snapshots pass
  (9.764 seconds).
- `go build ./...` passes inside `cmdr internal-slot run`.
- Detached `cockpit-test.sh up/text/down` passes inside a build slot; text shows
  `(no projects yet)` and `Tell Commander to add a project` in its isolated home.
  This is a smoke verification; the PostgreSQL overlap proof is the regression.

Full-suite limitation: the first `go test ./...` inherited execution identity
and failed decision fixtures with `execution sessions cannot record or apply
human decisions`. A second run cleared those variables only in test subprocesses;
`cmd/cmdr` and `internal/api` passed. It failed the unchanged
`TestFreshDetailsSeesDBEdgeBehindDisplayExport` because installed `br init`
rejects `--no-auto-flush`. After these failures were established, the remaining
broad eval workloads were stopped through their exact dedicated testguard
scopes. No full-suite or full-eval pass is claimed. The new targeted PostgreSQL
eval scenario completed successfully separately.
