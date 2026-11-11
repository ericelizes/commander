# Exhausted capacity after the shared-scheduler cutover

Scope: `commander-il9.202.48`, d82/d83, and the saved planning answer to
`exhausted-capacity-saved-order-policy` on November 11, 2026. Account lists are
ordered preferences, with other connected accounts available as fallback.
Explicit runtime pins and user pauses remain constraints. This is a source
change for separate review, with no installation or production restart.

## What the cutover already supplied

| Behavior | Existing evidence |
| --- | --- |
| One shared execution pool, independent of permanent Managers | `internal/execution/worker.go`, `ClaimNextExecution` |
| Fresh provider selection and fallback among saved accounts | `firstAgentAccount`, `AccountAvailable` |
| Logical Commander account switching | `TestCommanderFallsBackAcrossProviders` |
| Explicit exhausted-provider refusal before ending old work | `TestRequestedUsage`, `TestRefusedRestartWritesNothing` |
| Approval, question, hold and capacity checks before dispatch | `commander_execution_allowed`, `insertClaimedRun` |
| Operator mail addressed to Commander reads the PG planning owner | `spawn.ManagerRole` / `executionOwner` |

The old saved-list regression treated omitted accounts as forbidden. The saved
answer supersedes that policy; `TestSavedAccountOrderFallsBackToAnyConnectedProvider`
replaces it. Runtime pins constrain provider fallback, including Commander on
Claude. Account preferences do not create another kind of account.

## Gaps repaired

- Native usage exhaustion previously skipped legacy switching and became a
  generic result-review obligation. The host now retains the usage event in its
  physical-exit receipt. The store atomically writes a `native-usage-limit` fact
  using existing comment columns and requeues only the current admitted plan.
  Terminal status remains `native-error`, preserving existing checkpoint logic.
- The shared worker retries through canonical admission, retaining the source
  branch and workspace. Provider recovery uses a fresh session and an attributed
  bounded handoff. It never borrows a foreign provider's native conversation ID.
  Unanswered questions still block dispatch; saved answers are read from their
  exact source-run history. Attributed final reports retain review precedence.
- Usage-only launch refusals retain their cause without consuming the five
  startup-failure attempts. Available accounts can therefore recover work later.
- Unknown reset time remains unknown. A delayed exit cannot overwrite newer
  usage evidence. The actual scheduler backoff is not represented as a reset.
- Shared exhaustion appears on all eligible queued rows, and pinned-planner
  exhaustion on rows awaiting that planner. Questions, approvals and pauses keep
  priority. Expanded terminal rows use the canonical cause instead of stale
  Manager holders. Nested planning scopes use their actual Commander.
- Explicit ancestral Manager destinations still entered legacy routing after
  cutover. Active execution mail now resolves the current planning owner using
  fresh actual ancestry, without copying or starting retired Managers. Ownership
  read failure refuses delivery. Other explicitly addressed conversations retain
  their destinations.

## Verification

`tests/evals/scenarios/exhausted-capacity.json` names the interaction runners.

| Boundary | Test |
| --- | --- |
| Actual worker/spawner/PG/native-host recovery, code and knowledge, another Claude account and Codex, all-out refusal, retained artifact, pause and pin | `TestExhaustedCapacityScenario` |
| Repeated exhaustion cannot spend launch attempts | `TestUsageCapacityDoesNotSpendLaunchAttempts` |
| Exact session, physical exit, original workspace, duplicate receipt | `TestPostgresUsageRecoveryFencesSessionWorkspaceAndTerminalEvidence` |
| Pending and answered checkpoint ordering | `TestPostgresUsageRecoveryWaitsForSavedAnswerBeforeProviderHandoff`, `TestNativeCheckpointAnswerAndExitResumeThroughCanonicalCapacity` |
| Unknown deadline and delayed evidence | `TestUnknownUsageResetRemainsUnknownAndAvailableAccountsClearCause`, `TestDelayedUsageReceiptCannotOverwriteNewerCapacityEvidence` |
| Shared and pinned-planning rows, nested jurisdiction | `TestUsageCapacityAppearsOnEveryAffectedRow`, `TestPinnedPlannerUsageAppearsOnEveryDependentRow`, `TestNestedPlannerUsageUsesItsActualJurisdiction` |
| Terminal row cause with no stale holder | `TestCanonicalNestedUsageShowsCauseInsteadOfLegacyHolder` |
| Retired Manager mail alias | `TestActiveExecutionMailRetiredManagerFollowsPlanningOwner` |
| Real detached populated cockpit text | `TestExhaustedCapacityInIsolatedCockpit` |

PG tests accept only `COMMANDER_POSTGRES_TEST_DSN` pointing to the disposable
`commander_trial` database; they use private schemas. Model binaries are private
fakes. The cockpit runner drives `scripts/cockpit-test.sh up/text/down` on its own
absolute socket, expands the sidebar on that private server, and inspects root
and nested capacity errors. It does not change the user's visible workspace.

Full builds and suites run through `cmdr internal-slot run`. Test children must
not inherit an executor's `COMMANDER_RUN_ID` or `COMMANDER_RUN_SESSION`; clearing
those in the test child does not change the executing agent's identity. The
legacy `br` integration requires beads_rust flag compatibility, rather than a
`br` wrapper that redirects its flags to `bd`.

## Limits

Usage is provider-report driven; missing reports are unknown, and an unknown
reset is not guessed. Recovery retains up to 32 KiB of matching source-run/session
events from the last 256 KiB of the role log, plus its exit report. Agents must
inspect the retained workspace, current scope and saved answers. This establishes
an attributed handoff with fake providers, not full conversation translation or
production subscription behavior. Existing pre-change native hosts gain durable
usage receipts only on their next normal launch; this change does not restart
them. Build/suite outcomes and the exact review SHA are recorded on the bead.
