# Publication repair verification example

This invented exercise uses a catalog workflow with five wrapper ancestors and
one guarded leaf. It illustrates the expected evidence for a repair review; it
is not a historical run report or approval to repair a live project.

The Manager first verifies the current gate labels, ownership, ancestry and
exact waiter set. A proposed gate remains proposed: publishing it alone grants
nothing, and repair preserves its labels. Plain publication may encounter a
cycle when an ancestor already waits on the gate; unlinking only superseded
waiters does not repair that shape.

## Automated coverage to run

Run the focused packages through the shared build slot:

```sh
cmdr internal-slot run --timeout 5m -- env -u TMUX GOMAXPROCS=2 \
  go test -p 2 ./internal/gates ./cmd/cmdr ./tests/evals \
  -run 'TestPublication|TestGatePublicationRepairCommand' -count=1
```

Coverage must preserve the original card, protected leaf, text, status,
approvers and history. It must refuse foreign Managers and Operators, wrong
waiters, holds, missing endpoints, gate-chain cycles, duplicate gate-of edges
and sibling releases. Failed intent, interrupted writes, audit failure,
multiple-ancestor partial failure, lost fresh reads and leaf movement must
leave a conservative, retryable result. The five-wrapper eval is
`tests/evals/scenarios/publication-repair.json`.

## Invented detached cockpit exercise

Use a synthetic process-backed beads fixture and the isolated detached cockpit.
Capture it with `scripts/cockpit-test.sh text`. Expected assertions, not actual
execution evidence:

```text
EXPECT dry-run: zero writes
EXPECT apply + exact retry: ancestor waits removed, leaf wait kept, parent set
EXPECT original card and protected leaf unchanged
EXPECT foreign Manager refused
EXPECT sibling waiter refused
```

Tear down the private cockpit afterward. Run the full suite once through
`cmdr internal-slot run -- env -u TMUX go test -p 2 ./...` and record its actual
exit code separately. A failed host-adoption check or capacity refusal remains
a failed check; focused passes do not clear it. Additional source changes
require evidence for the resulting head.

## Invented scale comparison

Imagine 800 unrelated beads surrounding the five-wrapper workflow. A shared
project read should avoid repeating a full board scan for each wrapper.
`TestPublicationRepairReadsTheProjectOnce` checks the bounded read pattern;
`TestPublicationRepairScale_BDIntegration` can exercise a real bd against a
synthetic project. Record actual durations and read counts when these run;
no timing estimate here is a measurement or a promise for a live board with
comments and edges.

See [publication-repair.md](publication-repair.md) for the repair route and its
conservative graph and concurrency limits. This example authorizes no live
writes, installation, restart, shipping or publication.
