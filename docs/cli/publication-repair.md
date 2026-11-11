# Publish an existing approval with redundant ancestor waits

The owning Manager, Commander or user can explicitly repair publication of the
same open user approval card. Operators are refused. This does not approve,
recreate or retire the gate, change its scope, or weaken `unlink`/`internal-dep`.

Use invented identifiers below in a registered project:

```sh
cmdr internal-gate publish --project sample --bead sample-approval \
  --parent sample-wrapper-d \
  --repair-ancestor-waits sample-track,sample-wrapper-a,sample-wrapper-b,sample-wrapper-c,sample-wrapper-d \
  --reason 'Publish existing decision; redundant ancestor waits' --dry-run
```

After reviewing the dry run, repeat the identical command without `--dry-run`.
JSON output includes `gate_bead`, `parent`, `dry_run` and the explicitly requested
`repair_ancestor_waits`. Dry run performs the same preflight and writes nothing.
`--repair-ancestor-waits` is available only for `publish`; it requires `--reason`.
The gate must already have exactly one `gate-of`, `verifier:user`, `approver:user`,
and an open status. The parent must be the guarded leaf's existing parent. Each
listed waiter must be a strict ancestor of that leaf, excluding the workstream
root. Its edge to the gate must be exclusively a single `blocks` edge. A live
track is eligible only through that ancestry check; labels never waive checks.
The guarded leaf's `blocks` edge is mandatory and is never removed.

A wait is redundant only if nothing else depends on it. The command refuses, before
any write, when a listed waiter has a live descendant, other than the gate, the leaf
and the leaf's chain, that neither has its own `blocks` edge to the gate nor sits
under a live, unlisted bead with one. Live means not closed, so parked and
superseded-but-open work counts. What sits under the gate is exempt: the waiter's
wait never held it. The refusal names the bead and the waiter. To go on, give that
bead its own wait on the gate, or finish it; to leave the wait in place, do not use
this command. A still-proposed gate is not refused outright: this rule keeps the
repair from releasing work before Commander has reviewed the proposal. A wait
removed from a bead does not change what waits on that bead, which stays blocked
while it is open.

Before any mutation, the command freshly checks authority, ancestry, holds and
all resulting project dependencies (`blocks` and `parent-child`), including every
preserved gate wait and the proposed publication parent. A missing endpoint,
ambiguous edge, graph with 100,000 or more listed nodes, unreadable evidence or any
remaining cycle refuses. Unrelated existing cycles also refuse. This whole-project
read is uncached, intentionally conservative and runs once per invocation.

A `gate-publication-repair@v1` comment records the gate, parent, leaf, root, caller,
reason and sorted explicit waiter list **before** removing anything. A matching
intent is required to accept a missing edge on retry. Before each later write a
narrow fresh read of only the gate, leaf, chain and listed waiters checks authority,
ancestry, holds, edge shape and the durable intent; its cost does not grow with the
project. It does not see new work added elsewhere during the repair (see the
concurrency limit below); a repeat judges the whole graph again. Publication sets only the gate's
parent; a completion comment records the end. Original card text, labels,
approvers, status, gate-of, other dependencies and earlier comments remain.
`internal-comment` reserves this audit prefix for this command.

On any read/write/audit failure, stop and inspect the error. Repeat the exact
request as the same caller to finish a partial repair, including writes with a
lost response or a failed completion comment. An intent-write failure makes no
edge writes. An intent is permission to reconcile that exact plan, not proof
that removal completed; the fresh edges are authoritative. No rollback recreates
waits or replaces the original card. This is not a cross-process transaction or
a reservation against concurrent external writers; arrange exclusive graph
editing during the repair. Direct database access or raw beads writes can still
bypass these CLI checks. Normal `publish` without this flag is unchanged, except
that a cycle refusal names this flag. Its dry run does not predict bd's cycle
refusal, which comes only from the parent write.

Coverage: `TestPublicationRepair*`, `TestPublishCycleRefusalNamesTheRepair`,
`TestGatePublicationRepairCommand`, `TestPublicationRepairAuditCannotBeForged`,
`tests/evals/scenarios/publication-repair.json` (`TestPublicationRepairScenario`),
and on real bd (`COMMANDER_BD_INTEGRATION=1`, private temp server)
`TestPublicationRepairFiveWrappers_BDIntegration`: the built cmdr as the owning
Manager, every write through a real door handler with its checks, answers lost after
bd applied a removal and the parent write, exact repeats.
`TestPublicationRepairScale_BDIntegration` (`COMMANDER_REPAIR_SCALE_BEADS=N`) times one repair.
All fixtures are synthetic; the command must be separately reviewed before use
on a live project.

On PostgreSQL, publication and repair delegate once to authenticated
`/api/gate/publish`. The registered planning Commander must own the gate, its placement and
protected scope, or have an exact typed publication proposal agreed by every
affected owner; the user retains the interactive door. The
CLI supplies fresh bead and jurisdiction revisions. The service locks the
project, validates those versions and leaf coverage, and commits the original
audit, exact redundant edge removals, placement and completion audit together.
A late failure rolls all of them back. Unapplied saved human answers refuse
placement changes until their recorded recovery is handled. The action does
not approve or close the gate. A missing service never falls back to local
separate writes. Dry run writes no facts or receipt; exact committed requests
replay their original receipt without repeating mutations or progress.

The repair grant exists only inside the validated server transaction and permits
its exact edges, placement and audit. Raw comments, environment flags and
unrelated plan approvals cannot grant it. Removing a different relationship type
cannot delete a blocking edge, and parent relationships use the guarded parent
operation. For cross-owner changes, use a plan JSON object containing `dir`,
`basis`, and a `publication` object with `gate`, `parent`, `waiters`, and `reason`.
Do not mix it with `ops` or holding intent. Run `cmdr plan propose --file PATH`,
inspect it with `cmdr plan get --id ID`, record each owner's exact answer, then
`cmdr plan apply --proposal ID`. No owner agreement passes the human gate. Changed
payloads, requester, ownership or versions refuse. An ordinary plan grant cannot
authorize a publication action, and publication agreement cannot authorize raw
operations. See the jurisdiction architecture document for a JSON example.
Private PostgreSQL/HTTP and typed CLI coverage lives in
`TestPostgresPlanPublicationRepairIsAtomicAndRetainsApproval`,
`TestPostgresPlanPublicationRepairRefusesUnsafeOrForeignState`, and
`TestPostgresPlanPublicationCLIUsesCanonicalServiceOnce`, the publication
coordination API tests, and
`TestPostgresPlanPublicationCLISavedProposalUsesTypedEndpoint`.
