# Reviewed repair rebuild on the PostgreSQL cutover

Assigned execution: commander-il9.204.6.129, plan revision 8. Candidate branch:
`work/exec-vm-11-2e8e8faff743`. Final-pass main base (read Nov 11 at 19:32 UTC):
`64d90ffc71c47f50eb6c0ddaf46faa951bc088e3`, which contains the installed cutover `21756bbe`.

This is source preparation only. It grants no release or approval authority.
No installation, daemon restart, push, merge, live database mutation or migration
was performed. PostgreSQL schema 3 and all shipped migration files are unchanged.
The historical schema-one assembly document remains historical provenance;
its old install/rollback instructions do not authorize this candidate.

## Included source and equivalence

| Change | Reviewed head | Replayed candidate range | Evidence |
| --- | --- | --- | --- |
| Repair batch: capacity recovery, red failure ownership, missing-custody projection, scoped own-run review bootstrap, inspector focus, withdrawal, private fixtures/backend CI | `7aa45369f9f5f258dc4ff86c24424b62349f3b75` | `64d90ffc..96a46703` | `batch.range-diff`; 96/125 touched files have identical final blobs; differing shared files retain newer main or the additional listed fixes |
| Fixture heredoc install guard plus ANSI-C escape bypass correction | `03f37c36ea9f2e120ab097fc1997321edad837f3` | `96a46703..5709ce0b` | `guard.range-diff`: both commits exact; no Mac transplant |
| User-gate authority, unfinished-parent close refusal, pre-migration authorization, Manager guidance | `5bd34a9b197f5a205222b903642c9967ec01afbd` | `5709ce0b..46ac2a21` | `gates.range-diff`; tests/scenario/close helper exact; existing no-PR completion path preserved |
| Ordinary parent placement and transitive descendant custody correction | `e83792239d98250cbac1a9e1b94821f2caf6addf` | `46ac2a21..40f61946` | `reparent.range-diff`: production/tests exact; only CLAUDE append context differs |
| Labelled Commander interrupt, retained SIGINT explanation, post-ACK transport-loss correction | `10801ba4d30f34792d60db217e8fd1bc7ca749e9` | `40f61946..01e595f6` | `sigint.range-diff`; transport regression/scenario exact; native main safeguards retained |

All evidence paths above are under `.commander/rebuild129/evidence/` in the
assigned worktree. `files.json` lists reviewed/main/candidate Git blob IDs for
every touched source path; each differing file has a retained reviewed-to-candidate
diff under its group directory. `commits.txt` lists every replayed commit. These
are comparisons and integration evidence, not a renewed independent CLEAN verdict.

The old batch's source inventory remains in
`docs/testing/schema-one-repair-assembly.md` (original component reviewed heads:
capacity 39a2b529, sidebar 12200ec2, review helper 3e064c92, inspector 68ebee93,
baseline c37ae931, withdrawal e442a2f4, mail c7c59aa8).
The accepted validation report .204.6.84 attributes source reviews to original
FULL .55 plus delta .59 and fixture FULL .65 plus .80. Those original reviews,
findings, receipts and coverage limits are retained; this execution does not
rewrite them. Guard .82's original R1 and reparent .11's original descendant
custody major remain attributed to their original reviewers; accepted rechecks
on .82/.14/.2.3 are the planning record. SIGINT .41.12's original transport-loss
major and historical partial receipts remain intact. Independent reviewer
operator-exec-vm-18-bb11a0841f3c on .202.41.13.2 found that major resolved at
10801ba4, with no new findings. Its retained CLI/live coverage minor remains;
a detached startup smoke is not broad feature-specific live proof.

## Dropped or superseded content

- The old schema-one base and cutover rollback assumptions were not transplanted.
  The candidate descends from 21756bbe; shipped migrations were not edited.
- `ab0b747c` (Mac places/custody transplant, including 0023_places.sql) is excluded.
  The guard's two reviewed commits are replayed directly without that ancestor.
- In-progress Mac/custody .206.4.15 and read-only guard .204.6.89 are excluded.
- `79acd11a` is empty on replay: its fourth return value is already included in
  the newer-main signature adaptation in 0ebb52e2. No SIGINT behavior was dropped.
- The old six-argument gatePass signature and duplicate unqualified workflow-close
  call are superseded by main's noPR-aware signature, checkWorkflowCloseWaiving
  and noPRCompletionContext; reviewed authorization still runs before migration.
- The legacy Manager-capacity overlay is superseded in active mode by canonical
  execution/planning projection. `capacityLine` now leaves active lines unchanged;
  the failing-before/passing-after `TestActiveBoardIgnoresRetiredManagerCapacity`
  also participates in commander-planning-retirement.json. Legacy capacity
  behavior remains covered.
- Duplicate legacy TestRestoreExactBackend HOME initialization and the old
  spawn_missing_step token/dead-port setup are superseded by main isolation plus
  the reviewed private mail-server helper. No assertion was weakened.
- Old account-planner calls without saved ordering are superseded by main's
  accountOrder lookup and refusal on failed/empty preferences. Capacity recording
  is appended to the ordered plan result; main ordering tests remain.

## Conflict resolutions preserving current main

- Combined both sets of account-plan and spawn usage tests, retained main's
  ordered provider planning, and added reviewed capacity evidence.
- Retained sidebar ownership CSS/Planner fields while adding failure coloring.
- Preserved activeWorkstreamLine, canonical execution counts, recorded questions,
  saved answers, required checks and queue priority. Legacy recovery projection
  remains behind main's active-mode branch.
- Kept plan-revision checks and PostgreSQL pitch/answer handling in resolve-needs;
  withdrawn-gate refusal is checked before those paths.
- Preserved both `native-report` and `[commander interrupt]` fake-Claude cases,
  as explicitly directed by revision 7.
- Added the fourth `noted` result to every newer-main start error return. Preserved
  current-run host identity/admission checks, flushNativeExits, run-aware event
  publishing and native exit reconciliation. Automatic interrupt/crash restart
  remains restricted to ExecutionRunID == 0, retaining canonical native custody.

Revision 8 required current main at the start of the final pass. The candidate
was rebased onto 64d90ffc with all 40 replay patches unchanged:
`main-update.range-diff` contains only exact (`=`) pairs. This retains the
PostgreSQL watcher-flags correction as well as the 21756bbe cutover. SIGINT was
still absent from that main head; no duplicate landing was imported.

## Verification

Build/full-suite commands are `go build ./...` and
`go test -json -count=1 -p 2 -timeout 20m ./...`, run inside
`cmdr internal-slot run` with a private HOME/state/cache and pinned private Rust
beads backend. `.commander/rebuild129/final-{build,suite}.log` and the suite
summary retain outcomes and every explicit skip. The assigned bead's Final report
records the completed checks and exact deliverable SHA. App source passed typecheck,
105 test files / 1,028 tests, and build. The earlier 21756bbe-base suite was
intentionally stopped in its exact private eval scope after revision 8 required
a new base and an independent integration check found the capacity overlay
regression; its partial result is not a full-suite pass.

## Rollback boundary

Nothing has been installed; abandoning this branch leaves the running system
unchanged. A later authorized install must pin the exact final candidate head,
preserve a compatible paired CLI/daemon and matching renderer, and retain all
current PostgreSQL data. Never roll back to the historical 7aa/77 schema-one
binary/state bundle or retired SQLite/Dolt files. After withdrawal writes,
reconcile withdrawn labels, asks, intents and dependencies under explicit
planning authority before using a binary without those semantics; repair forward
when compatibility is uncertain. No automatic state rollback is provided.
