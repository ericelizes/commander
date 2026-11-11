# Shared work tree and Commander jurisdictions

## Product contract

Keep the existing priority-ordered sidebar and nested workstream structure.
Workstreams contain goals and work beads. Their structure is independent of
agent processes. PostgreSQL is the canonical source for the tree, dependencies,
priorities, acceptance criteria, holds, decisions, claims and execution evidence.

One Commander conversation normally owns planning for the whole workspace.
The user can open another conversation for an urgent investigation and transfer
a subtree to it. The original Commander continues handling everything else.
There is exactly one effective planning owner per bead. Parent jurisdiction
inherits down real parent edges; explicit nested boundaries override it.

Execution agents claim ready beads by capability and capacity. They investigate
and make ordinary implementation choices directly. They record activity,
results and blockers without requiring a planning conversation for each write.
Planning decisions exceeding their authority go to the current planning owner.
Cross-jurisdiction changes require recorded coordination; only genuine user
preferences and approval decisions become Needs you.

Commander can investigate and execute urgent work directly. A thread is not a
mandatory delegation layer. Interrupting the urgent conversation must not block
other conversations, execution agents, or durable decision processing.

## Ownership and handoff

`commander_sessions` identifies logical Commander conversations independently
of processes or providers. `commander_jurisdictions` stores explicit overrides
on existing beads; it is not a second task tree. `jurisdiction_projects` stores
the default owner and a revision used for compare-and-swap transfers.
`commander_planning_owner(project, bead)` resolves effective ownership through
actual ancestry with a cycle guard.

Transfers require the expected owner and revision, an authorized current owner
or the user, and a reason. One transaction changes the boundary, increments the
revision, records history, invalidates graph caches and fences prior supervision
requests for the affected scope. Nested overrides remain unchanged. An ongoing
executor retains its active claim. A result arriving after handoff goes to the
new planning owner. Holds, approval evidence and conversation histories remain.

The first migration retains legacy Manager scope boundaries and runtime
addresses. Legacy registrations cannot overwrite transferred jurisdiction.
These are compatibility addresses, not a requirement for permanent Managers.
The final orchestration cutover must remove automatic per-workstream process
creation and migrate retained conversations through an explicit adapter.

## Execution and supervision

Ready intent is recorded on the canonical bead with a brief, acceptance criteria,
required capabilities and a plan revision. `cmdr execution ready` requires both
the expected plan revision and jurisdiction revision. Only the planning owner or
user can change readiness. Slots represent available runtime capacity, with one
execution per slot; they do not own workstream plans.

A short PostgreSQL transaction reserves an eligible leaf and a capable slot.
Workstream ranks and nested bead ranks determine selection order. Both global
capacity and workstream limits include startup reservations, preventing a burst
of concurrent launches from oversubscribing a cap. The local adapter advertises
code and knowledge execution; unsupported capabilities remain an internal
planning question rather than a fabricated running agent.

Startup custody expires after five minutes and is renewed during preparation.
Binding a run checks the token, plan and responsibility generations, current
holds/waits/dependencies, project activation, leaf status, slot enablement and
capability match. Binding happens before launching the runtime process. Once
bound, a real run keeps its slot until terminal evidence; a timeout alone cannot
steal it. Failed startup retains the cause and retries with a delay, capped at
five attempts before requiring planning recovery. Re-readied work can be picked
up by another slot even if its former slot was disabled.

`orchestration_control` records the installation-wide off/shadow/active mode and
version. The daemon reads it before starting workers, and a controlled restart
applies a change. This prevents different CLI and daemon environments from
silently choosing different orchestration. Active mode replaces the old
proposal scheduler and automatic per-workstream Manager creation. Explicitly
opening a workstream or nested step resolves its current planning jurisdiction.

The sidebar distinguishes queued work, runtime preparation, missing capability
and an exhausted launch failure. Queuing is durable custody, not evidence of a
working process. Runtime facts are read in one fresh batch and matched to plan
revisions, separately from the cached task graph. Existing canonical execution
counts continue to require actual execution evidence.

Planning ownership and execution responsibility are separate. A run claim
binds a concrete execution identity/session to its bead. Responsibility survives
process exit and transfers durably to the planning owner when a result is ready.
State mutations and event publication commit together.

Supervision consumes committed events and performs bounded periodic recovery.
Runtime presence, activity, acknowledged responsibility, meaningful phase
progress and completed outcomes are distinct facts. A heartbeat or chat line
cannot reset the progress clock. Missing runs may resume through a validated
capability adapter; failed delivery is not acceptance. Recovery must be durable,
deduplicated, bounded and respectful of holds, dependencies and approval gates.

## Durable questions and execution checkpoints

An execution question records its immutable run, logical session, native
provider/thread, current plan revision, recommendation and checkpoint. Related
scopes form a durable inbox for their current planning owners. Partial answers
must be revalidated after an ownership change; finalized answers remain history.
A refusal completes the question as a recorded decision, without granting any
release permission. Plan changes supersede unanswered questions.

A blocking question prevents further dispatch while its execution exits
normally. Answer-before-exit and exit-before-answer converge on one resumable
checkpoint. A successful replacement run consumes that resume intent. Explicit
re-planning clears it; a failed launch restores it only for the unchanged plan.
This prevents later independent work from accidentally reopening an old session.

The host adapter validates the original session and provider before changing
runtime metadata or launching a model. Codex also requires the recorded native
thread; Claude requires its transcript, with existing account transcript carry
supported. Missing native state produces a concrete recovery failure rather
than silently creating a new conversation. Cross-provider checkpoint handoff
still requires a separately validated recovery path before cutover.

## Versioned batch planning

`cmdr plan apply --file plan.json` submits an atomic batch through the existing
`/api/plan` route backed by the existing write interface. The JSON contains `dir`, `basis`, `ops`, and optionally
an existing holding `intent`. Caller identity comes from the current runtime,
not an actor supplied in the plan. For example:

```json
{
  "dir": "/path/to/project",
  "basis": {
    "request_id": "revise-integration-20261110",
    "jurisdiction_revision": 3,
    "beads": {"goal": 7, "task": 12}
  },
  "ops": [
    ["update", "task", "--description", "Implement the agreed behavior"],
    ["update", "task", "--acceptance", "The regression is verified"]
  ]
}
```

Every existing mutation target, new parent and dependency endpoint requires its
read revision. Extra basis entries express additional read dependencies. The
backend checks them under the project graph lock inside the write transaction,
before any operation runs. One conflicting revision, ownership change or invalid
operation rolls back everything. Dependency changes version both endpoints;
adding, moving or removing children versions their immediate parents. Existing
approval, review and jurisdiction checks remain in the write path. Only a
registered planning Commander or the user can submit a versioned plan, and a
missing service fails closed. A daemon without the planning route refuses the
request before any mutation, so an older write endpoint cannot silently drop
version checks. The API does not wait for a board snapshot to acknowledge a
committed plan.

Creation operations can declare `--ref goal`; later identity positions use
`$goal`, including `--parent=$goal`, dependency endpoints and comments. Names
must be unique within the batch and declared before use. Titles, descriptions
and evidence remain literal text. The response's `created` map returns every
alias and its actual bead ID, so a complete nested tree can be created in one
request. Existing write checks run again on resolved operations inside the same
transaction and can read newly-created beads; a refusal rolls everything back.

A plan receipt commits in the same transaction as its edits. `basis.request_id`
is an optional stable client key; without one, the original basis, operations
and holding intent determine a content key. Identical retries return the saved
result with `replayed: true`, including original created IDs. An explicit key
reused for different content is refused. Failed batches leave no receipt. Replay
is a historical read, so later plan changes or jurisdiction transfers do not
cause the original mutation to run again. Receipt identity includes project and
caller. The API checks receipts before current write guards, then rechecks under
the graph lock for concurrent first submissions.

## Exact cross-jurisdiction coordination

`cmdr plan propose --file plan.json` saves the immutable plan through
`/api/plan/propose` and creates an ordinary durable decision question in the
same transaction. It does not mutate the tree or stop existing execution.
Affected owners inspect `cmdr plan get --id ID` and answer the matching question
through `cmdr question answer`. A registered Commander may ask another
Commander's scope a question; asking grants no write authority.

Once every affected owner agrees, the requester applies the original saved
payload with `cmdr plan apply --proposal ID`. Its basis, operations, holding
intent and requesting Commander must match the proposal. The application
transaction checks current ownership, jurisdiction revision, read versions and
all approvals before allowing the exact cross-scope edits. A refusal, changed
payload, ownership handoff or plan change rejects the grant. Retrying a committed
plan still returns its original receipt. Coordination is internal and creates
no user decision card; agreement is not release permission.

Every actual read dependency is a decision scope, including closed records.
Top-level creation has an explicit project scope, so an empty project needs no
placeholder task. A request supports up to `store.MaxQuestionScopes` scopes;
there is no nine-owner limit. Planning decisions never advance execution
progress or queue an execution checkpoint. Jurisdiction changes supersede
pending proposals, while ordinary execution questions follow the current owner.
Their durable notification leases share supervision's per-recipient dispatch
fence, retry/backoff and shadow-mode behavior. Delivery does not answer a question.

Publication is an exclusive typed action in the same review/apply interface:

```json
{
  "dir": "/project",
  "basis": {
    "request_id": "repair-publication-once",
    "jurisdiction_revision": 4,
    "beads": {"gate": 3, "scope": 8, "leaf": 2}
  },
  "publication": {
    "gate": "gate",
    "parent": "scope",
    "waiters": ["scope"],
    "reason": "Preserve the protected leaf approval and remove its redundant ancestor wait"
  }
}
```

`cmdr plan propose --file publication.json` calls `/api/gate/publish` with
`propose: true`. The service runs the full publication validator without writes,
then atomically saves this typed payload and the affected decision scopes. After
all owners agree, `cmdr plan apply --proposal ID` uses that same typed endpoint.
The action's semantic hash is distinct from ordinary plan operations. Neither an
ordinary plan agreement nor a publication agreement authorizes the other's
endpoint. No synthetic receipt-identity operation is executed as a plan.
The graph validator, pending-human-answer checks, immutable workflow edges and
exact transaction-local repair grant still apply at execution. Publication does
not pass the gate, approve a release or erase its original approval history.

## Execution evidence and planning authority

In active orchestration, every non-planning actor must supply its immutable
current run and session to write execution evidence. The run must still own
that exact bead. The shared PostgreSQL mutation path permits active execution
phases, open/in-progress status and run-bound comments. It refuses goal/brief/
acceptance/priority edits, ownership or parent changes, new beads, dependencies,
hold or gate labels, closure and deletion. Late or foreign sessions cannot post
ordinary evidence. Registered planning owners retain planning authority.
Shadow mode preserves the prior runtime behavior until coordinated cutover.

Runtime instructions and all legacy command paths still require integration
checks before cutover. In particular, the low-level planning API must share the
full typed workflow/approval close-time validators; its existing `checkWrite`
preflight alone does not prove all release/review gates are preserved.

## Recorded human direction at the mutation boundary

Active PostgreSQL writes preserve human holds and decision markers even when a
caller bypasses the high-level CLI. Clearing a hold, dismissing an ask, marking
it answered or closing a human gate requires the current typed decision journal.
Coordination votes cannot substitute for that human answer. Agents cannot weaken
human gate definitions. Completed work must be reopened before adding children
or changing its required dependency edges; open children, unpassed dependencies
and live executions prevent false completion.

`human_scope_epoch` identifies the question's goal and release conditions:
brief, acceptance, placement, type and relevant labels such as its PR. Runtime
phases, ownership hints, waits, priority ordering and adding implementation tasks
under the same goal do not invent new human decisions. `human_authority_epoch`
also distinguishes new hold/request assertions and reopened gates. Pitch versions
capture the scope epoch when asked; typed answers issue authority for that
recorded scope and current restriction epoch. A changed question is not approved
by a delayed answer. Replaying an old journal cannot grant permission for a later
hold. Canonical cleanup of answered markers does not invalidate the answer.

The decision UI sends its displayed planning revision. The typed answer path
checks it before recording a new answer, and saving checks the observed revision
again under the project lock. Automatically displayed gates and plain asks are
journalled from their current displayed decision before processing an answer.
A non-gate approving Resume choice records approval without marking the work
complete. New custody and notifications use current Commander jurisdiction while
the original journal provenance stays immutable.

A deferred or retired required gate is not a passed gate. SQL dispatch checks,
supervision blocker counts and the shared sidebar holding predicate keep it as
a blocker. Dropped work can be parked without being reported complete. Explicit
holds have a parked holder, and an idle scoped Commander is not mistaken for an
execution agent. Scoped Commander turns retain the user's standing decisions.

Before active cutover, finish already-saved legacy answers in shadow mode and
inventory unanswered historical cards. Older records do not have scope-epoch
proof and must not be silently rebound to a later hold. The shared completion engine is now in `internal/workflowclose`. The typed CLI
and the active PostgreSQL API use the same workflow graph, current-head, CI and
independent-review checks. The API binds evidence writes and close to the write
transaction; a later failed operation rolls back the generated evidence as well.
Direct active closes without that verifier fail closed. Runtime provenance is
read through the transaction query handle, including historical runs of generic
executors. Review identity is not inferred from a mutable bead assignee.

Typed no-PR requests now carry a target-bound definition digest through the
write door. The digest covers criteria, PR labels, gate/workflow identity, real
parent/dependency edges, holds and current planning owner. The service checks it
under the same graph lock as the mutation and derives attribution from the
caller. It accepts only the existing narrow missing-PR waiver, with a bounded
reason and explicit user-approval evidence; a gate naming a PR still refuses.
It never infers a waiver from arbitrary comment text. Typed pass, retirement and
replacement use this path; versioned planning requests reject waiver metadata
until it is part of their exact coordinated/replay payload contract.

Generated workflow identity, snapshot and placement are immutable. Retirement,
deletion and removal of a merge-target identity are checked while the old
requirement is still visible. An unfinished required gate cannot be unlinked to
release work. Stage completion also checks its own gate references even when
the gate is a sibling rather than a child or an explicit blocking dependency.
PostgreSQL gate retirement writes its status and retirement marker in one atomic
update. Existing unfinished retirements of required gates/workflows are routed
to the current planning Commander for reconciliation; a retirement marker is
never accepted as proof of a passed requirement. Intentionally parked work stays
parked. Parking a workflow check remains possible, but it stays an unpassed dependency
in SQL dispatch, shared holding and bead readers. Minor follow-up creation uses
the completion transaction and is idempotent before commit; a later refused
check rolls back those new beads and links.

Merged-without-review settlement revalidates the saved settlement against the
current merged head and every guarded PR, preserving its unreviewed meaning.
Ordinary automatic gates use `POST /api/gate/check`. The service authorizes the
planning owner or user, reads the recorded command itself, and runs it outside
the graph transaction. It limits concurrent evaluations to four, coalesces
identical in-flight requests, bounds shell duration and retains only the output
tail. The commit locks the gate and checks its exact definition, plan revision
and progress generation. A changed gate or a newer committed result refuses the
old result without modifying its state. Success and failure evidence, status
and failure labels commit together. Raw passing writes, counterfeit result
comments and direct failure-label edits cannot manufacture an automatic fact.
Generated workflow steps continue to use their own workflow verifiers.

The CLI and background condition watcher both delegate to this path once. The
watcher reads automatic gates and their referenced stages fresh, independent of
a stale display cache or optional mail availability. PostgreSQL failures are
recorded immediately rather than leaving a known failed condition green for a
second poll; legacy backends retain their old consecutive-failure policy.
Notifications route to the current planning Commander. Identical verified
observations return unchanged without new comment history, events or progress;
changed checker facts extend the existing bead comment history with
service-owned typed metadata. They are not separate tasks. Completion clocks
are monotonic: a late receipt on an old closed child cannot move the parent's
last-completion time backwards.

The sidebar follows the same unpassed-dependency rule across workstreams:
deferred required checks remain explicit waits, while unfinished retired checks
name the reconciliation required. Neither becomes a human decision or a working
agent. Generated workflow steps retain their requirement identity through the
bead-to-snapshot projection; ordinary parked work remains out of play.

New PostgreSQL review addenda are recorded by the current planning Commander,
with service-owned recorder and jurisdiction-revision attribution in the existing
comment journal. Ownership is checked again inside the mutation transaction.
Completion rechecks the original artifacts and immutable task-run identities;
the stored authority survives later jurisdiction handoffs. Historical Manager
records retain their legacy validation path. A plain copied comment is not new
recording authority.

This integration is not the cutover sign-off. Canonical publication now uses a
versioned graph transaction and a private exact repair grant, preserving the
protected leaf and human approval. Cross-owner typed repair coordination, final
runtime and fair-supervision integration remain required. Human-direction invariants stay
independent checks, not substitutes for CI or review proof.

## Atomic publication repair

PostgreSQL ordinary publication and redundant-ancestor repair use authenticated
`/api/gate/publish`. The typed CLI reads current bead/jurisdiction versions and
delegates once, with no local fallback. Under the project lock, the service checks
current planning ownership, the exact read basis and pending human answers. The
compiled gate validator proves real ancestry, protected leaf coverage, absence
of released live descendants and the resulting graph's cycle safety before any
repair grant becomes active. That grant allows only its exact redundant edges,
gate placement and original/completion audit. It cannot close a gate, change
criteria or grant approval. A late failure rolls back every mutation and audit.

Receipts reuse the existing planning journal, with a publication-action hash
namespace to distinguish mechanical actions from ordinary batches. Exact lost
responses replay their committed result without new work progress. Dry runs
write neither state nor receipts. Raw audit comments/environment flags and
unrelated plan coordination grants cannot authorize repair. Different relation
types are deleted independently; parent edges still require the guarded parent
operation. Same-owner scoped Commander repair is integrated; exact cross-owner
publication-action proposals remain part of the coordination work before
cutover, rather than accepting an unrelated planning approval.

## Runtime planning and execution contracts

Claimed execution uses the embedded `executor.md` prompt across the code and
knowledge adapters. It reads the canonical project-root bead interface, makes
routine choices locally, records evidence on its assigned bead, and uses durable
questions with immutable run/session identity for decisions outside its scope.
A blocking question saves a checkpoint and stops without manufacturing a final
result. Executors are not instructed to create children, change the tree, close
beads or maintain permanent Managers. Launch/resume briefs retain the exact
assigned goal, acceptance criteria, current owner and saved answer context.
The scheduler leaves model choice to the runtime; claimed launches resolve the
configured execution model before validation. An unreadable PostgreSQL planning
owner refuses launch rather than falling back to a historical Manager.

Scoped Commander chats use `planning-commander.md`. The default Commander uses
it once orchestration is active; off/shadow mode retains its legacy recovery
contract. Active planning owns the tree directly using versioned atomic batches,
readiness and exact cross-owner proposals, rather than delegation to workstream
Managers. Native chat and cockpit entry points select the same contract from
canonical mode. Reopening an existing cockpit refreshes its prompt without
changing conversation identity. Active cockpit startup does not recreate legacy
Manager processes, and canonical project lookup does not depend on a retired
`state.db` file.

`commander-executor-runtime.json` drives private PostgreSQL integration across
both project types and both subscription runtimes: real scheduler reservation,
real launcher preparation, installed prompt, run/session binding and ownership
handoff question routing, with mocked detached tmux so no model starts. Separate
native resolver/cockpit tests exercise prompt selection and existing-session
refresh. `TestCommanderRuntimeContractInIsolatedPostgresCockpit` additionally
starts the rebuilt CLI on an isolated tmux server with private PostgreSQL,
captures `scripts/cockpit-test.sh text`, verifies the canonical workstream and
active prompt, and confirms no legacy Manager windows are created. Real model
binaries are disabled. This is startup/rendering evidence, not a full execution
flow: native model behavior, foreground claims, Docker claim custody,
cross-provider checkpoint recovery and the complete UI state audit remain
release requirements.

## Implementation scope and release evidence

The jurisdiction, execution and supervision scope below is implemented. Release
qualification also requires the installed-version and read-only live checks; source
coverage alone is not evidence that a cutover has happened.

- Focused Commander chat creation, independent session/runtime recovery,
  provider fallback, scoped context, urgent-thread navigation and return handoff.
- Enforced jurisdiction on all planning doors, batched JSON edits with revision
  checks, cross-boundary proposal/decision records and conflict reconciliation.
- Complete generic execution integration, including executor plan capabilities,
  question/answer checkpoint resumption and migration away from legacy runtime
  assumptions, preserving approval and independent-review boundaries.
- Durable decision inbox routed by effective planning owner; superseded requests
  fenced when ownership or plan changes, genuine user questions surfaced once.
- Completion of supervision retry/acknowledgement/audit correctness, bounded
  escalation, dependency release and generic capability adapters.
- Sidebar projection of planning owner, actual execution count, truthful next
  action and waits, including nested and hidden execution beads.
- Populated forward migration, restart/replay and concurrency tests; fake-agent
  full-flow eval; bounded query/load verification; full Go and UI checks.
- Independent review, isolated cockpit verification, one follow-up PR, CI,
  backup, live shadow observation, authorized install/cutover and read-only
  verification of real ownership, claims, results and UI event updates.

Current targeted integration coverage lives in `internal/store/jurisdiction_test.go`,
`jurisdiction_migration_test.go`, `supervision_test.go` and
`internal/supervision/worker_test.go`. They cover inherited scope, nested overrides,
concurrent transfer conflicts, rollback, retained legacy ownership, holds and
execution preservation, result routing, acknowledgement races, restart retry
state and shadow mode. Broader product coverage includes the native execution,
question/checkpoint, atomic-plan, authority, canonical-state and detached-cockpit
evals, plus the renderer jurisdiction and nested ownership tests.

## Recovering obsolete saved human decisions

A saved answer remains immutable even if its scope can no longer authorize the
current work. First try the existing `internal-resolve-needs --resume-only`
path. A still-applicable answer must finish without asking again.

`cmdr decision inspect --project-id ID --bead ID` reads current work, versions,
Commander, scope/restriction epochs, exact pitch history and the saved journal.
For changed conditions or a historical question with no recorded scope proof,
`cmdr decision reconcile --file PATH` submits an exclusive typed request to
`/api/decision/reconcile`:

```json
{
  "project_id": 7,
  "bead_id": "held-work",
  "request_id": "reconcile-obsolete-answer-once",
  "plan_revision": 12,
  "jurisdiction_revision": 4,
  "generation": "3",
  "answer_id": "the-original-saved-answer-id",
  "reason": "The changed release conditions are outside the original answer",
  "ask": "Resume changed work?",
  "decision": {
    "question": "Resume this changed release scope?",
    "context": "Explain the changed conditions and preserve the original answer in history.",
    "options": [{"label": "Resume", "approves": true}, {"label": "Keep held"}]
  }
}
```

Attribution comes from the session, not the JSON. Only the current registered
Commander may reconcile an existing genuine human restriction on open work.
Version, jurisdiction, generation and exact saved-answer identity are checked
under the project lock. Current scope/authority proof refuses an unnecessary
replacement question. Applied answers are not reversed. An answered historical
pitch missing its journal requires provenance recovery rather than treating it
as unanswered. For an unanswered historical question, omit `answer_id`.

The transaction creates a fresh, unanswered pitch with current scope proof,
marks only the obsolete unapplied journal superseded, replaces the queue card,
and saves an immutable reconciliation receipt. Original answer payload, pitch,
choices and authority history are retained. It does not edit bead state, labels,
results or execution progress. Holds and gates remain unpassed; no authority is
issued for the new question until a new explicit human answer is recorded.
Concurrent requests cannot create duplicate questions. A late failure rolls all
changes back, and exact retries return the original result. Missing service
never falls back to local writes. This supplies the administrative recovery
interface; the populated shadow migration and final live cutover audit remain
required.

## Consolidating imported planning ownership

Imported `commander_jurisdictions` rows record `origin: legacy`; ordinary scope
transfers record `origin: explicit`. An explicit same-owner transfer also changes
provenance to explicit. Consolidation therefore follows recorded intent rather
than guessing from role names. Initial schema upgrade and empty-destination
imports preserve the old boundaries until this reviewed transition occurs.

In shadow mode, inspect `cmdr jurisdiction inventory --project-id ID`. Its
`/api/jurisdiction/legacy` response includes the default Commander, jurisdiction
and orchestration-control revisions, imported boundaries and a snapshot hash of
all actual parentage, planning versions and boundary provenance. Heartbeats and
execution progress do not invalidate this inventory; changed planning does.
Submit `cmdr jurisdiction consolidate --file PATH` with this reviewed basis:

```json
{
  "project_id": 7,
  "jurisdiction_revision": 4,
  "default_commander": "commander",
  "control_revision": 2,
  "snapshot": "the-64-character-inventory-snapshot",
  "request_id": "consolidate-imported-planning-once",
  "reason": "Retain explicit focused scopes and move imported planning to Commander"
}
```

The authenticated `/api/jurisdiction/consolidate` endpoint accepts current
session attribution, never an actor in the file. Only the user or project
default Commander can perform this shadow-mode transition. It rechecks the whole
inventory under the project lock, removes only imported boundaries and records
each return to inherited ownership. Explicit nested scopes, including explicitly
retained legacy identities, remain intact. Execution custody is preserved even
when a running agent has the same identity as a retired planner. Tree, planning
versions, progress, holds, decisions, conversations and runtime registrations
remain unchanged. Execution questions follow the resulting current owner.

New responsibility generations fence old recovery deliveries. Boundary history,
responsibility changes and a `jurisdiction_consolidations` replay receipt commit
atomically. Identical concurrent/lost-response retries do not repeat the
migration, including after activation; changed intent under one key refuses.
Updates and history use set-based statements rather than a round trip per bead.
Missing service never falls back to local writes.

The result's `retired_planning_candidates` list is an inventory for the runtime
retirement phase, not permission to kill those identities. That phase must
recheck current planning ownership and execution custody across projects before
stopping a planning host; live executions and retained conversations must
survive. This database transition does not itself prove runtime retirement,
populated backup/restore, shadow draining, active readiness or live UI correctness.
Those remain cutover requirements.

## Planning identity and execution caller context

A logical planning identity can appear on a live execution run during migration.
Registration alone does not give that execution session planning authority.
Nonempty run ID, run session or inherited Manager attribution identifies an
execution caller. In active orchestration, that context takes precedence over
Commander registration and role spelling. Current run/session/bead custody
permits phase, status and ordinary execution evidence; stale, partial or terminal
identities refuse. A planning handoff does not silence the still-current run.
An anonymous execution context cannot fall through to interactive-user authority.

Versioned plans, proposals, publication, planning gate checks, review-addendum
stamps, completion waivers, readiness, planning-question answers, human decisions
and jurisdiction/installation changes require a logical planning or user context
with their existing ownership and decision checks. An execution caller cannot
borrow the same identity's planning permissions. A logical planning conversation
continues to own its scopes independently of an execution run's custody.

The write transport preserves inherited Manager attribution as well as immutable
run/session identity. Explicit server contexts, including empty attribution, do
not inherit another request's or daemon's execution identity. Automatic check
facts are still service-owned; execution-attributed requests are rejected before
the check command runs. These context checks do not replace the remaining native
foreground-claim, runtime retirement and end-to-end UI verification work.

## Native process custody

In active PostgreSQL mode, native execution chats resolve their existing current
run and assigned bead directly from the database before role-name conventions.
The resolver requires one unambiguous live run, its exact session, recorded code
worktree (or knowledge project workspace), current brief/acceptance and current
planning owner. It refuses unbound operators and ambiguous identities instead
of falling back to old operator session files. Imported registered planning
identities use the direct planning contract; a consolidation-retired identity
without current scopes cannot restart its legacy Manager runtime.

Execution specs carry an immutable run ID. Child environments explicitly clear
ambient Manager/run attribution and install the actual run/session. The detached
host handshake retains the run ID across adoption. A different or unstamped
execution host is not accepted as proof of the current run, and an execution
start does not kill a host belonging to another session. Before launching a new
process, the resolver checks custody again. Execution account/runtime steering
is not inferred from a legacy Manager role name.

Native state publication captures the originating process spec and uses the
run-aware store sink. Late frames, exits and state callbacks cannot write a
replacement run's state. Transcript hydration without a live process spec does
not persist activity as current work. Heartbeats retain the same session/run
fence. Codex execution threads are keyed by immutable session, avoiding reuse of
another bead's role-level thread or handoff. A retained session keeps its thread;
validated cross-provider checkpoint recovery still needs separate verification.

Normal reservation readiness still requires an open bead. Existing executions
may resume in-progress work, while holds, dependencies, saved answers, project
state and pending result review prevent a fresh native process. A live stamped
host may still be adopted under a hold so it remains manageable.

The detached fake-model harness exercises real native hosts and PostgreSQL
state, including process adoption, replacement-run callbacks and inherited
environment. These checks establish binding for existing claims. They do not
complete unstamped legacy-host migration, retirement of already-running planners, or
end-to-end sidebar/provider behavior; those remain release requirements.


### Native execution completion and capacity

A native process bound to an execution run ends after its first result. The host
closes stdin and bounds shutdown even when the daemon is absent. Only observed
physical exit produces a durable role/run/session receipt; a dropped socket is
not exit evidence. Receipt consumption uses the same graph-locked execution
closure transaction as supervision, preserving the first terminal status and
protecting replacement runs. An unfinished bead stays open and no user decision
is manufactured. Once exit is committed, the slot can claim another ready bead.

Receipts survive failed database commits and daemon restarts. An uncommitted
receipt blocks accidental relaunch of the same role; independent valid receipts
can still reconcile. Invalid records are quarantined for diagnosis. Saved
checkpoint answers do not free a live claim: after physical exit, continuation
uses a fresh capacity reservation with the validated source run and session.
Legacy Manager-named executions use this same lifecycle without inline provider
restart. Native Codex checkpoint thread lookup is fenced by recorded run/session.

The native-exit-lifecycle scenario tests these rules with isolated PostgreSQL and
actual fake-model processes, including disconnected and absent daemon recovery.
The actual fake Codex app-server/bridge/native-host path also proves physical
exit and replacement-session fresh thread/start without thread/resume. Raw
scheduler/native custody integration, validated cross-provider recovery, live
plan revocation, activation preflight, migration and full sidebar proof remain
required before release.


Fresh PostgreSQL execution admission is now fenced installation-wide. In active
mode, InsertAgentRun requires the canonical startup claim; role names, registered
planning identities and absence of a competing reservation do not grant a bypass.
The mode row is held FOR SHARE through run binding and reservation, serializing
with disable/cutover updates. Off mode refuses fresh launches. Shadow retains
legacy preparation for migration; already-recorded runs remain readable and can
finish after activation. Spawner checks mode and matching enabled, unbound,
unexpired reservation before workspace/provider preparation; binding revalidates
current plan, authority, capabilities, readiness, holds and capacity atomically.
Native role-only start resumes an existing canonical run; it does not manufacture
ready intent or a new claim. New execution is admitted by the ready-bead worker.
The commander-execution-admission scenario covers the role matrix, actual mode
lock contention, preserved imported custody, capacity and pre-launch refusal.
This admission fence does not establish controlled legacy-host drain, native/raw
custody migration, activation readiness preflight or complete sidebar behavior.


### Claimed work uses the native execution host

The ready worker's real Spawner now performs the existing code-worktree or
knowledge-workspace preparation, then binds a native run and calls the daemon's
same chat Engine. It creates no tmux pane, container or second shell launcher
for claimed work. Fresh identities come from the claim, including knowledge
work; saved checkpoint requests retain their validated identity. The daemon
passes its Engine explicitly to the Spawner.

The run records execution_driver, execution_runtime, execution_account and
execution_model atomically with its capacity claim and bead association. Native
resolution uses that selection, rather than mutable role preference files or a
later usage observation. Live session/runtime resets are refused; provider
changes require a validated checkpoint and new claim. Off mode can adopt a
surviving bound native host but cannot start a fresh process.

The detached host owns first instruction delivery as part of process startup.
It journals an execution-start event before writing the instruction, so chat
replay preserves the first turn across daemon replacement without a separate
send-after-start gap. Existing imported/manual runs without the native driver
retain their prior attachment behavior. An execution result still ends the
physical process and creates the exact exit receipt described above.

If launch fails after database binding, the run and workspace are retained.
NativeExecutionRoles finds such startup intent directly in the database; the
daemon retries the same identity with a stored one-minute delay and five-attempt
budget. Holds and legitimate waits do not consume startup-failure retries.
Failures never become synthetic user decisions or fabricated terminal exits.

The native-worker scenario exercises the actual Worker, Spawner, PostgreSQL,
HTTP write interface, code worktree, detached native host and fake Claude process
for code and knowledge work. It proves committed-start recovery, same-PID daemon
adoption, one first-turn journal entry, immutable provider selection and physical
exit. A separate actual fake Codex host test covers native exit and fresh thread
identity. The actual Worker-to-Codex and checkpoint paths are now exercised below.
Release verification still includes legacy process attestation/drain,
verification of configured protections, plan revocation, activation preflight and sidebar behavior.

Redirecting a running agent goes through its bead. When a bead's plan revision
moves past the one its running native execution claimed, cmdrd sends one
message into that agent's session (re-read the bead and change course; stop
and report if the bead is closed or deferred) and records an
`execution-redirect` fact on the bead, so each revision is told once.

Accounts are generic Claude or Codex logins. store.AgentAccounts maps each agent
kind to an ordered account list, editable in Settings. Defaults prefer Claude
for Commanders and Codex for execution agents, with the other provider as fallback.
Subscription usage and configured limits must be preserved; this change does not
introduce a new per-agent dollar ceiling.


### Native checkpoint continuation

Checkpoint resume reads the source run's driver, session, provider, account and
model from PostgreSQL. The worker carries that binding into a new capacity
reservation and run; the binding transaction rejects a silent provider/account
change. Derived role session files and mutable provider preferences do not grant
or revoke native checkpoint authority. The question CLI also reads the actual
native provider and session thread from that run, so preference changes cannot
corrupt the saved answer's checkpoint. Nonblocking questions need no paused
conversation cursor.

Code checkpoints reuse the existing guarded worktree and branch, preserving
prepared files and commits. They never attempt to recreate or reset an already
checked-out checkpoint branch. Claude continuation requires its actual transcript
at both preparation and final engine launch. Codex continuation uses its exact
saved thread; a missing derived cursor can be restored from the recorded
checkpoint, but an existing different cursor is refused. A validated legacy
Codex checkpoint may promote that exact cursor into the immutable session path.
The bridge receives require-resume: missing, rejected or different-thread server
responses fail instead of falling back to thread/start. A restored cursor does
not itself prove the conversation exists; the provider must accept resume.

The native-checkpoint and native-worker scenarios cover actual Worker/Spawner/
PostgreSQL/HTTP/native-host/fake-provider flows for code and knowledge work on
Claude and Codex. They delete derived session/chat metadata/cursors, alter role
preferences, preserve a code commit and prepared files, and verify same-session
Claude --resume or exact-thread Codex thread/resume. Unit and CLI tests cover
missing conversations, conflicting cursors, rejected server resumes, provider
binding refusals and actual question-command checkpoint recording. These tests
do not establish cross-provider conversation translation or legacy process
attestation; those remain separate recovery/migration requirements.


Runtime presence and execution activity now use the same current-run evidence.
The daemon observes native role/run/session/PID identity, validates it against the
current bead claim, and records positive custody in PostgreSQL. A logical planning
chat, a newly inserted claim, stale role activity and an older session cannot
count as an executing agent. Evidence expires after 45 seconds; losing a local
engine connection leaves custody unknown rather than inventing process death.
A physical exit invalidates only its exact run/session. Both fleet/row counts and
nested frontend spinners use this verified execution set; unverified claims have
a separate checking state. Presence does not advance work progress.

The native-worker scenario now covers actual private Worker/Spawner/native-host
process evidence entering Source.Snapshot and disappearing on physical exit for
both code and knowledge tasks. Replacement-identity tests reject old observations
and exit receipts. Frontend tests reject stale nested working flags, planning
chat activity and wrong-project/non-run entries. New shared account ordering is
wired through PostgreSQL, settings, planning engine and fresh execution worker;
bound checkpoint continuations retain their recorded provider. Fresh executions
cannot escape a saved order to an unlisted account when its preferred provider is
unavailable or cannot support the task.

This is not cutover proof. The detached cockpit smoke renders an isolated empty
fleet; full populated PostgreSQL cockpit validation, old-process attestation and
retirement, run revocation/result delivery, migration rehearsal, release-wide
checks and the single authorized PR/install cutover still remain.


Native chat events now journal their captured immutable run ID. The status worker
consumes generic executions, including runtime permission/question/plan requests,
with the existing two-worker/token-reservation budget and normal account runner.
Classification validates current bead/run/session before updating its evidence
cursor, clears evidence across run changes, and validates the same identity before
publishing. Logical planning summaries require the current planning owner. The
UI accepts activity descriptions only for the matching observed run/session;
interpretation cannot turn a planning chat into an execution or advance progress.
Subscriptions retire after executions stop and are joined on worker shutdown.

The native-worker scenario drives this full path through the actual fake native
host and a deterministic classifier runner for code and knowledge work. Dedicated
store/UI tests cover late reused-session events, generic roles, old job results,
role-only observations and unchanged progress. Account-preference read failures
refuse a new planning launch rather than silently using default accounts.


Load verification now uses 1,000 synthetic native executions with explicit
bead/run bindings and run-fenced state updates, rather than legacy role-only
presence. It sustains the existing 100-updates/second workload while repeatedly
reading Source.Snapshot. A diagnosed recursive SQL plan rescanned project rows
for each ancestor edge. Current identity and the compact parent graph now come
from one read-only repeatable-read transaction, with ancestry resolved in linear
walks. Normalized agent-state masking uses a map, and read-only diagram/workflow
walks avoid temporary copies of the entire step tree.

The unchanged acceptance gate passed with 1,984 committed updates over 20 seconds,
124 reads, median 134.6 ms, p95 158.9 ms and maximum 179.9 ms on the private VM test
database. This verifies database/snapshot throughput with simulated processes;
it does not claim the VM can run 1,000 model processes. Optional test-only CPU
profiling and query-plan diagnostics are available for investigating regressions.


Native exit receipts now preserve the bounded terminal report together with the
physical run/session exit. A single database transaction closes the execution,
retains an attributed unaccepted exit note in bead history, and records a result
for the current planning Commander. Explicit attributed final-report comments
remain preferred evidence. Missing reports produce an explicit inspection
obligation. No exit report closes a bead or grants approval. Current blocking
questions instead preserve their checkpoint/resume source across answer/exit
ordering. Failed commits retain receipts for replay; duplicate exits cannot
replace the first terminal outcome or another run's custody.

The API projection reads current result/run/time facts in a batch and shows
“Result ready for Commander review” with zero execution spinners. Result waits
remain held work rather than stopped/overdue work; saved human answers and
planning checkpoints keep priority. Actual private Claude host/worker tests for
code and knowledge cover this projection, report recovery across a jurisdiction
transfer, history surviving a new work intent, and question continuations.

The populated detached PostgreSQL cockpit now renders the same canonical result
state, with no legacy Manager window. Source.Current supplies a load and its
canonical snapshot together; the live-data wire carries that frame for terminal
consumers. PostgreSQL terminal readers use this projection for scope status,
execution counts, spinners and fleet metrics. Database failures return an explicit
unavailable frame and clear the sidebar instead of retaining a healthy-looking
saved frame. Legacy SQLite fixtures retain their existing presentation behavior.
Dedicated tests cover wire transport, ignored stale Manager activity, verified
execution spinners and database failure after a successful read. This live check
and the private fake-host flows establish this interaction, not production
migration, retirement or cutover readiness.

## Cutover retirement and populated-state rehearsal

Obsolete planning identities are eligible for physical retirement only after an
explicit consolidation receipt, with no ownership in any project and no running
execution under the normalized runtime identity. A bounded database transaction
fences ownership and execution writes while the local Engine verifies and stops
the logical host. Unknown pre-host processes and execution-stamped hosts refuse
retirement. A durable receipt prevents repeated retirement; chat history and
session identifiers remain intact. Active-mode recovery performs this cleanup
without reintroducing permanent workstream Managers.

Legacy orphan cleanup independently preserves any recorded execution, including
raw imported runs, and refuses to kill on an uncertain custody read. Physical
host stamps also protect native executions when local metadata says stopped.

The explicit restored-state rehearsal accepts only a dedicated
commander_rehearsal_* database on the separate trial server at 127.0.0.1:55432.
It migrates an actual production backup, consolidates imported ownership and
activates orchestration without starting a model. It compares fingerprints of
all pre-existing columns in fifteen durable tables, preserving work, ordering,
dependencies, decisions, gates, execution history and usage. Production cutover
still requires full release checks and verification of the real installation.

Schema 3 is an additive upgrade for the shipped schema 2. Applied migrations
remain unchanged. The upgrade adds classifier run binding, runtime retirement
receipts and the shared ownership predicate, while preserving all existing bead
progress, claims, results, decisions and authority epochs. Closed/deleted
supervision history becomes resolved only when no saved human answer remains.
Connection probing and schema migration have separate bounded timeouts.

In active mode the board projects verified runs, saved decisions/results, real
external waits and recorded planning obligations. A missing permanent Manager
does not imply an error. A busy planning conversation contributes no execution
count. API, terminal and direct board formatting use the same observed run set.
Unknown custody remains a check; genuinely unassigned work remains a repair.

Consolidation resolves ownership from a single locked tree/boundary read with
memoized parent traversal, matching SQL semantics for deleted nodes, cycles and
missing parents. It does not run a recursive SQL ancestry scan for every bead.

Long-lived planning conversations receive the current orchestration protocol
through their existing prompt hook. No extra model turn or conversation restart
is needed. Execution-attributed or run-bound identities cannot borrow that
planning context. Missing brief/criteria errors name the existing guarded plan
repair; ordinary leaf edits use --description and --acceptance, then fresh
revisioned readiness. Holds and immutable workflow definitions remain guarded.


The sidebar names the responsible Commander on each root workstream. Nested
workstreams inherit that label unless they have an override, in which case the
override is named beside the nested title. The focus menu uses the same recorded
identity. Ownership labels never contribute to execution counts or spinners.
