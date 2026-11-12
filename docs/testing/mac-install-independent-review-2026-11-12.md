# Independent Mac install candidate review — 2026-11-12

**Verdict: not yet fit for unconditional install; P2 validation findings V1 and V2 remain unresolved.** Migration, conflict resolutions and the Mac scenario passed their scoped checks.

Reviewer: operator-exec-vm-13-0a65de82ef16, assigned commander-il9.206.4.25. This reviewer did not prepare the candidate. The review authority is the assigned bead and retained il9.202.53 exception; it grants no release or installation authority.

Candidate reviewed: `a2df56745bb4211c381881a8e73c33a30d805b8b`, branch `work/exec-vm-8-396488386e63`, based on `89a45afc925ca028684cb8127cca155191077e31`. Review/test integration: `919099e9097642f68db2ac819f6cee896f8cf55b`, merging that exact candidate onto main `cb89c79c14ded4d8f465932c406131116bed4699` on `work/exec-vm-13-0a65de82ef16`. The merge had no conflicts. The initial six-file difference from the candidate contains only main's later status-description and PostgreSQL workstream-order fixes, including their tests. The first catch-up merge is `94d3584328663f0ab01dfd43b713f4bec9115b5e`, preserving then-current main `b977f16755aa0dda982ec58f63ec354824675365`. This second merge also had no conflicts. Unless explicitly called final-source references, file/line judgments below refer to the immutable candidate a2df5674 and first review integration 919099e9; newer main additions can shift line positions. Main catch-up adds user-only workstream completion, the Confirm done flow and classifier UTF-8 repair; it changes no migration SQL or schema attestation.

## Conflict-resolution judgments

| Resolution | Source evidence and judgment |
| --- | --- |
| CLAUDE.md | `CLAUDE.md:1636` adds Mac guidance; `CLAUDE.md:2311` appends cross-project/qualified migration guidance. Main's existing own-run, withdrawal, completion authority and ordinary-parent guidance remains present (`CLAUDE.md:2271`, `:2279`, `:2291`, `:2304`). PASS: appended guidance does not replace main's guards or confer installation authority. Some own-run/withdrawal prose repeats existing guidance; this is harmless duplication. |
| own-run implementation | `internal/store/own_run.go:27` retains read-only and statement limits, `:31` uses main's context-aware NewConnector/OpenDB path, `:40` refuses unavailable/wrong versions without initialization, `:44` adds catalog-lineage validation. Diff against main is exactly that added validation. PASS: main's connection cancellation behavior and no-fallback contract preserved. |
| own-run tests and scenario | `internal/store/own_run_test.go:92` retains deadline and explicit cancellation against a stalled handshake, with redacted errors and no legacy state; `:190` requires all four scenario cases. `tests/evals/scenarios/own-run-bootstrap.json:5` retains stalled-handshake/claim-fences/no-legacy/postgres-read-only. Both files are byte-identical to main. PASS. |
| withdrawal implementation | `cmd/cmdr/internal_gate_withdraw.go:182` retains active-vs-retained pitch modes and refusal on changed card/marker; `:214` checks all unanswered generations and exact latest decision; `:252` refuses answer journal history. Byte-identical to main. PASS: cancellation preserves the inactive retained-pitch fix and does not fabricate approval. |
| withdrawal tests | `cmd/cmdr/internal_gate_withdraw_test.go:340` tests retained inactive pitch, retry and no new card/approval; `:374` covers changed card/marker/generation/scope/answers/journal and `:426` interrupted retries. Byte-identical to main. PASS. |
| withdrawal documentation | `docs/cli/gate-withdrawal.md:25` describes inactive-card support and exact retained generation; `:45` states withdrawal never passes a gate. Byte-identical to main. PASS. |
| gate projection/result | `internal/gates/gates.go:94` gives withdrawal precedence over closed status using typed GateWithdrawn; `:220` refuses SetResult for a withdrawn gate. Byte-identical to main. PASS. |
| daemon apiOptions | `internal/daemon/daemon.go:977` preserves main's `FinishBoard: src.FinishBoard`, LiveData, LiveOwner and all existing options while adding Places and PlacesHome. `:508` binds place comments to the actual API server; `:957` publishes it atomically. PASS: board completion callback and Mac door wiring coexist. |
| cockpit assertion (original 76da31a1, rebased a2df5674) | `tests/evals/mac_execution_cockpit_test.go:84` and `:89` now expect “Commander to review the finished result”, matching unchanged main `internal/nextactor/nextactor.go:258`. The test still requires the actual Mac result title and rejects legacy overdue/live-provider text (`:92`). The scenario independently checks result run, report text, open bead, comment deduplication, foreign write refusal and exit token revocation (`tests/evals/mac_execution_test.go:193`). PASS: current display wording is asserted without dropping result integrity checks. |

## Migration and accepted deltas

PASS by source inspection, independent audit of saved rehearsal evidence, and private database reruns. `internal/store/postgres.go:132` takes one transaction and migration lock before validating the historical shape (`:157`), applying additive places 4 (`:187`) and qualified custody 5 (`:196`), then validating final shape before commit (`:211`). `:201` drains deferred constraints before qualified ALTER TABLE operations; the backfill is checked immediately. Failure rolls back. The migration has a separate two-minute budget (`:104`) instead of sharing the connection probe deadline.

`internal/store/postgres_0004_places.sql:6` adds places, runs, comment idempotency, active-run uniqueness and events. `internal/store/postgres_0005_qualified.sql:3` adds/backfills target_project_id, then changes the target FK while preserving local endpoints, forbids foreign parent-child edges, adds traversal indexing and serialized graph revision; `:36` requires nonnull endpoints. `internal/store/store.go:102` takes the same graph lock before callback locks, preserving ordering for canonical claim/result mutations. Main's receipts, publication, authority and automatic-gate checks remain inside `internal/beads/postgres_write.go:211` and `:619` custody transactions.

`internal/store/postgres_lineage.go:66` refuses historical incompatible places/custody numbering before migration; required catalog fingerprints and managed-table addition checks at `:86` and `:96` fail closed. `internal/store/shipped_migrations_test.go:12` freezes shipped SQL. The original rehearsal and reviewed candidate have byte-identical internal/store and internal/pgbind source (`git diff --exit-code 76da31a1 a2df567 -- internal/store internal/pgbind`). The later main workstream-order fix adds no migration change.

The preparer's live-copy rehearsal remains attributed to operator-exec-vm-8-396488386e63. It migrated a restored private PG16 copy from 3 to 5 in 4.201551027 seconds, reopened successfully and loaded a board with 3 projects/965 objectives. I inspected its fingerprint generator and comparison scripts and independently recomputed its before/after summary: 57 original tables, 677638 rows, 54 tables identical. The only differing tables are commander_schema, projects and state_events, exactly the saved planning disposition to question `47cd683a1b44fe6af29c0058c68fe598` recorded on .24. The original private dump and restored cluster were deleted by the preparer; this review did not redump production or independently repeat the live-copy rehearsal.

The differences have a direct source explanation: migration 5 line 4 updates 12283 dependency rows. Existing `internal/store/postgres_schema.sql:309` emits their events and increments project state_revision at `:317`, which triggers project events. Thus the accepted project revision deltas 642/7655/3986 sum to 12283 and match dependency counts; state_events grows 280814→305380 by 12283 dependency UPDATEs plus 12283 project UPDATEs. Retained normalized project checksum and original-event checksum match before/after; all dependency targets are local after backfill. Other original columns, claims, questions, holds, reports and progress remain protected. This is accepted metadata movement, not a claim of literal equality for those three tables.

## Findings and limits

No new source defect identified in the listed conflict resolutions or migration. Validation findings V1 and V2 below prevent an unconditional fit-for-install verdict. Historical Mac limitations/minors and prior reviewer attribution remain retained; this review does not waive or rewrite them. In particular, prior .206.4.17 findings belong to its original reviewer, fixes and independent recheck, and .206.4.23's clean integration verdict belongs to operator-exec-vm-22-128d9eab149b. Linux tests use a paired fake Mac and harmless simulated Operator. They do not qualify actual macOS/Electron architecture, packaged artifacts, matching deployed revisions, endpoint, account login, pairing or native process behavior.

Rollback to schema-3 binaries is impossible after migrating the database to schema 5: the old opener rejects a newer schema. Retain schema-5 writes and use a separately verified compatible binary or repair forward. Restoring a schema-3 backup after new writes requires explicit data reconciliation; changing the version marker or selecting retired state is not a rollback.

## Validation evidence

All Go test subprocesses clear COMMANDER_RUN_ID, COMMANDER_RUN_SESSION and COMMANDER_MANAGER_ROLE, plus TMUX; the reviewer/reporting process keeps its immutable launcher identity. Full builds and suites use `cmdr internal-slot run`. Eval backend br 0.7.6 was explicitly provisioned and hash-validated first. Database tests use the private PostgreSQL 17.11 server at localhost:55432, commander_trial, with disposable per-test schemas. No candidate Store/CLI was opened against live application state.

| Check | Result |
| --- | --- |
| Private store/place suites | PASS, `go test -p 2 -json -count=1 -timeout 12m ./internal/store ./internal/place`: store 192 top-level passes / 292 including subtests, 3 skips; place 17 top-level passes / 37 including subtests, zero skips. Store package 212.812s; place 21.950s. |
| Mac scenario | PASS, `go test -p 2 -json -count=1 -timeout 5m ./tests/evals -run '^TestPostgresMacExecutionScenario$'`: 1 test, zero skips/failures, 35.380s package time. |
| Fresh cockpit smoke | PASS, fresh `mktemp -d /tmp/mac-review-smoke.XXXXXX` parent, private socket subdirectory and throwaway HOME/COMMANDER_HOME, `scripts/cockpit-test.sh up`, `text`, `down`; captured “no operators running”, then teardown. |

The store skips are exactly TestRestoredProductionCutoverPreservesDurableWork (requires the separate restored-live-copy fixture), TestPostgresExecutionQueueAtScale and TestObservedExecutionQueryPlan (optional scale/diagnostic runs). The saved preparer live-copy evidence is audited above; no second production dump is claimed.

The initial store run recorded 291 passes including subtests and one tooling failure in TestPostgresCustomDumpRestoreShape: host pg_dump16 cannot dump a PG17 server. The complete clean store/place rerun above uses matching PG17 pg_dump/pg_restore through the existing private trial container, scoped to commander_trial. Both original and rerun logs remain retained; no source was changed to produce a pass.

The Mac scenario's actual detached capture contained:

```text
1  Mac execution result
   ◔ Commander to review the finished result
no operators running
```

The scenario also proved that a code-only VM slot cannot claim the Mac bead; the exact canonical run receives one attributed report; a foreign write/token is rejected; the physical exit revokes the token; and the result leaves its bead open for Commander review.


## V1 — P2: unresolved intermittent Codex graceful-interrupt assertion

At `internal/chat/codexbridge_stop_test.go:142`, the existing TestCodexBridgeHangupEndsCodexAndItsTool fails because `assertStopped` cannot find the fake provider's turn-interrupted marker (`:115`). The reviewed integration failed once in the full Go run, then once in a 30-run targeted repeat (29 pass / 1 fail). The exact pre-candidate main cb89c79c archive passed 30/30 and a further bounded 100/100 repeats. Required execution environment variables were cleared in every test subprocess. Both candidate failures reported the same missing marker; the process-stopped checks preceding it had completed. No process-leak assertion failed.

The chat, agentrt, testguard and pgbind trees are byte-identical between that main and the reviewed integration. This does not establish a new source defect or a causal link to Mac/migration changes, but it also does not establish a pre-existing baseline failure. Root cause and disposition remain unresolved. The first 30-run comparisons ran through a build slot. The final cached single-test baseline repeat used the repository's allowed targeted-test path after cancelling only its still-pending slot request; no running workload was interrupted.

Evidence: private full-go.log, candidate-chat-repeat.log, baseline-chat-repeat.log, baseline-chat-repeat100.log and the associated controller/result JSON. The repeat command was `go test -p 2 -json -count=30 -timeout 5m ./internal/chat -run '^TestCodexBridgeHangupEndsCodexAndItsTool$'`; the second baseline-only command changed count to 100. No test or implementation was changed to suppress this result.

Verdict: **NOT YET FIT for an unconditional Mac install card** for reviewed candidate a2df56745bb4211c381881a8e73c33a30d805b8b: the conflict resolutions, accepted migration deltas and isolated Mac path pass review, but V1 leaves the broader main-behavior regression validation unresolved; the final publication snapshot also retains the inherited UI-floor failures in V2. The planning Commander should retain this finding and obtain its technical resolution/disposition before treating the candidate as install-ready. This review passes no gate and authorizes no install, restart, merge or main update.


## Broad-run validation limitations

The broad `go test -p 1 -json -count=1 -timeout 12m ./...` invocation finished with exit 1: 3345 top-level passes, 209 skips and one recorded top-level failure (V1); including subtests, 5656 passes, 368 skips and one failure. Package outcomes were 62 pass, 2 fail and 8 skip/no-tests. The second failed package was tests/evals: its 12-minute package budget expired during TestSameTitleSpawnScenario, after the long Commander scenario harness and other completed tests. This is an incomplete broad eval run, not a newly identified source defect; no all-suite-green claim is made. Controller wall time was 1322.854 seconds. The separately required private Mac scenario passed and is rerun on the final main catch-up below. An audit after the broad run found no surviving test-guarded children associated with this worktree.

The app suite (`npm test -- --reporter=json`) reported 1053 passes / 1055 tests and two failing assertions, in test/jurisdiction-owner.test.tsx and test/smoke-page-errors.test.ts. The reporter counts nested describe groups as suites (335 passed / 3 failed of 338), so these are not 338 test files. Targeted candidate/main controls are reported below. `npm run typecheck` passed all three TypeScript projects; the initial `go build -p 2 ./...` passed. These outcomes remain distinct from the no-new-defect judgment for the migration/conflict resolutions.


## Final main catch-up and controls

The first catch-up tested source commit is 94d3584328663f0ab01dfd43b713f4bec9115b5e, on review branch work/exec-vm-13-0a65de82ef16 above main b977f16755aa0dda982ec58f63ec354824675365 as read then. A later main presentation commit is incorporated and checked below. The documentation-only pushed tip is recorded in the bead's Final report. No manual source conflict resolution or feature fix was made by this reviewer. Final `git diff --check` and current-main ancestry checks passed.

The conflict-free final catch-up retains both main and Mac behaviors: final `internal/daemon/daemon.go:978` still has FinishBoard plus Places/PlacesHome; its new closing sweep is retained. `internal/api/doorchecks.go` keeps the Mac run-on label checks and main's user-only closing/confirmation guards. `internal/store/classification.go` retains the Mac presence predicate and main's clipEvidence UTF-8/NUL repair. Livedata retains Places and the new ClosingGate projection. The original migration/lineage sources remain unchanged. The new main functionality was preserved and tested here, not independently recertified as a separate feature review.

All following Go checks ran in a build slot against that combined source with private localhost:55432 PostgreSQL and the required environment variables cleared:

| Final source check | Result |
| --- | --- |
| API, daemon, livedata, nextactor, statusclassify, supervision packages | PASS: 783 top-level / 1303 including subtests; 4 skips; zero failures. 77.084s controller time. |
| CLI close/create/withdraw/resolve/completion selection | PASS: 36 top-level / 127 including subtests; zero skips/failures. 67.027s. |
| Store UTF-8/classification/claim/replacement selection | PASS: 5 top-level / 7 including subtests; zero skips/failures. 9.587s. |
| Final Mac scenario and populated detached cockpit | PASS: 1 test, zero skips/failures. 33.158s controller time. Uses a fresh mktemp socket root and captures the current Mac result review action. |
| Final go build -p 2 ./... | PASS. 7.599s. |

The final backend skips are TestOverviewSpikeCompare, TestReviewBlock_GenerateFixture, TestWorkflowReview_GenerateFoldedBlockFixture and TestBoot_SettlesARestartingJobAndPersistsIt. Their skipped status is retained rather than counted as passes. Exact commands and JSON output are in final-checks.py, final-checks.json and corresponding logs.

UI controls each ran the same two failed files on the reviewed candidate tree and an exact archived cb89c79c main tree. Each reported 17 passes / 1 failure of 18 tests: the missing Responsible Commander label at `app/test/jurisdiction-owner.test.tsx:24` reproduces on main. The smoke late-page-error test at `app/test/smoke-page-errors.test.ts:116` passed on both repeats after its full-run failure. Renderer source, UI tests and smoke scripts are byte-identical between main and the reviewed tree (`git diff --exit-code cb89c79c 94d358 -- app/src/renderer app/test app/scripts`). Thus the owner-label assertion is a demonstrated baseline failure, while the initial late-error failure remains an intermittent validation observation in unchanged code. Neither is hidden or represented as a green renderer suite. These controls do not resolve V1.

## Artifacts, cleanup and disposition

Published artifact: docs/testing/mac-install-independent-review-2026-11-12.md on the review branch. Private evidence is under .commander/independent-mac-review in the assigned worktree: checks.json, final-checks.json, test-counts.json, rehearsal-audit.json, renderer-results.json, UI/control results, controller scripts and complete test logs. The exact baseline source archive is baseline/. The saved original rehearsal remains under preparer worktree .mac-rehearsal and retains its original attribution; this reviewer recorded independent fingerprint-summary hashes in rehearsal-audit.json.

Only test dependencies were provisioned in this private worktree. No Commander/app installation, resident restart, main-branch update, live migration/feature-state write, real model/Mac launch, gate passage or bead closure occurred. Canonical own-bead phase/comments were the only live planning writes. The review branch is pushed for planning review; it is not merged to main. Private run DSN material is removed after validation. Detached cockpit teardown completed, and the final process audit is recorded with the private evidence. No private PostgreSQL server was started: the pre-existing private trial server was used through disposable test schemas and remains untouched outside those fixtures.

Review scope is complete with a negative release-readiness verdict. Still incomplete: technical resolution/disposition of V1, a fully green broad Go/eval and renderer floor, and the separately authorized actual Mac/artifact/recovery checks. Binary rollback to schema 3 remains incompatible after migration to schema 5. The planning Commander owns next work and any install-card decision; this report changes no shared plan or approval state.


## Publication snapshot and V2 — P2: renderer validation floor remains red

During publication main advanced again. The branch now incorporates main 3f374267a614d7dff1c3c6d909630ceae1138e88 through merge 9560c9949ae4d8daeef62e6b25194837c455f95e, then the UI-only main snapshot 2049efb0b164fdb4e76db64186c5e801892b1e8c through publication source commit e8d9bee2abbd1015864d6bb08573ea1bb1e84469. Both merges were conflict-free. The source branch was pushed atop that then-current main snapshot. All Go files at e8d9bee2 are byte-identical to the tested 9560c994 tree (`git diff --exit-code 9560c994 e8d9bee2 -- '*.go'`). The final documentation-only tip is recorded in the canonical Final report.

The 9560c994 checks passed daemon/livedata/nextactor/supervision: 604 top-level / 1046 including subtests, 2 fixture-generator skips, zero failures (49.243s). Focused UI at that tree was 145 pass / 1 inherited owner-label failure of 146 tests. Typecheck passed (41.421s). The final private PostgreSQL Mac scenario, including a fresh populated detached cockpit capture, passed again: 1 test, zero skips/failures (23.391s controller time). The final full Go build passed (10.030s). These Go results also cover the byte-identical publication Go tree.

The focused publication UI run on e8d9bee2 reported 183 pass / 3 fail of 186 tests across 23 files (26.497s). Failures:

- `app/test/jurisdiction-owner.test.tsx:24`: missing Responsible Commander: commander label, already reproduced on archived main cb89c79c.
- `app/test/overview.test.tsx:149`: expects Earlier recap; main's newer summary presentation uses Refreshing summary.
- `app/test/overview.test.tsx:209`: expects Writing the recap…; main's newer streaming-summary presentation no longer exposes that text.

The publication renderer, test and smoke source trees are byte-identical to main 2049efb0 (`git diff --exit-code 2049efb0 e8d9bee2 -- app/src/renderer app/test app/scripts`). The two Overview failures therefore accompany the retained main presentation change; they are not attributed to the Mac patch. They still leave the requested renderer validation floor red. V2 is a validation finding on the integrated publication snapshot, separate from the original candidate's source judgments. No main UI code or assertion was rewritten by this reviewer. Final typecheck and cleanup results are recorded below.

Final publication `npm run typecheck`: PASS, 35.772s. The private run DSN file is absent. Final scoped process audit found no surviving test-guarded, Node or browser test children. No install, resident restart or main update was performed.
