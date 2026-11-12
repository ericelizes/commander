# Missing custody after capacity clears

INT-MISSING-1 is a projection failure: an ownerless open Broken leaf stayed
unheld, but removing only ManagerCapacity let a working Manager make its row
read Working. The shared working-line projection now keeps State missing
before generic Manager activity, at root, nested and gated scopes. Recovery
continues to name the existing work and accountable Manager. Existing live
Operator age/details, receipt precedence and genuine user action priority remain
covered. No workflow state, gate, card or recovery task is created.

## Reproduction and verification

`TestMissingStateSurvivesCapacityClearDuringManagerActivity` covers root and
nested owners (including an inherited child track with NowStep inside it),
idle/working/background-waiting Managers and zero/nonzero unread mail. It checks
capacity exhaustion followed by clearing only capacity, failure ownership and
zero flags. `TestMissingStatePrecedesBusyManagerAtGate` covers the gated path.
`TestMissingCapacityClearScenario` reads
`tests/evals/scenarios/missing-capacity-clear.json`, checks the invariant,
retains one hidden failure beside a genuine user action, and verifies actual
replacement Operator custody clears the failure. The age regression in
`TestBoardBrokenRowBesideOperatorKeepsAgeThroughManagerTurns` now requires the
missing-custody age to survive Manager turns even without another Operator.

The exact frozen 3ebc348 board source was substituted using a Go overlay with
both logical and canonical worktree paths. The new root/nested activity tests,
gated test and interaction eval fail with Working instead of State missing.
The changed source passes the focused combined checks. A broader livedata
package run initially exposed the obsolete age-reset expectation above; its
updated assertion checks persistence of the actual failure instead.

Private evidence is retained under `.commander/fix56/`: before-fail.log,
combined.log, livedata.log, integration.log, renderer61.log, renderer-build.log,
fake-smoke.log, missing.png and cockpit-*.log. Bounded targeted Go commands use
`-p 2`, with a maximum 20-minute package watchdog and private TMPDIR outside any
repository. The separate full-suite validation is intentionally not duplicated.

The two renderer files `overview.test.tsx` and `derived-diagram.test.tsx` pass
61 tests. A fresh private renderer build consumes a snapshot projected by this
candidate's Go source. The headless fake smoke checks State missing, the owning
Manager, zero ask rows and computed failure color equal to `--err`. Visual
inspection shows the red sidebar and failure panel, naming manager-sample-flow
and sample-flow.1. This is synthetic rendering evidence, not native PostgreSQL
or installed-app verification.

A fresh private cmdr and fakeopencode drive detached `cockpit-test.sh`
up/text/keys/text/down using private `/tmp` directories. Captured text includes
`show the board`; teardown reports `torn down`. This is isolated startup/input
smoke, not a claim that the startup fixture reproduces the feature failure.
No real actor, mail service or database is used.

Retained CAP-ALIAS1, R4, TEST1, INT-WITHDRAW-1 and INT-NESTED-1 remain outside
this correction. Historical component/full review evidence remains attributed
to its original heads. The final bead report supplies the new exact head and
delta policy for independent recheck retaining FULL55; no writer review verdict,
installation, renderer activation or shipping is implied.
