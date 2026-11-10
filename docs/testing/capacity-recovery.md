# Capacity refusal and recovery

`TestCapacityEngineSnapshotRecovery` uses the real Engine, account planner,
usage store, current-schema SQLite store and `livedata.Source.Snapshot`. The
provider is the existing re-executed fake Claude; the bead backend is a
`MockClient` with an invented parent and two tracks. No real account is accessed.

The fake provider emits a rejected rate-limit event and an error result. With
the other Claude account exhausted and the runtime pinned to Claude, the Engine
refuses the automatic move. A subsequent start also returns `ErrNoUsage`.
`Source.ManagerCapacity = Engine.CapacityFailures` makes the parent and both
tracks read `Manager out of usage`. Restoring only the alternate account starts
an actual fake process on that account, completes a turn, clears capacity state
and retains the Claude pin. The now-idle, unplanned rows read
`Not started: Manager to plan`.

Run the automated flow with:

```sh
go test -p 2 ./internal/chat -run '^TestCapacityEngineSnapshotRecovery$' -count=1 -v
```

For live verification, first build this checkout's `cmdr` and
`tests/evals/fakeopencode` through `cmdr internal-slot run`. Create an isolated
cockpit with `scripts/cockpit-test.sh up`, supplying those binaries via
`COCKPIT_CMDR` and `COCKPIT_FAKE_OPENCODE`, a fresh `COCKPIT_HOME` and a short,
uniquely owned `COCKPIT_TMUX_TMPDIR`. Initialize its `ch/state.db` with the current
`store.Open` before starting concurrent cockpit readers.

Run the same targeted test with these additional variables (absolute paths):

```sh
CMDR_CAPACITY_COCKPIT_HOME="$COCKPIT_HOME" \
CMDR_CAPACITY_COCKPIT_SOCKET_DIR="$COCKPIT_TMUX_TMPDIR" \
CMDR_CAPACITY_COCKPIT_BINARY="$COCKPIT_CMDR" \
CMDR_CAPACITY_COCKPIT_ARTIFACTS="$artifact_directory" \
go test -p 2 ./internal/chat -run '^TestCapacityEngineSnapshotRecovery$' -count=1 -v
```

The test verifies the cockpit owner and server token, serves live snapshots on a
private loopback HTTP endpoint, and runs the built `cmdr board` in a dedicated
detached cockpit window. It uses the HTTP endpoint, without `--snapshot` or a
fixture display block. `scripts/cockpit-test.sh text` must capture the refusal
and recovery on all three rows. The test saves `refused.txt`, `recovered.txt`,
the corresponding snapshots and `provider.jsonl` under the artifact directory.
The provider and HTTP server end with the foreground test; its window is removed.
Always finish with `scripts/cockpit-test.sh down` using the same cockpit variables.
If reusing it, a stale socket after teardown is handled only as the script directs,
after verifying the server is gone.

This proves Engine → Source snapshot → HTTP → cockpit board rendering. It uses a
test HTTP adapter rather than a running cmdrd daemon, and does not claim real
provider recovery or installation verification. The independent review of the
original candidate remains historical evidence; this test is no source review.

`TestNestedCapacityOwnerActivity` and eval
`TestNestedCapacityOwnerScenario` (`nested-capacity-owner.json`) distinguish a
nested owner's idle/stuck state from an absent/idle/stuck/working root Manager.
They cover inherited child tracks and retain the exhausted/recovered presentation.

## Current-main reconciliation

The integration candidate starts at
`77d816f0165025a1a9e610204e88aae62e6f9272` and carries only the capacity delta
from `07ffcce26037660f497844da54df0832b57ba4af` to
`ec822ce8420eb167b362ffead7faf65e13d3a6c9`. Unified Commander account planning
already exists on that target; `accountplan.go` is unchanged.

The 23 source-delta paths retain identical added/deleted lines. The only
three-way conflict was in `internal/livedata/snapshot.go`: retain the target's
PostgreSQL unknown-process degradation check, then populate ManagerCapacity.
The daemon callback and nested-owner board changes apply around current target
code without replacing its PostgreSQL execution semantics. Canonical Store,
schema, selectors, hooks, core guards and secret policy are outside this delta.

Targeted account/usage/fallback, capacity, ownership mail routing and nested-owner
tests and evals passed on the integrated tree, as did the private fake-provider
cockpit refusal/recovery flow above. The latter still uses SQLite and mocked
beads with a test HTTP adapter; it proves neither PostgreSQL integration nor real
provider recovery. The new integration head needs independent current-scope
review; historical CLEAN reports do not transfer. Full-suite verification is
pending the known baseline fixture repair and Manager disposition. No normal
ship, installation, restart or live account switch is part of this preparation.
