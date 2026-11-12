# Withdraw an obsolete approval ask

`cmdr internal-gate withdraw` cancels an ask without approving its action.
The owning Manager, Commander or user supplies an explicit reason and a citation
to the durable cancellation instruction. Operators must ask their Manager.
The citation records authority within the existing local role/hook trust boundary;
it does not authenticate external instruction text or grant production authority.

First inspect the exact gate and its retained pitch generation, then preview:

```sh
cmdr internal-gate withdraw --project example --bead sample-flow.3 \
  --expected-state open --generation 1 \
  --reason 'The requested experiment is cancelled' \
  --instruction 'planning-note:sample-17 cancellation by owning Manager' --dry-run
```

Repeat the same arguments without `--dry-run`, adding
`--expected-snapshot <expected_snapshot from preview>`. The hash binds the gate's
identity, scope, labels, dependencies, original card presence, needs-user marker, active-card or retained-pitch mode,
exact retained pitch, pitch history and answer
journal. A changed snapshot refuses. Retain the exact command for retries.

Only an explicit `kind:gate`, or legacy `gate` plus `approver:user`, qualifies.
The gate must be open with a retained, unanswered decision and expected generation.
An inactive card is also supported when both the card and needs-user marker are
absent and the exact latest unanswered generation retains a valid decision and
nonempty title. Its original pitch backs the audit directly; no live ask is
synthesized or restored. Presence/marker mismatches refuse, and retries retain
the original mode and card or pitch evidence.
Any answer history, approval evidence, hold, proposal, execution assignee, execution
wait/owner or running/exited run refuses. Every nonclosed incoming waiter or child
refuses, including parked and superseded beads; the error names the actual bead.
Dispose of obsolete work through the existing supported controls first. Withdrawal
never unlinks a waiter, replaces a card, or edits another bead.

The command records `gate-withdrawal@v1` intent before mutation, adds `withdrawn`,
defers the gate and removes its active `needs-user` marker/card. The old pitch,
unanswered generation, original description/criteria, links and answer journal stay
intact. Watchers stop seeing an active ask on the next snapshot; their existing
normal pass forgets its obsolete notice. No watcher files or global config are
edited. A completion audit follows cleanup. Exact retries recover interrupted
writes, including lost responses; different requests refuse.

Withdrawn gates never count as passed, workflow approval or production approval,
even if someone subsequently closes the bead. The marker is reserved to this verb;
answer and gate-result doors refuse it. Reusing an old cancelled approval requires
a fresh ask rather than treating its retained history as consent.

Validation and writes are separate backend operations, with fresh checks between
writes and the existing local answer lock. They are not a backend compare-and-swap
transaction across hosts or arbitrary concurrent graph/card editors. A concurrent
change can leave an audited partial withdrawal; stop and inspect a refusal, then
retry only the original request when its evidence is restored. This is the same
local cooperative trust boundary as the other repair commands.

Tests: `TestGateWithdraw*`, `TestGateWithdrawalScenario`, and
`tests/evals/scenarios/gate-withdrawal.json` (runs the real private CLI handler in
`cmd/cmdr`, with synthetic state and no agents).
