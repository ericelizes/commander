# Delivery throughput contracts

Review quality, execution availability, coverage and delivery progress are separate
facts. A failed provider call proves no source defect and no successful review.

## Scope and review depth

`cmdr workflow review-policy --repo PATH --base BASE [--head HEAD] [--full]`
reads committed Git scope, resolves both SHAs and emits `scope_hash`. Sensitive
domains, binary and broad changes require full coverage even with few lines.
The numeric-only CI budget exception does not exempt credential, permission or
execution changes. The classifier is conservative pattern matching, not semantic
proof. Explicit user full-review requests remain binding.

Use `ce-code-review mode:agent depth:full` for the full floor, otherwise use
`mode:agent depth:auto`; the installed skill decides its lite/focused/full path.
`depth:focused` is not a valid invocation token. Do not translate the recommendation
into an unsupported argument or add bespoke timing rules. Independent adversarial
work is one bounded challenge, not a second primary roster.

## Original execution and receipts

`cmdr workflow review-exec --repo PATH --base BASE --task TASK --run-id ID
--actor operator-ROLE --prompt-file FILE --run-dir NEW_DIR -- COMMAND ARGS...`
creates private original scope, prompt, task and actor records. It writes the
actual child PID before releasing a launch gate. Prompt and scope hashes are
sealed before reviewer work; output and terminal result are recorded afterward.
The default execution deadline is ten minutes, maximum thirty. Linux execution
owns a bounded transient systemd scope and stops it on completion or timeout,
including setsid descendants. Other Unix hosts own the launched process group
only; detached reviewer work is unsupported there.
The wrapper does not infer source quality from exit zero. Files are private and
read-only after completion, not cryptographically protected from the same OS user.

Post the human report and a separate single-line `workflow-review-result@v1`
JSON comment. Required fields are task, actual task-bound run_id/actor, full
head/base, scope_hash, manifest_hash, depth, execution, source, coverage and
findings. Blocked/failed executions require reason and partial coverage. Clear
source has no findings; unassessed source cannot claim complete coverage.
Only complete execution, complete coverage and no blocker/major findings satisfy
a review. Retain minor suggestions without inventing a fix commit; a typed
minor-only proof may use `no_fix_required:true` instead of claiming no findings.
Minors get at most one fix pass (`one-minor-fix-pass@v1`): a recheck reviews only
the fix delta (receipt base = the prior reviewed head, proof `prior_review` and
`prior_head`; the prior is a full review, never another delta, and the receipt's
`resolved` lists every prior blocker/major the recheck verified fixed), a review
that read only a delta never stands as a plain proof, and every minor left on the
passing head is filed by the close door
as a Manager-held follow-up bead instead of another review round. A fix delta the
verifier measures on GitHub as tiny documentation (at most 3 allow-listed prose
files, docs/** or markup outside dot/agent directories, and 20 lines, no agent or
build files, nothing sensitive) renews the earlier typed review with
`delta_renewal:true` instead of a new review. Reviewers run
`cmdr workflow review-report-check` before posting, so format errors never cost a
review.
The verifier resolves task/run/actor from stored run provenance, not owner labels.
The executor registers the original manifest under Commander home's
`review-executions/<run-id>/<manifest-hash>.json`, before releasing the child.
The verifier reads this fixed registry (never arbitrary paths from comments),
checks its content digest, head/base/scope/actor and full-depth floor, and requires
a real successful terminal execution for a complete receipt. Inventing a hash
or lowering a required full depth cannot satisfy it. These local records remain
mutable by the same OS user; they do not attest model reasoning.

Earlier typed execution failures do not poison an unchanged head. A later real
completion can supply missing coverage. Earlier actual source findings at that
head still block; ambiguous legacy NOT CLEAN is never automatically reinterpreted
or rewritten. Keep valid baseline coverage when reviewing a delta and explain the
scope map; current-head closure still requires truthful current-head proof.
One recovery per cause is the workflow contract; repeated failure escalates one
named owner/action rather than recursively creating review rosters.

## Runtime and routing

Linux tests leave RLIMIT_NPROC unchanged and run under a verified cgroup v2 PID
cap at most256. A finite inherited cgroup bound is accepted; otherwise the harness
creates a unique bounded systemd user scope. Runners need a user service manager
(`XDG_RUNTIME_DIR=/run/user/$(id -u)` in non-login SSH) or an existing dedicated
cgroup bound. Missing isolation fails with an actionable error; it never falls
back to an unrestricted workload. Cleanup stops only its newly created scope.
macOS retains its reserve-based guard. Old Linux service units with LimitNPROC
need the supported unit refresh, not a host limit increase.

Wake delivery validates real workstream registration; failure cannot fall through
to an unchecked direct chat start. Invalid/nested/terminal-owned routes retain
unread mail and report remediation. Retries back off, rather than creating a
high-frequency wake loop or transferring terminal ownership without authority.

## Writes, progress and delivery

New CLI write requests set `no_wait:true`. The response acknowledges the durable
backend operation and reports `projection_pending` while a bounded coalesced
snapshot refresh continues. Old clients retain synchronous projection semantics.
`backend_ms` and `projection_ms` make the two costs visible. An acknowledged
mutation must not be repeated merely because its display is still refreshing.

Delivery milestones exclude internal Operator attempts; leaf task counts remain
separate diagnostic data. Neither is an estimate of remaining effort or time.
Manager activity without changed stage/gate evidence has its own stall signal.
Check rollout topology, access owner, code pin and restorable rollback inputs at
workflow start. Preserve the user's scoped approvals instead of asking repeatedly
or treating a new administrative task as new authorization.

## Regression verification

Run the Go suite once per integrated batch and affected packages after fixes.
Key cases: seven-line CI delta versus small auth/data-loss change; quota recovery
versus unresolved source findings; real task/run identity; manifest before child
execution; deadline cleanup; Linux ordinary-child and kernel PID-cap saturation;
safe quoted guard inputs versus actual mutations; unread mail across backoff;
durable acknowledgement with a deliberately blocked projection.

Use isolated `scripts/cockpit-test.sh` with fake runtimes for live rendering.
Never start a real Manager or hand over a live conversation as a test.
