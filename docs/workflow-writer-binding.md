# Explicit writers for PR review

Use the standalone binary built from the implementation branch. This does not
require installing Commander or restarting cmdrd. The owning Manager runs these
commands; an Operator must hand the recipe back to that Manager.

1. Run the existing `workflow plug pr-review --project P --parent B --before M --pr URL`
   command with the same parameters. Its JSON `container` is the existing review
   block R, and `remaining` lists the steps. An idempotent plug preserves bindings.
2. Read the original implementation and actual fix writers' commit reports.
   Select all writer task IDs relevant to this PR diff. Review task IDs are not
   writers merely because they live under the review block. No graph scan can
   replace this Manager selection.
3. Read the current full SHA with `gh pr view URL --json headRefOid --jq .headRefOid`.
   Bind the selection (include every original/fix writer, comma-separated):

   ```sh
   /absolute/path/to/cmdr workflow bind-writers --project P --block R --writers original-task,fix-task --head FULL_SHA
   ```

   **Commits from the user's own sessions.** A PR the user wrote outside any
   task (and handed over for readiness) has no writer task. Bind those commits
   as an external writer, by range or by a comma list of full SHAs, adding
   `--writers` only for actual fix tasks Operators made since:

   ```sh
   /absolute/path/to/cmdr workflow bind-writers --project P --block R --external-writer user --commits BASE_SHA..LAST_USER_SHA [--writers fix-task] --head FULL_SHA
   ```

   `BASE_SHA..LAST_USER_SHA` (two dots) is resolved through GitHub's compare
   API to the commits reachable from the last one and not from the base; it must
   be ahead of its base and GitHub's list must hold its full count. Every commit
   must belong to the PR at the current head. The binding records
   `"external":{"author":"user","commits":[...]}` (sorted full SHAs) beside
   `tasks`. The user is never an Operator, so reviewer independence is judged
   against the bound tasks only. The user's own CLI, Commander or the owning
   Manager may bind an external writer; Operators and other Managers never.
   Changing the commits, the tasks or the head gives a new revision, as above.

4. Renew each step in template order. Existing closed steps use the same close
   command and stay closed. For every review/fix step, reference a closed separate
   review task with a CLEAN report for the current full SHA:

   ```sh
   /absolute/path/to/cmdr internal-comment --project P --bead S --text 'workflow-review-proof@v1 {"review_bead":"separate-review-task","head":"FULL_SHA","fix_commits":["FULL_FIX_SHA"]}'
   /absolute/path/to/cmdr internal-close-objective --project P --bead S
   ```

   Use `"no_findings":true,"fix_commits":[]` only when that review found nothing.
   Include fixes from earlier rounds. Primary and adversarial reviewers must have
   no overlapping actual Operator roles; neither may overlap any writer role.
   The final check goes last, followed by the block close if still open.

Every task needs readable, exact-task run records in the same project and fresh
parent ancestry in the block's workstream. Shared Manager owner labels do not
matter. Missing data refuses completion. If a push changes the head, confirm the
writer selection again, rebind to the new full SHA, and renew all checks. Changing
writers at the same head also invalidates old checks; reverting a selection does
not restore them. Graph check prints the binding command for legacy unbound blocks.
