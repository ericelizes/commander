# Mac candidate merge resolution, November 12, 2026

Assigned execution: commander-il9.206.4.29. Source merge: `49322711e1d337c47bdbb7f5ef8c954c9999358b`, parents candidate `cab459311bc81c593d447881aab9e80b52ea17cb` and main `f8001daf9ab077e3e46a24460b970a9cc8c87a79`. Branch: `work/mac-main-resolution-206-4-29`. This resolves source conflicts; it grants no review, release or installation approval. Earlier independent review findings and attribution remain in their original documents.

## Resolution

`internal/store/supervision_results.go` retains the candidate's `closeSupervisedRunTx` extraction, used by atomic Mac place completion. It moves main's entire transaction body into that helper, including usage-limited normalization to native-error, attributed native-usage-limit facts, current identity/physical exit checks, checkpoint ordering, final-report precedence, exact admitted-plan recovery requeue and supervisor result handling. Normalization belongs inside the helper so both native and Mac transactional callers share it. Ignoring indentation, the diff from main is just the helper extraction.

`CLAUDE.md` retains both append sections: the candidate's cross-project custody, qualified PostgreSQL lifecycle, Mac composition and migration-shape guidance, and main's exhausted-capacity behavior and verification guidance. Nonconflicting main updates, including runtime-pin/account policy, load freshness and eval deadline guidance, remain intact.

## Verification

All checks used machine build slots. Go test children cleared COMMANDER_RUN_ID, COMMANDER_RUN_SESSION, COMMANDER_MANAGER_ROLE and TMUX. PostgreSQL tests used only the existing private localhost:55432 commander_trial database and disposable test schemas; model processes were fakes.

| Check | Outcome |
| --- | --- |
| `go build -p 2 ./...` | PASS |
| Full `internal/store` | 196 top-level passes, 3 skips; 303 passes including subtests; zero failures |
| API, daemon, livedata, nextactor, statusclassify, supervision | 802 top-level passes, 4 skips; 1346 passes including subtests; zero failures |
| Chat, execution, spawn, CLI, TUI usage/capacity/checkpoint selection | 34 top-level passes, zero skips; 64 passes including subtests; zero failures |
| Mac execution and populated capacity cockpit scenarios | 2 passes, zero skips/failures, with the temporary overlay described below |
| Full app tests | 1043 passes / 1057 tests; 14 failures, all reproduced on exact main |
| App typecheck | FAIL on stale Overview summary fixture, also reproduced on exact main |

Passing main tests include TestPostgresSupervisionInactiveGates, TestPostgresSupervisionResultTransferAndFencing, TestPostgresSupervisionRejectsAcknowledgementAfterProgress, TestPostgresUsageRecoveryFencesSessionWorkspaceAndTerminalEvidence, TestPostgresUsageRecoveryWaitsForSavedAnswerBeforeProviderHandoff, TestUsageCapacityDoesNotSpendLaunchAttempts, TestNativeCheckpointAnswerAndExitResumeThroughCanonicalCapacity, TestDelayedUsageReceiptCannotOverwriteNewerCapacityEvidence and TestExhaustedCapacityScenario (code and knowledge, account/provider recovery).

The initial store run had 195 passes, three skips and one tooling failure: host pg_dump 16 cannot dump private server 17. The complete rerun passed using the trial container's matching pg_dump/pg_restore clients through a private wrapper restricted to commander_trial on localhost:55432.

Unmodified eval compilation fails at tests/evals/overview_test.go:37: overview.Recap no longer has Summary. An exact f8001daf source archive reproduces the same compiler failure. A private Go overlay adds an unsatisfied build constraint only to that unrelated test file; no committed source changes. With it, the unchanged TestPostgresMacExecutionScenario and TestExhaustedCapacityInIsolatedCockpit both pass. These run the actual detached cockpit up/text/down flow on private sockets, including populated Mac result and nested capacity rows. This is qualified scenario evidence, not a green unmodified eval package.

Renderer/test/script trees are byte-identical to main. Exact-main controls reproduce all 14 failed assertions: complete-button (3), jurisdiction-owner (1), overview (10). The owner/overview control reports 30 passes and 11 failures; complete-button reports zero passes and three failures. Typecheck reports TS2353 at test/overview.test.tsx:22: summary is absent from Pitch; exact-main control reports the same error. These baseline failures remain unresolved and are not represented as green checks.

Store skips: restored-production rehearsal, explicit 1000-assignment release gate and query-plan diagnostic. Core skips: Overview spike comparison, two fixture generators and boot restart fixture. No full Go suite was requested or claimed by this execution.

## Evidence and limits

Private complete commands, timings, JSON test events, renderer results, exact-main archive, overlay and controls are in `.commander/merge-evidence` under `.worktrees.noindex/mac-main-resolution-206-4-29`. Key files: checks.py, usage-checks.py, store-repeat.py, controls.py, extra-control.py, main-typecheck.py, results.json, counts.json, store-repeat-results.json, usage-results.json, controls-results.json, renderer.json, main-renderer.json, main-complete.json and the corresponding logs.

No Commander/app installation, main update, live schema migration, backup, resident daemon restart, gate passage, bead closure or actual Mac/model launch occurred. The existing private PostgreSQL server was not started or stopped. Scenario teardown completed; test children were audited at finish. The pushed documentation tip is recorded in the bead's final report.
