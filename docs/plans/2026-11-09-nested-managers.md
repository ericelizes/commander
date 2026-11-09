# Nested Managers: routing and migration (design)

Task: commander-il9.203.1.1 (design only, no code). Decision d70 (2026-11-09): every
workstream has a Manager; a nested workstream has its own nested Manager. Operators
under a nested workstream report to it; the parent Manager coordinates its nested
Managers instead of running their Operators.

## 1. The rule

**Owner(bead)** = the nearest bead in `beads.Lineage(bead)` (the bead itself first)
that is an open `kind:workstream` bead; else the top of the chain (today's root).

- A bead labelled only `kind:track` (an unmigrated project) is NOT an owner: it stays
  with its workstream, exactly as today. Only `beads.WorkstreamLabel` counts, so a
  project gets nested Managers only after `internal-migrate-workstreams --apply`.
- Closed, deferred (parked) and superseded workstream beads are skipped: their work
  goes up to the next open owner. A parked nested workstream's Manager is not woken.
- Role name is unchanged in shape: `manager-<owner bead id>` (`store.WorkstreamManagerRole`).
  Root Managers keep their role, session, mail registration and cursor.
- **Route to the nearest, authorize the nearest or any ancestor.** Mail, waits, settle,
  Operator reporting and spawn ownership go to Owner(bead). Checks that say "only the
  owning Manager may ..." accept Owner(bead) or any owner above it (the parent
  coordinates and may repair). Commander and the user stay exempt as today.

Rollout gate: a per-project switch `nested_managers` (state.db `projects` column, new
migration; off by default). Off = Owner is the root (today, byte for byte). The
cutover verb (section 4) turns it on per project, so QA Hub's project can go first and
the commander project later at a quiet time. Once every project is on, the switch and
the "off" branch are removed.

Two new helpers carry the rule, so no site re-derives it:

- `beads.Owner(ctx, c, dir, known Parents, labels LabelsOf, id) string` (display/loaded
  data, dotted fallback like `Lineage`). Labels come from loaded rows in livedata; for
  a client read, one `List{IDs: chain, Status:"all"}` (one `bd list --id a,b` process)
  over the lineage. Cost: +1 process per resolution over today's `Root`.
- `wsmgr.StrictOwner(ctx, c, dir, id) (string, error)`: `StrictRoot`'s fresh walk plus
  the fresh label read; any failed read is an error (never creates a Manager).
- `wsmgr.OwnerChain(...)`: Owner and every owner above it, for authorization checks.

Both consult the project switch; `beads.Root`/`StrictRoot` stay for callers that
really mean the top (sidebar rank, `internal-place` top-level moves, check-in rows).

## 2. Every routing site, its new rule and tests

Unit tests below use the existing fake beads clients and private homes; a shared table
fixture `nestedTree` (root R, nested A and B under R, A.1 nested under A, step s under
A.1, legacy track T under R, closed nested C) is reused across packages.

### Resolution primitives

| Site | Today | New | Tests |
|---|---|---|---|
| `beads.Lineage` / `beads.Root` (`internal/beads/lineage.go`) | top of chain | unchanged; add `beads.Owner` beside it | `TestOwner_NearestOpenWorkstream`, `..._LegacyTrackIsNotAnOwner`, `..._ClosedAndParkedSkip`, `..._SwitchOffIsRoot`, `..._UnreadableLabelsFallBackToRoot` (display only) |
| `wsmgr.StrictRoot` (`registration.go`) | fresh root | unchanged; add `StrictOwner`, `OwnerChain` | `TestStrictOwner_*` incl. failed label read = error |
| `wsmgr.Root` (`wsmgr.go:76`) | root | becomes `Owner` (all its callers are routing: settle, waits, livegates, resolve-needs, spawn, proposals, objective edit, tab) | covered per caller |
| `wsmgr.validateTarget` | refuses `root != bead` and `kind:track` | accepts `StrictOwner(bead) == bead` (an open `kind:workstream` bead, nested or top; or an open top-level objective); still refuses track-only, gate, task | `TestValidateTarget_NestedWorkstreamIsAValidTarget`, `..._StepUnderNestedIsRefusedNamingItsOwner`, `..._TrackOnlyRefused` |

### Manager lifecycle (wsmgr, cmdrd)

| Site | New rule | Tests |
|---|---|---|
| `wsmgr.Workstreams`/`targets` + `tui.ListNumberedWorkstreams` (`column_objectives.go:1654`) | today lists top-level rows only. Add `Manager.Owners(ctx)`: every open owner (top-level rows plus nested `kind:workstream` objectives in a switched-on project). Rank/number from the board's dotted number (`1.2`); Target gets `Number string`, `Rank` stays the top-level rank for windows | `TestOwners_ListsNestedOnlyWhenSwitchedOn` |
| `Prepare` / `Preamble` | works per owner; the preamble of a nested Manager says: your scope is beads whose NEAREST workstream is you; your parent is `manager-<parent owner>`; nested workstreams under you have their own Managers | `TestPreamble_NestedNamesParentAndChildren`, prompt assets test |
| `EnsureNative` (`daemon.ensureManagers`, every 30s) | iterates `Owners`. Skips a parent whose only open child is a workstream (the board shows the child in its place; mail still starts it) | `TestEnsureNative_StartsNestedFresh`, `..._SkipsSingleChildParent`, `..._SwitchOffStartsRootsOnly` |
| `EnsureRole` (mail wake, `daemon/chat.go wakeManager`) | unchanged shape: registration row first, then cached owners (`Owners` instead of top rows). `ErrInvalidRoute` when the bead is no longer an owner | `TestEnsureRole_NestedRegistered`, `..._NestedClosedIsInvalidRoute` |
| `Open` / `internal-open-workstream` | `StrictOwner(bead)` instead of `StrictRoot`: opening a step under nested A opens A's Manager, pointed at the step. `--focus` and the native/terminal fallback unchanged | `TestOpen_StepUnderNestedOpensNestedManager`; eval `TestNestedOpenScenario` |
| `Adopt`, `OpenByRole`, `SyncNames` | Adopt stays top-level only (legacy per-project Managers). OpenByRole works by row. Windows: nested windows named `#1.2 <title>` | `TestSyncNames_Nested` |
| `wsmgr.OwnerOfRole` / `RoutedMail` (`mailroute.go`) | `StrictOwner(bead)` replaces `StrictRoot`. A role naming a step routes to its nearest owner; a nested role whose workstream closed routes to the next open owner up (note in body), not straight to Commander; Commander only when no owner is open | extend `TestOwnerOfRole*`, `TestRoutedMail*`; `TestOwnerOfRoleReadCount` updated (+1 list per mail) |
| `internal-place` `settleOwnership` (`internal_place.go:186`) | compare `StrictOwner` before/after instead of roots. A bead that stops being an owner (moved under a step, demoted) is unregistered (`UnregisterNestedManager` keeps its session); one that becomes an owner is opened. Both Managers told, as today | `TestSettleOwnership_NestedBecomesOwner`, `..._MovedBetweenNestedSiblings` |
| `internal-create-objective --workstream` / `internal-set-objective --workstream` | after the write, open the new nested Manager (as `internal-place` does for a new top-level) when the project is switched on; parent Manager told who owns it | `TestCreateNestedWorkstreamStartsItsManager` |
| `internal-unregister-workstream` | "actual roots refuse" becomes "actual owners refuse"; a non-owner registration (stale row) may be removed with `--confirm-root <owner>` | `TestUnregister_NestedOwnerRefuses` |
| `store.workstream_managers` | no schema change: rows are per bead already; nested rows become legitimate | existing |

### Operators

| Site | New rule | Tests |
|---|---|---|
| `spawn.ManagerRole` (`spawn/host.go:499`) -> prompt + `COMMANDER_MANAGER_ROLE` | Owner of the task bead | `TestManagerRole_NestedTask`, `TestHostBootstrapPrompt_NestedManager` |
| `internal-spawn-operator` manager default (`:153`), gate checks (`:415`, `:452`) | owner by `wsmgr.Owner`. New check: a `manager-*` caller that is an ancestor but not the owner is refused, naming `cmdr internal-mail --to manager-<owner>` (the parent dispatches, it does not run the child's Operators); `--as-parent` lets it through with a comment on the bead (urgent fixes while the child Manager is down). The workflow-gate scope (nearest workstream/track) is already nearest-first: unchanged | `TestSpawnOperator_ParentRefusedUnderNested`, `..._AsParentRecorded` |
| `spawn.CheckWorkstreamCap` (`workstream_cap.go`) | cap counted per Owner; the `max-operators:` label read from the owner bead. A parent's label caps the sum of its subtree too (count runs whose owner chain contains the parent), so an existing root cap keeps bounding the whole tree, for a spawn on the root's own steps too; with no label the default bounds the root's subtree (nesting never multiplies the bound) | `TestWorkstreamCap_NestedOwnCap`, `..._ParentCapBoundsSubtree` |
| `internal-operator-provider` (`:109`) "a Manager only its own" | owner chain (nested Manager or its ancestors) | `TestOperatorProvider_ParentMayRespawnNested` |
| `internal-mail` `rerouteOperatorMail` (`:234`) | Operator mail to `commander` goes to the Owner, computed fresh. NEW: Operator mail to a `manager-*` role that is not its task's Owner (a running Operator spawned before cutover still has the root in its env and prompt) is rerouted to the Owner, with the root copied once per run (`[cc: moved to manager-<owner>]`) | `TestRerouteOperatorMail_StaleManagerRoleGoesToOwner` |
| `startMissingManagers` / `workstreamForManagerRole` (`:460`) | resolve the role's bead with `StrictOwner`; open only when the bead is its own owner | `TestMailStartsNestedManager` |
| `closeDoorRole` (`door_checks.go:196`) | unchanged (owner-label walk on lineage) | none needed |

### cmdrd watchers (all mail through `RoutedMail`, so a stale address still lands)

| Site | New rule | Tests |
|---|---|---|
| settle `runManager` (`daemon/settle.go:140`), crash restart, `operator-died` hand-back | Owner of the task, computed at settle time (so post-cutover reports of old runs go to the nested Manager) | `TestSettleMailsNestedManager` |
| watch `byManager` (`watch.go:83`), check-ins (`internal-checkin`) | group by Owner | `TestWatchGroupsByNestedOwner` |
| waits (`waits.go:168,247`), live gates (`livegates.go:310`) | Owner of the waiting/gate bead | `TestWaitFiresToNestedManager`, `TestLiveGateMailsNestedManager` |
| invariant (`livedata/invariant.go:90`) | `Violation.Workstream/ManagerRole` from the nearest owner in loaded rows (labels known) | `TestInvariantViolationNamesNestedManager` |
| pitch / stale card (`daemon/pitch.go`, `NoPitch.ManagerRole`, `NeedsYou` scope) | `NoPitch.ManagerRole` and stale-card role from the owner of the card bead (`livedata/workstreams.go:221,229`) | `TestPitchMailsNestedManager` |
| health `checkPlans` (`daemon/health.go:219`) | key `plan:<owner role>` per diagram level; a nested workstream's findings go to its own Manager, not the root's. Hold: a hold on the root still quiets the whole tree; a hold on a nested owner quiets that subtree | `TestCheckPlansNestedFindingsGoToNestedManager` |
| reported gap `MarkReported` (`livedata/liveness.go:82`) | the settle's future owner = Owner, so the "Manager to close" overlay names the nested Manager | update `TestBoardReported*` golden |
| approval dispatch custody (`approval_handoff.go:83`) and resolve-needs (`internal_resolve_needs.go:138`) | `Manager dispatch` receipt and answer mail to the Owner of each released step | `TestResolveNeeds_NestedStepDispatchedToNestedManager` |
| proposals (`proposals.go:365`) | overrule mail to the Owner | `TestProposalOverruleMailsNestedManager` |

### Door checks and authority

| Site | New rule | Tests |
|---|---|---|
| `internal-gate publish`, `unlink`, `retire-legacy`, `publication_repair.go:156`, `approvedcontext/restore.go:60`, `workflow_addendum.go:65,97` | allowed actor = any role in `OwnerChain(bead)` (plus Commander/user); refusal names the nearest owner | one table test per verb over `nestedTree` |
| `internal-objective-edit` (`:390` root check, `:498` hold/park/Manager wiring) | `--hold`/`--park` still Managers + Commander; the role written into mail and `owner:` defaults is the Owner | `TestSetObjective_NestedOwnerDefaults` |
| `internal-move-card` (`:90`) | puts the ancestor back under its Owner | `TestMoveCard_NestedOwner` |
| proposals `isProposalScope` (`:86`) and standing decisions (`proposals.go:104`, `decisions.go:83`) | a nested Manager's default decision scope is its own bead; `ActiveIn(lineage...)` already includes every ancestor, so a root decision still binds nested Managers. `proposed:` labels from a nested Manager go to Commander as today | `TestDecisionsAddDefaultScopeNested`, `TestDeliversStandingDecisionsToNestedManager` |
| `decisions.Deliver` (mail hook) | per role: a nested Manager gets global + its lineage's decisions | covered above |
| Manager guard (`managerguard`) | unchanged (role prefix only) | none |

### Board, snapshot and app

| Site | New rule | Tests |
|---|---|---|
| `SnapWorkstream.ManagerRole` (`snapshot.go:412`) | unchanged for top-level rows | none |
| nested rows: `SnapStep` for a workstream step | add `manager_role` (Owner role) on steps that are owners; the board's track line (`trackLine`) reads that Manager's live state for "Manager picking it up"/working instead of the root's | `TestBoardNestedRowReadsItsOwnManager`, golden `testdata/board/nested-managers.json` |
| `ManagerWaits`/`foldWaits` (`snapshot.go:381`), `MailRoles` (`:551`), `watchOf` (`:789`), `livedata/agents.go:110` (tmux scrape) | per Owner: nested roles get agent state, unread counts, waits and their watch; scrape maps a window tagged with a nested bead to its role | `TestSnapshotNestedManagerAgentsAndWatch` |
| check-in (`CheckinReport`, `Checkin`) | still one line per top-level row; stalled places name their Owner role in the place line | update `scenarios/checkin-headline.json` |
| `cmdr board --role manager-<nested>` | filter by Owner: the nested Manager sees only its subtree; the parent with `--role` sees its whole subtree with each nested row's Manager named | `TestBoardRoleNested` |
| app `ChatSlot.tsx:45,68`, `viewmodel.ts:728,856`, `providers.ts:137`, `navDrag.ts:134` (sets `manager_role: ''` for tracks), `TaskGraph.tsx:487`, `FlowGraph.tsx:202` | role -> row lookup also searches nested steps' `manager_role`; a nested row's header and chat select its own Manager; a parent row whose single child stands in selects the child's Manager (the parent's chat via the row menu "Parent Manager"); Agents list shows nested Managers indented under their parent; `src/shared/snapshot.ts` gets `manager_role?` on `SnapStep` | `app/test/nested-managers.test.tsx` (selection, agents tier, deep link `cmdr:<nested>` opens its chat), fixture `test/fixtures/snapshot-nested-managers.json`, shots `--query 'ws=1&agent=manager'` |
| `api/tabs.go:134`, `cmd/cmdr/tab.go:67` | tabs belong to the Owner's chat | `TestTabsNestedOwner` |
| `internal/tui` sidebar (`column_objectives.go:415,982`, `workstream_rows.go:157`, chat badge/title) | working dot and badge by Owner | `TestColumnNestedManagerWorking` |

## 3. How the parent Manager coordinates

- **Dispatch.** The parent creates a nested workstream (`internal-create-objective
  --parent <me> --workstream`), which starts its Manager, then briefs it by mail with
  the outcome, ticket, constraints and deadline. It never spawns Operators under a
  nested workstream (spawn refuses; `--as-parent` only when the child's Manager is
  down, recorded on the bead). It may re-prioritise siblings
  (`internal-move-objective`) and set caps (`--max-operators` on a child).
- **Rollup.** The parent reads `cmdr board --role manager-<parent>` (its subtree, each
  nested row with its Manager and line) and its children's diagrams; it does not
  re-derive their status. Nested Managers mail the parent on milestones (gate passed,
  PR merged, blocked on a sibling, done) with a one-line result; cmdrd mails the parent
  when a child workstream closes (settle of the child bead: the invariant already flags
  a parent left with no open child, and Owner(parent) = the parent). NEEDS YOU cards
  stay the user's, raised by whichever Manager owns the step.
- **Cross-nested dependencies.** A step in A that needs a step in B gets a real edge
  (`internal-dep add <A step> <B step>`), written by A's Manager (it owns the waiter).
  Both Managers see `blocked by` on the board. B's Manager is told by mail; the parent
  is told when the edge would hold a gate or crosses two levels. Conflicts in order or
  scope between siblings are the parent's call; anything that changes what the user
  sees as top-level is Commander's.
- **The parent holds no steps** (lead.md already says so); when all children close, it
  closes itself after its own check bead.

What `prompts/lead.md` must say (and `TestNestedManagersInPrompts` pins):

1. Replace "You stay the one Manager for everything under your top-level workstream,
   and Operators you spawn anywhere under it report to you" with: every nested
   workstream has its own Manager (`manager-<its bead>`); you own the beads whose
   NEAREST workstream is yours; Operators report to that Manager.
2. A "Nested Managers" block: as a parent, dispatch/rollup/cross-deps as above, with
   the exact commands; as a nested Manager, your parent is in your preamble, mail it on
   milestones and when blocked by a sibling, never write beads under a sibling.
3. `cmdr:` links to a nested workstream open its Manager's chat.
4. `prompts/operator-code.md`: unchanged ("report to $COMMANDER_MANAGER_ROLE"); add
   that a reroute note in a reply means your Manager changed.
5. `commander.md`: a nested Manager is addressed directly; Commander's check-in still
   reads top-level rows.

## 4. Migration of running work (no lost mail)

Cutover verb: `cmdr internal-nest-managers --project P [--apply|--undo]`
(Commander/user only; dry run by default, prints the plan as JSON).

Dry run lists, per nested owner: its new role, registration state, running Operators
whose Owner changes (with their current `COMMANDER_MANAGER_ROLE`), open asks/cards,
waits and check-ins currently keyed to the root, and the root inbox's unread mail whose
subject or body names a bead in that subtree.

Apply, per nested owner, in this order:

1. **Register before routing.** `Prepare` (session file, prompt, am registration,
   `workstream_managers` row) and write the new role's mail cursor at the current
   inbox head (`mailstate`, as a role's first delivery would), so mail that arrives
   between now and the first start is news, never "history" held by the
   no-cursor rule. A role with no cursor is exactly what `mail-history` alerts on.
2. **Flip the switch** for the project (once every nested owner is through step 1).
   From this moment every resolver
   above returns the nested owner. Mail already sent to the root stays in the root's
   inbox; nothing is moved or re-marked (am has no move).
3. **Start the nested Manager** (`EnsureTarget`) with a handover message: its running
   Operators and their beads, open cards, waits, check-ins, and the subjects + ids of
   the root's unread mail about its subtree (from the dry run), with
   `cmdr internal-inbox --agent manager-<root>` as the place to read them. It is a
   copy for context; the root still owns those mails.
4. **Tell the root Manager** (high importance): which subtrees now have Managers,
   which of its Operators now report elsewhere, and that it should forward (not act
   on) any unread mail listed for them, then mark it read itself.
5. **Running Operators are not restarted.** Their env/prompt still name the root;
   `internal-mail` reroutes their reports to the Owner (copying the root once per
   run), settle/crash/died mail goes to the Owner computed at settle time, and
   their Manager-sent mail keeps arriving by hook (the Operator's own cursor is
   untouched). Respawns (`Resume`) get the new role in env and prompt.
6. **Waits and check-ins.** Labels live on beads, so waits re-route by themselves at
   the next poll. In-memory check-in schedules keyed by the root (`internal-checkin`)
   are re-keyed by Owner of their bead at the next watch pass; a check-in set by the
   root on a nested bead fires to the nested Manager with a note.
7. **Cards.** Open NEEDS YOU cards stay where they are (they are on beads); the
   pitch and stale-card mail go to the Owner from now on. Answers already journalled
   (`needs_resolutions`) re-send pending mail to the Owner, carrying the stable answer
   id, so a duplicate is ignorable.

Rollback: switch off (the verb's `--undo`): resolvers return the root; nested
registrations and sessions stay (they resume on a later switch-on); the root gets a
handover in reverse. Mail sent to a nested role while on stays in its inbox; the
`[no Manager]`/route rule sends later mail up to the root because the nested bead is no
longer an owner, and the dry run of `--undo` lists unread nested mail for the root.
Review fixes (il9.203.3.2): undo also mails each nested Manager to stop dispatching,
and each root only its own nested Managers' unread mail; an Operator spawned while on
still names its nested Manager, so `internal-mail` reroutes its report to the task's
current owner (the root) both ways; the spawn door refuses a Manager outside the step's
owner chain, so an undone nested Manager cannot spawn. A rerun of `--apply`/`--undo`
mails nobody again. Ownership is one rule (`beads.ChainOwners`): a nested workstream
under a closed, parked or superseded workstream, or under any closed bead, owns
nothing, for routing, registration and the board alike.

## 5. Risks

- **Read cost.** Every Owner resolution adds a label read (`bd list --id` over the
  chain). Watchers already pay 4 shows per routed mail; budget +1 list. Livedata uses
  loaded labels (free). Measure with `TestOwnerOfRoleReadCount`.
- **Disagreeing resolvers.** If one site keeps `Root` while another uses `Owner`,
  reports go to one Manager and waits to the other. Mitigation: an AST test like
  `TestEveryClaudeLauncherIsAccounted` that lists every `beads.Root`/`wsmgr.Root`/
  `StrictRoot` call and fails on one not in an allow-list of "means the top" sites.
- **Manager count.** commander alone would go from 2 to 6 live Managers; plan usage and
  the Manager runtime share scale with it. Single-child parents are not auto-started.
- **Stale env in running Operators** (`COMMANDER_MANAGER_ROLE` = root): covered by the
  internal-mail reroute; a raw `am` send would still land at the root (agents never
  call `am`).
- **Split brain during cutover.** The root Manager may act on a nested step between
  flip and its notice. Mitigation: notice is high importance and arrives at its next
  tool call (mail hook); spawn refuses parent dispatch under a nested owner.
- **Moves.** Moving a bead between nested siblings changes its Owner mid-run;
  `settleOwnership` covers registration and notices, settle resolves at settle time.
- **Legacy `kind:track`.** Unmigrated projects never get nested Managers; that is
  intended, and the dry run says so.
- **History rule.** A new role without a cursor makes old mail "history"; step 1 of the
  cutover (and `Prepare` for any new nested Manager) writes the cursor first.

## 6. Build order

1. `beads.Owner`, `wsmgr.StrictOwner`/`OwnerChain`, project switch (migration),
   `nestedTree` fixture, resolver AST test. Switch off: no behaviour change.
2. wsmgr lifecycle: `validateTarget`, `Owners`, `Prepare`/`Preamble` (+ cursor stamp),
   `EnsureNative`, `EnsureRole`, `Open`, `OwnerOfRole`/`RoutedMail`, `settleOwnership`,
   create/set `--workstream` start, unregister.
3. Operators: `spawn.ManagerRole`, spawn parent refusal, cap, operator-provider,
   internal-mail reroute (incl. stale manager role).
4. cmdrd watchers: settle, watch/check-ins, waits, live gates, invariant, pitch,
   health, reported gap, resolve-needs, proposals.
5. Door authority (`OwnerChain`) on gate verbs, publication repair, addendum,
   approved context, move-card, decisions scope.
6. Snapshot/board (`manager_role` on owner steps, agents/watch/waits/mail roles,
   `board --role`), goldens; then the app (selection, agents tier, fixture, shots).
7. Prompts (`lead.md`, preamble, operator/commander notes) + prompt tests.
8. Cutover verb (dry run, apply, undo) + eval `TestNestedManagersCutoverScenario`
   (private home, fake am server: a running Operator's report and a wait fire after
   the flip land at the nested Manager; the root's unread mail is listed in both
   handovers and none is marked read or lost).
9. CLAUDE.md section; separate review (il9.203.3); ship (il9.203.4). The user installs;
   then Commander runs the cutover on QA Hub's project first, commander's later.
