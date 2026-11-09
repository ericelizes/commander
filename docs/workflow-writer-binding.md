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
   `tasks`. The user's own CLI, Commander (external commits only, never task
   writers; the task writers already bound for the PR are kept) or the owning Manager may bind an external writer; Operators and
   other Managers never. Changing the commits, the tasks or the head gives a new
   revision, as above.

   **An Operator's commit is never the user's.** Before it records anything,
   the bind reads the project's local repo (`repo_path`, else the project dir)
   against every Operator run of the project in `agent_runs` and refuses a
   commit that run wrote: made by an entry of its branch's reflog
   (`op/<name>` or its worktree's branch) or its worktree's HEAD reflog that
   wrote commits (a commit, non-fast-forward merge or pull, cherry-pick,
   revert, `am`, rebase picks, a finished rebase's own commits), never one that
   only moved the branch (created from, reset, checkout, any fast-forward), so
   a reviewer that reset onto the PR head writes nothing; a run branch with no
   reflog or an archived `refs/archive/op/<name>-<sha>` tip counts all of its
   history (nothing tells there, so it fails closed); or
   carrying an author/committer identity only Operator runs used (seen in their
   reflogs, never in the main checkout's reflog or its `user.email`; Operators
   normally commit as the user, so this only bites when their identity
   differs). The refusal names the run and says to bind its task with
   `--writers`. A fix Operator branched off, reset or fast-forwarded onto the
   user's commits does not claim them. A git error refuses. Reviewer independence applies the same reading to
   the review task's own runs: a reviewer that wrote a bound external commit is
   refused. Limits: a commit missing from the local repo (made on another
   machine) is on no local branch and passes, and so does an Operator commit
   whose branch, worktree and reflogs were all deleted; this is a speed bump
   against a mistaken or careless bind, not a proof of authorship.

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
