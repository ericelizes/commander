# Scoped PostgreSQL review bootstrap

`cmdr workflow own-run --project PROJECT --project-dir /absolute/root --task TASK`
reads only the current caller's run. It requires immutable launcher environment
`COMMANDER_RUN_ID`, `COMMANDER_ROLE`, `COMMANDER_RUN_SESSION` and the current
working directory matching the recorded worktree. It returns the actual stored
actor spelling, session, task, run ID, registered project/root and worktree only.
Dots/dashes in launcher role names retain existing canonical-role compatibility.

The Store's normal `state.postgres` selector is mandatory. Missing selectors,
unavailable/unsupported schemas, retired legacy state, missing location data,
replaced claims, terminal runs/tasks and identity mismatches fail closed. This
reader never migrates a schema, and every connection has PostgreSQL read-only
transactions enabled. One SELECT joins `active_run_id` with task, run and project;
there is no newest-run lookup. Identity checks are a point-in-time snapshot,
not a lease for future execution. This retains the local launcher/role trust
boundary; it cannot authenticate against a malicious process sharing the UID.
Driver/connection diagnostics are omitted to avoid credential disclosure.

Capture the canonical task before sealing (using the registered root, never
worktree-local stale beads), and save its exact description bytes as the original
prompt. Canonical JSON is an object; historical single-element arrays can be
unwrapped explicitly. Keep dispatch/context bytes in different files:

```sh
cmdr bead --project-dir /absolute/root show TASK > original-payload.json
cmdr workflow own-run --project PROJECT --project-dir /absolute/root --task TASK > own-run.json
cmdr workflow review-exec --repo /absolute/worktree --base BASE_SHA \
  --task TASK --run-id ACTUAL_RUN_ID --actor ACTUAL_STORED_ACTOR \
  --prompt-file /private/original-prompt.txt \
  --context-file /private/dispatch.txt --context-file /private/own-run.json \
  --private-git --run-dir /private/fresh-run --timeout 30s -- /bin/true
```

`--context-file` is repeatable. Each regular file (at most 8 MiB) is copied to
`context/NNNN.txt`, with its own SHA256 in manifest `context[]`. The original
prompt remains independently bound by `prompt_sha256` and copied to `prompt.txt`.
The manifest binds all artifacts to task/run/actor, current head, base, scope,
command and policy. `--private-git` seals object-store alternates, environment and
scratch paths, directing transient Git objects into the fresh run directory.
It does not change sandbox permissions or grant write authority. It reads shared
Git objects; it is not a sandbox against arbitrary child writes.

A successful finite controller command establishes execution only. A controller
report must say source not assessed, coverage partial, INCOMPLETE. It must not
claim a source verdict. Save a separate standalone receipt with exactly
`workflow-review-result@v1 ` followed by JSON (actual manifest task/run/actor,
head/base/scope/manifest hash/depth, execution outcome, source `not-assessed`,
coverage `partial`, reason and empty findings). Then run:

```sh
cmdr workflow review-report-check --head HEAD_SHA \
  --file /private/controller-report.md --receipt /private/controller-receipt.txt
```

A nonempty separate receipt takes precedence over embedded report text. The
checker returns `verdict: incomplete`; controller acceptance cannot satisfy a
source review or a gate. The existing registry verifies manifest and terminal
facts independently of PostgreSQL; `review-exec` does not itself refresh the
live claim, so capture `own-run` immediately before launching. Original dispatch
context is retained evidence, never current action authority.

Coverage: `TestOwnRunClaimFences`, `TestWorkflowOwnRunRefusesMissingLauncherAndLegacy`,
`TestOwnRunPostgresReadOnly` (disposable `commander_trial` only), eval scenarios
`own-run-bootstrap.json` and `review-runtime-context.json`, plus isolated cockpit
verification. No production schema or service mutation is needed.
