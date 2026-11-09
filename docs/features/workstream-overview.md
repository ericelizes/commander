# Workstream Overview

Selecting a workstream or track opens **Overview**. **Workflow** remains the
second tab. Selecting an actual decision still opens its existing pitch and
answer flow. Snapshot updates never switch the tab the user selected.

Overview separates the live board from a narrative reader:

- The outcome, current status, decisions and track navigation use the current
  snapshot. A stalled agent is a recovery problem, not an invented user gate.
- `POST /api/overview` accepts a known project/workstream and an optional actual
  descendant track. It returns a cached recap plus the exact cited evidence.
- The reader gets a bounded set of scoped step records, current board status,
  and recent Manager text. Manager reports are labelled as reports, not proof.
  Tool payloads and credentials are never read into the evidence bundle.
- The default reader uses the main Commander's existing Claude account. It
  runs in an empty temporary directory, in safe mode, without tools, MCP,
  inherited role, or session persistence. The prompt lives in
  `internal/overview/recap.go`. It cannot call Commander mutation routes.
- A recap explains where the outcome stands, meaningful changes and recorded
  next actions. Each claim must cite an input evidence ID. Unknown sources,
  malformed output and excessive claims are rejected. Citations are necessary
  provenance, not a guarantee that the model's interpretation is correct.
- Only the focused Overview requests a recap. One reader runs globally at a
  time, with a 90-second deadline and bounded output. Identical evidence is
  reused; each scope has a two-minute retry/change cooldown. This never blocks
  snapshot loading. The cache is bounded to 128 scopes and lasts for the daemon
  lifetime. An old recap keeps its original sources and is visibly stale when
  current evidence differs. Live state remains available on reader failure.

**Named things are explained.** The prompt requires the first mention of any PR, ticket,
step, person, system, number or limit to say what it is and why it matters, or to say the
records do not. Step records carry PR numbers/repo, tickets, acceptance criteria, parent
title, `outcome` (a closed step's close reason) and `waits_for`: the titles of open blockers and
open approval gates (never bare bead ids, never closed/parked/superseded ones), plus a count of
blockers outside the workstream. **Finished task outcomes:** the snapshot carries a closed bead's
close reason (`close_reason`, capped at 400 characters, from the list read already made: no new
read), and the reader gets up to 12 finished Operator tasks with one (under live steps first, then
the latest), since a task's close reason is often the only record of what its step's work found
(e.g. a regeneration that ran and changed nothing). Closing a step folds its tasks off the board, so
the closed step carries `finished_tasks` (`attachFoldedOutcomes`, same list read: the 2 latest by
`updated_at` with a close reason, closed in the last 7 days (about 5% of a real snapshot), on the nearest drawn ancestor) and each row `done_blockers` (blockers
finished and not drawn), so a folded finished blocker is never counted as an outside wait. Tests
`TestOverviewReaderSeesFinishedTaskOutcomes`, `TestOverviewReaderSeesOutcomesFoldedUnderAClosedStep`,
`TestCloseReasonRidesOnClosedRowsOnly`, `TestFoldedTaskOutcomesRideOnTheirClosedStep`. Not fed: bead descriptions (not in the snapshot).
Read-only comparison harness: `TestOverviewSpikeCompare` (skipped without `OVERVIEW_SPIKE_*` env;
output goes to a temp dir unless `OVERVIEW_SPIKE_OUT` names an absolute path outside the repo; one
inside the checkout, links resolved, is refused: `TestOverviewSpikeRefusesAnOutputInsideTheRepo`).

## Manager result handoff

The stored `Manager to close` wait remains a compatibility/custody marker.
User-facing status says **Result ready for Manager review**, or **Manager review
overdue** after its deadline. The diagram assigns this handoff to the Manager.
A task owned by a Manager is not a live Operator merely because its stored
status is `in_progress`. Actual resumed Operator work outranks the old receipt.
The shared `resultHandoff` derivation is used for workflow and diagram status;
no task is closed or receipt rewritten to change its appearance.

The discriminating regression is a Manager-owned internal task with an
`in_progress` storage status, a `review` state and an outstanding result marker.
It must show zero working Operators and the same Manager handoff in the board,
workflow and diagram. A second case adds a real resumed Operator and must show
Operator work instead. `result_handoff_test.go` covers both and overdue review.

## Approval dispatch custody

Answering an approval records the owning Manager and a `Manager dispatch`
receipt on each newly released, otherwise unheld step **before** closing the
gate. Actual dependency and parent edges determine the steps and Managers;
another open blocker, existing Operator, recorded wait, or user decision is
preserved. If dependency reads or custody writes fail, the answer stays saved
and the gate stays open for retry. Queued work remains `open`, never fake
Operator progress.

The board, workflow and diagram show **Approved; Manager dispatch pending**.
After two minutes they show **Approved work not started; Manager recovery
needed**, with the Manager as the actor. This is an internal recovery issue,
not another user approval. Ordinary Manager-only state writes cannot erase
this receipt; an actual Operator assignment in progress, explicit next wait,
user decision or closure supersedes it. A successfully persisted task-bound
Operator run also accepts dispatch on its actual ancestors automatically; a
failed spawn leaves the pending receipt intact.

Both dispatch and result-review handoffs retain their custody record when the
reminder deadline fires. The watcher queues recovery mail and records a
notification receipt that survives daemon restarts. Mail failure retains the
handoff for retry. Sending mail does not prove that the work started; the
record stays until a real successor state is written. Existing mail wakeup
validates the Manager's current workstream and resumes its conversation;
held sessions and invalid routes remain explicit delivery failures.

`TestApprovalRetainsCustodyWithStoppedManagerAndMailFailure` reproduces the
closed-approval/unowned-successor incident through the answer command and
snapshot loader. `TestManagerHandoffReminderRetainsCustodyAcrossRestart`
checks failed mail, expiry and restart. The `approval-handoff.json` eval checks
that the board and diagram agree, show no working Operator, and never invent
another user decision.

## Pending gate publication

A user approval gate with `kind:gate` has the same proposal semantics as the
legacy `gate` label. Adding its decision card does not create a second `proposed:ask` after
Commander has accepted the gate. A still-proposed gate retains its original
proposal and still requires Commander acceptance. Questions added to automatic
gates still require a new Commander review.

Valid proposed approval gates stay in the internal graph. Their `Proposed`
marker routes the ask to Commander through `NoPitch`, keeping it out of the
user's actionable queue. Hiding the gate itself loses its blocking edge and
falsely marks the dependent step as unowned. The shared board instead shows
Commander reviewing the proposal, or an overdue Commander action. Held,
parentless, invalid or cyclic cards retain their existing publication guards.
Acceptance publishes the saved decision; it never closes the user gate or
grants its permission.

`TestGateCardDoesNotCreateASecondProposal` covers both gate representations and
pending/accepted gates. `TestProposedGatePublicationScenario` loads a saved
card through the real store, parent edges and dependencies, then verifies
pending, overdue and accepted states without granting approval.

## Manager mail wake routing

Mail wake-up resolves a registered Manager directly from its project/workstream
row, reads that root fresh, and revalidates actual ancestry and objective kind
before starting the same conversation. It does not refresh the entire fleet
merely because a stale recipient is absent from the display. An unregistered
role can use an explicitly supplied cached workstream listing, still with the
same fresh validation; otherwise its unread mail stays pending for routing
repair. Explicit user navigation retains the fresh-discovery fallback for a
new workstream that is not yet in the snapshot.

This separation matters because the mail poll is serial: a stale recipient's
uncached fleet scan delayed completed results for unrelated idle Managers.
The regression puts an obsolete role before a valid registered Manager in the
real Waker poll, makes the forbidden global scan wait until cancellation, and
asserts that the valid Manager starts without that scan. No terminal session
is automatically taken over and no invalid role is silently accepted.

## Verification

`app/test/overview.test.tsx` exercises default navigation, evidence inspection,
existing decision actions, track isolation, stale recaps and reader failure.
`tests/evals/scenarios/workstream-overview.json` and its eval exercise the
API-to-reader boundary, reject invented evidence and assert no command mutation.
The real reader should also be checked on live workstream evidence: its output
must preserve incomplete work and distinguish implementation from deployment.
