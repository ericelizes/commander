# Parent-only placement of ordinary steps and approval cards

`cmdr internal-place --ordinary --project P --bead B --parent W --dry-run`
validates a parent-only placement. Repeat without `--dry-run` to apply it.
The JSON identifies the actual previous parent, destination and dry-run status.
This mode never uses the workstream conversion, wrapper or Manager settlement
paths. It does not rank siblings.

Only an owning Manager, Commander or the user may run it. Both source and target
must retain the same fresh strict owner. The source must already have a parent;
workstreams, operational beads and internal Operator tasks (`kind:task` or
`spawn-attempt:`) are excluded. The destination must be an existing live
workstream with compatible ordinary children. Closed, tombstoned and superseded
sources/destinations refuse. The underlying Beads issue type may be `task`;
`kind:task` specifically denotes Commander's internal Operator task.

Checks use fresh ownership, placement and parent/blocking-edge evidence. A
proposed cycle refuses; the reachable dependency walk is bounded to 128 beads.
Custody uses the existing 60-bead closure reader and refuses unreadable/oversize
closures or any newly unheld work, including an emptied old parent. It writes
no custody labels to make a move pass. An existing parent with a genuine holder
retains that holder. The move does not close or approve a gate, publish a card,
change gate-of edges, remove blocking waits, or rewrite run provenance, card
versions, answer journals, labels, description or acceptance criteria.

The only mutation is the ordinary `SetParent` through the existing write door.
There is no backend compare-and-swap between validation and the write; concurrent
changes can invalidate the checks. Quiesce competing placement edits, run each
move separately, and inspect fresh source/card/edge state after each. A failed
or lost response can mean the parent landed: read before retrying. A retry to
the same parent validates again and performs no additional write. This command
is not an ownership-transfer control and never creates/starts/removes Managers.

Coverage: `TestOrdinaryPlacePreservesApproval`, `TestOrdinaryPlaceRefuses` and
private-handler eval `TestOrdinaryPlaceScenario`, driven by
`tests/evals/scenarios/ordinary-place.json`. The scenario moves an ordinary step
and its unchanged approval gate out of an operational parent, preserving the
step's blocking edge. Live cockpit proof uses only synthetic fixtures.
