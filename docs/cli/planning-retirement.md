# Planning retirement

The current planning Commander can retire obsolete work without inventing a
passing check or binding writers to an abandoned review. An execution run cannot
use this authority.

```
cmdr internal-gate retire-legacy --project P --bead STAGE --reason 'Superseded local delivery'
cmdr workflow retire-block --project P --block REVIEW_BLOCK --reason 'Withdrawn PR review'
```

Without `--replacement`, `retire-legacy` retires the ordinary stage and every
attached automatic gate, including a failed merged gate on a deferred stage.
`--dry-run` validates that scope without writing. With `--replacement`, the
existing marker-repair operation retains its genuine open user approval card;
the stage may now be deferred.

`workflow retire-block` retires the entire plugged pr-review block and its steps
in one PostgreSQL transaction. Already closed steps keep their original evidence.
Open steps receive a superseded label, an attributed retirement comment and a
superseded close reason. Definitions, predecessor edges, failed outcomes and
prior comments remain intact. Retirement creates no review proof or writer
binding, and does not satisfy a downstream review check.

The service reads and validates the whole retirement scope under the graph lock.
Human gates/cards, holds, live executions, workstreams, Confirm done, foreign
planning jurisdictions and unexpected unfinished children refuse the operation.
These commands require PostgreSQL and have no legacy write fallback.

Plan batches can use `retire ID --mode legacy-stage|workflow-block --reason R` as
an atomic write operation, with their usual planning revision and receipt checks.

Cross-area steps may sit beside nested workstreams. Confirm done does not prevent
a workstream split. Deferred work remains unfinished in progress denominators and
blocks dependent approvals, including Confirm done.

Coverage: `TestPostgresPlanningRetires*`, `TestPostgresRetirement*`,
`TestPostgresWorkflowRetirement*`,
`TestPlanningRetirementCLI`, `TestRetireLegacyParkedStageRetainsReplacement`,
`TestCheckPlacementConfirmDoneAndCrossArea`,
`TestPausedWorkIsUnfinishedForProgressAndConfirmDone`, and the real guarded-service
scenario `TestPostgresPlanningRetirementScenario` driven by
`tests/evals/scenarios/planning-retirement.json`.
