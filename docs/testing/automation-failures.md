# Owned automation failures

`stalled` and `stuck` are red failure classes. They do not imply a user
flag or an approval request. The display keeps the concrete cause, accountable
Manager, existing recovery bead and next action in `why`; the sidebar shows it
and the workflow/diagram carries it. Manager activity or unread mail cannot
complete an overdue handoff or turn a stopped run green.

Operational beads remain in the projected dependency graph even without
objective/task labels. Closed reports cannot keep custody outstanding despite
retained historical review/wait labels. An ordinary action card with a generic
Continue/Approve label is not an approval request without `approves:true`.
Existing genuine gate authority and user cards remain in effect.

The time-wait watcher includes task-only handoffs. Before notifying it rereads
the exact receipt and current custody, refuses closed/deleted/parked/user-held
or replacement-Operator custody, and retains a receipt for one successful
notification. It routes through existing Manager mail delivery. The persisted
Commander evidence watch escalates otherwise unreported failures once per
unchanged failure; generic lack of delivery retains its existing observation
window. No new worker, review, gate or card is created. Run restart budgets,
exclusive claims and generation/current-holder guards stay in canonical
dispatch/settle. Notification is repair routing, not evidence that repair
succeeded; the error remains red until its underlying evidence changes.

## Verification

Unit tests in `internal/livedata/recovery_test.go`,
`internal/daemon/automation_failure_test.go` and
`cmd/cmdr/approval_action_test.go` cover operational blockers, closed reports,
Manager reaction, planning/recovery receipts, moved custody, persistent
notification/escalation deduplication and ordinary access actions through the
actual ask door. The interaction scenario is
`tests/evals/scenarios/automation-failure.json` (`TestAutomationFailureScenario`).
Board/workflow/diagram/check-in regression tests exercise the existing
projection paths. The selected renderer suites (`overview.test.tsx` and
`derived-diagram.test.tsx`) pass 61 tests.

The isolated cockpit used `COCKPIT_TMUX_TMPDIR=/tmp/cl-fail-617` and
`COCKPIT_HOME=/tmp/cl-fail-617-home` with `scripts/cockpit-test.sh`:
`up`, `text`, `keys 'show the board' Enter`, `text`, `down`. Captured text
contains the detached fake cockpit and the supplied text. It starts no real
agents and touches no visible monitor. It was torn down after capture.

The headless renderer proof uses a synthetic `sample-flow` snapshot derived
from the interaction scenario. `app/scripts/smoke.mjs --fake` reports:

```text
smoke: expect -> true
smoke: expect -> true
smoke: expect -> true
smoke: conn=live {"ws":1,"asks":0,"steps":0,"offline":false}
smoke: pass
```

The assertions check visible failure/owner text, computed failure text and dot
colors against `--err`, and zero ask rows. The PNG was visually inspected: the
red overdue review names `manager-sample-flow`, says to review the existing
report and close/return `sample-flow.1`, and assigns escalation to Commander.
Local artifacts are under `.commander/failure-proof/` (renderer log/PNG and
cockpit before/after/down text). This is fake-state rendering evidence, not a
production recovery or a live PostgreSQL verification.

Full Go build, app build and TypeScript checks pass through a build slot.
The full Go suite was also run through a slot; its final status and any
out-of-scope environment/cutover failures are recorded on the task bead.
No install, restart, ship, merge or production change is part of this proof.
Independent review remains a separate prerequisite for source delivery.
