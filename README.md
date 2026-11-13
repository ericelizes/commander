# Commander

Commander is an app for planning and running work with AI agents across your
projects. You message Commander, and together you decide what the workstreams
are and what each workflow looks like: its steps, checks and decision points.
Commander keeps that plan on one priority-ordered board. A shared scheduler
runs execution agents using Codex or Claude on steps that are ready.

## Work with Commander

Start with the outcome you want. Commander proposes workstreams, breaks them
into steps, identifies dependencies and checks, and marks the points that need
your decision. Adjust the plan in chat before work starts, or as you learn.

For example:

1. **Ask for an outcome:** “Let customers export their invoices as CSV.”
2. **Shape the workflow:** Commander proposes an export workstream: agree on
   fields → implement → test → review a PR → merge → verify the result.
   Each step has a check; scope approval and merge are marked as your decisions.
   If the work calls for a separate outcome, Commander proposes another
   workstream and shows how they depend on each other.
3. **Adjust it in chat:** “Include refunds. Show me a sample CSV before you
   implement it, and keep rollout separate.” Commander updates the steps,
   checks and decision points with you.
4. **Let agents run:** Ready steps go to execution agents. They do the work,
   run checks and bring back evidence. Dependent steps wait for their inputs
   and required approvals.
5. **Decide at the marked points:** Review the sample, approve the next stage,
   and merge the PR when it is ready. Production changes wait for your approval.
6. **Confirm done:** Inspect the result and click **Confirm done**. This is
   always your last click; an agent finishing does not complete the workstream.

Keep steering through the same chat: “Move this ahead of the export work,”
“Change the acceptance check,” or “Pause this workstream.” Commander updates
the plan and coordinates stopping or redirecting execution. You can ask what is
running, what is blocked and what happens next.

### Read the board and answer decisions

The sidebar is your priority list. Numbered rows are workstreams; indented rows
show nested outcomes. Each row has a **% done** progress indicator and a status
line explaining the current activity or wait. Progress reflects delivery
milestones, not time spent; read the status line to see what needs to happen
next. Paused work stays visible.

**Needs you** cards collect decisions and questions. Read what the card asks,
the evidence and what approving it unlocks: approval allows the named next
stage to proceed once its other prerequisites are met. It does not approve the
whole workstream. **Confirm done** is the final user decision after the work
and required checks are complete.

Tell Commander a **standing decision** once—such as “Always show me a preview
before publishing”—and it holds for later work within its scope. You can revise
it in chat without repeating it for each agent or step.

### Review results and ship

For code work, agents open PRs, run checks, handle CI failures and address review
comments. They bring the PR back when it is ready; **you merge**. Production
changes and outward messages always wait for your approval.

Open a workstream to inspect its workflow and results. Reports appear as
workstream tabs, so findings and evidence stay alongside the work they explain.
Ask Commander to clarify a report or change the next step.

## Set up

Commander has a background service, `cmdrd`, and a desktop app. It supports
macOS and Linux; a headless Linux machine can serve the web app.

1. Install Go 1.25+, Node.js with npm, Git, tmux, jq and the agent CLI you intend to
   use (Codex or Claude), and sign in to that CLI. The installer checks other
   prerequisites and sets up Agent Mail, which is required for mail and adding
   projects.
2. From this checkout, run:

   ```bash
   bash install/install.sh
   cmdr app install
   cmdr app
   ```

   Ensure `~/.local/bin` is on your PATH. On a headless machine, use
   `cmdr app --web` to print the web app link.
3. Message Commander to add your project and describe the outcome you want.
   Include its local path when needed.

## Reference

| Command | Purpose |
| --- | --- |
| `cmdr app` | Open the app and start the service if needed. |
| `cmdr app --web` | Print the web app link. |
| `cmdr --help` | Inspect the installed CLI's commands and flags. |

- [Contributor instructions](CLAUDE.md): builds, tests and isolated cockpit verification.
- [Orchestration architecture](docs/architecture/commander-jurisdictions.md): planning ownership, shared scheduling and execution.
- [PostgreSQL live state](docs/architecture/postgres-live-state.md): state architecture and migration rules.
