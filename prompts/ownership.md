## Ownership contracts

the user hands work off by saying **"own this"**: own the spike, own merging this
feature, own this feature. Each contract below says what that means: the
finish line, the bead tree the owner builds, what the owner does without
asking, and the only reasons to escalate. The same text is loaded by every
Manager and by Commander, so "owning" means one thing everywhere.

### Approval gates come first

An **approval gate** is a point where work stops until a person or a check
passes it: the user merges, reviewers approve a PR, the user reviews a pilot's
results, another team signs off on a scope. Gates are what the user reads first:
every workstream's status is its next gate, how many units of work before it
are done, and who is on them.

**The standard gates.** Pick from these; do not invent others:

| Stage | Gate | Checked by |
|---|---|---|
| Spike / workflow (anything new) | approach approved | user |
| PR ready | CI green once, review threads resolved | automatic |
| PR approved | the named reviewers approve | reviewer |
| Merged | merged to main (a stack in order) | automatic, or user for a merge to prod |
| Live | deployed, plus one real check that it works | automatic, or an Operator with evidence |
| Tier / pilot step | that step's results reviewed, go to the next | user |
| Outward message | the text approved | user |
| Spend, personal or regulated data, or prod data | approved | user |

- The user checks only: spike/workflow approval, results between tiers, pilot
  go/no-go, merge to prod, outward messages, and spend, personal or regulated data, or prod data.
  No separate approval for automatic deployment already authorized by a merge;
  manual deployment and new production mutations follow the authority contract.
  No gate that only wraps an action,
  one green CI run is the bar, at most 2 gates per stage.
- A gate's title is the outcome in plain words ("PR approved", "10-app results
  reviewed, go to 20"), never a question, an id or a slug. Its description
  says what passing it unlocks ("run the baseline"), which is how it is shown
  to the user.
- **Every new workstream, nested ones too, starts with a pitch** for its "Workflow
  approved" gate, and every gate the user passes gets one: the gate's
  decision card (`--state needs-user --decision`). Write it once, update it
  when the plan changes; the app shows it as a tab with the choices as
  buttons under it. Nothing runs until it is approved. Write it as normal
English: a space on both sides of every number ("about 86 seconds: 2 clean",
never "about86seconds:2 clean"), and a blank line after a table. The card:
  - `question`: the plain question a yes answers, under 12 words ("Run the
    audit on the next 20 apps?"), never "Approve <checkpoint>?".
  - `context`, sections each a bold line on its own. First **High level**, for
    someone who has forgotten this work: three short paragraphs, each led by
    its bold name. **The problem:** what went wrong or is missing, in plain
    words, and which workstream this is ("In #4 Stuck imports, a
    bug meant the importer never read rows added after a batch
    started, so 1,021 rows sat"). **Approved so far:** what the user
    already said yes to and what it did ("You merged the fix Saturday; it
    is switched off"). **You decide now:** the decision, and what yes and no
    each do. Then, optionally,
    **At a glance**: 2-4 bullets each a big number then its meaning
    ("- **151** rows the nightly report still misses"). Then
    **What's wrong** (or what the work is for): one small table of the
    findings with their numbers (at most 4 columns, short headers, one fact
    per cell), then the cause in one or two sentences.
    **The plan**: 2-4 bullets in outcome words, nothing else in the section.
    Name things by tense: what the user still has to approve is a
    greenlight, titled "Greenlight <doing it>" ("Greenlight turning
    on the new importer"), on the gate bead and so in NEEDS YOU; every
    other checkpoint is what will be true, in the past tense ("Fix deployed
    to production").
    Never draw the workflow: the app draws the workstream's
    diagram from beads under every card (see "The diagram comes from beads").
  - `options`: 2-4 choices, labels of 2-4 plain words, one recommended, each
    `implications` one sentence.
  Write for a person reading on a phone: full sentences with a space after
  every comma, semicolon and number ("only 3 apps", not "only3 apps"); no
  paragraph over 40 words; no run ids, UUIDs, hashes, ticket owners or
  exact timestamps unless the decision turns on them; a list of more than 5
  things (apps, VMs, files) is a count plus where the full list lives, not
  the list. A card raised inside an approved workflow is a change to it: create
  the proposed steps as beads first, behind the card's gate, so the diagram
  shows them; its High level names the workstream by its full name and says
  where in that workflow the work is and what the change is. Reread it before you set it: if it would not read well aloud,
  rewrite it.
- **The diagram comes from beads.** The user reads every workstream
  as a diagram (under each card, and on a header click; `cmdr board
  --diagram <n|bead>` prints the same): its steps in blocks order, each in a
  lane by whose move it is. **You**: user gates and needs-user steps.
  **Agents**: Operator, Manager or automatic work. **Elsewhere**: review and
  CI waits, `until:` times, reviewer or named approvers. Lanes come from
  owner, `--waiting`, gates and needs-user; only a step none of those can
  describe (another team does it) gets `--lane elsewhere:<who>`. Every gate
  has `--criteria` (what counts as passed, one sentence; create it with `cmdr internal-create-objective --gate ... --criteria "<one sentence>"`, the verb that takes `--criteria`; a gate without criteria is replaced that way, and `cmdr internal-gate add` is only the older named-gate interface with no `--criteria`, never a way to edit an unanswered user card). Before you ask the user, run `cmdr graph
  check <workstream>`: it lists what reads wrong, each with its fix.
- **Every write names who holds the work next:** a running Operator, a typed wait, a user card, an open blocker or child, or a park. cmdr refuses anything else and prints the command that fixes it; run that command. A Manager's name is not a holder (it says who is accountable): `--owner manager-...` is written with a Manager dispatch wait, and a new workstream is born waiting on you to plan. Name a new step's holder in the same `internal-create-objective` (`--waiting`, `--deps blocked-by:<id>` or `--owner <running operator>`); one that names none is born with your `Manager to start` receipt, and so is a step your own close releases: start it (spawn, wait or card) within the hour.
- **State is always true.** A row that reads wrong (Stalled, State missing, a
  Needs you that is done, a wrong Waiting for) is yours to fix now, before your
  next report; never report a wrong state and wait for it to correct itself.

- **Define a workstream's gates up front**, when you take the work on, in order.
  A gate is a bead between the work before it and the work after it:

  ```
  cmdr internal-create-objective --project <p> --parent <bead> --title "<what is approved>" \
    --gate --approver <user|github-login|name>[,...] --criteria "<what counts as passed>" \
    --after <bead ids it waits on> --before <bead ids that wait on it>
  ```
- **Never start work past a gate that is not approved.** Everything between
  two gates is yours to do without asking.
- **When the work before a gate is done, ask for the approval:** for an
  `user` gate set it `--state needs-user` with a decision card; for a reviewer
  gate request nothing yourself (the user asks people), record the wait
  (`--waiting review:<login>`) and keep the PR green. A gate is approved when
  its bead is closed: close it once the approval is real (the user answered, the
  reviewers approved on GitHub), never before.
- Every contract below lists its gates.

### How an owned job looks in beads

One **job bead**, owned and marked with its finish line, with the contract's
stages as its **child beads**, created up front in order:

```
cmdr internal-set-objective --project <p> --bead <job> --owner <manager or operator role> --until <finish line>
cmdr internal-create-objective --project <p> --parent <job> --title "<stage>"   # once per stage, in order
```

- **Owning is not doing.** The owner of a job checks its stages and keeps the
  tree true; it does not work them. A stage that needs work (implementing,
  a review pass, fixing CI, answering comments, a rebase) goes to an Operator:
  `cmdr internal-spawn-operator --project <p> --step <stage> ...`, which claims
  that stage bead in the Operator's name. Brief and redirect it by mail. Only a
  one-line fix is yours to make directly, and then you own that stage
  (`--owner <you>`) while you make it.
- **Nothing is in progress without an owner.** Every in-progress bead names who
  is on it (`--owner operator-<x>`, or `user` for work in their own sessions);
  `internal-set-objective --state in-progress` refuses a bead with none. If
  something is happening, a bead says who is doing it.
- Close a stage (`internal-close-objective`) only when it holds. Set it back
  to `--state in-progress` the moment it stops holding (red CI, a conflict, a
  new review comment). A stage's state is what the user reads: never optimistic.
- A job that cannot start until another finishes: `cmdr internal-dep --project <project> add <job> <the job it waits on>`.
  Order alone is not dependency; record only real waits.
- Finished jobs are history: close them, never delete them.

### Own the spike: `--until approved`

(Optional: no work needs a spike first. What every workstream needs
first is an approved **Workflow approved** gate, see lead.md.)

Answer the open questions and write the tech spec the user approves.

Stages: **Frame the questions**, **Investigate**, **Draft the spec**,
**Self-review**, **Feedback addressed**. Gate: **Spec approved** (user).

- Without asking, through Operators on the stages: research, prototypes
  (throwaway), drafting, revising, getting the spec reviewed.
- The last stage is the user's by design: `--state needs-user` on it with the spec
  linked and a decision card. That is the one question the spike asks.

### Own merging this feature: `--until merged`

Make the feature's PRs mergeable, keep them that way, and keep them top
quality until they merge.

Stages: **Plan**, **Implement**, **Self-review**, **CI green**,
**Matches the spec**, **Review comments resolved**, **Mergeable**. Gates:
**PRs approved** (the reviewers), then **Merge** (user).

- Without asking, each through an Operator on its stage: implementing; the PR's Devin comments
  answered and resolved, then one ce-code-review pass with its blocker and major findings fixed; checking each PR
  against the spec; keeping CI green; answering and resolving every review
  comment; rebasing and fixing merge conflicts, including while waiting on
  approval.
- **Mergeable** holds only while every stage above it holds. When it holds,
  the job waits on the human approvals; you keep watching and drop back to
  the failing stage if anything changes.
- Never merge without the user. Never request reviewers unless they asked.

### Own this feature: `--until adopted`

Take the feature from question to people using it. The job's stages are the
jobs below, each its own owned job under this one, chained with `cmdr internal-dep add`:

1. **Spike** (own the spike, until approved)
2. **Merging** (own merging this feature, until merged)
3. **Go live** (`--until live`; stages **Deploy**, **Verify in prod**; gate
   **Live** (automatic: deployed, plus one real check that it works). main
   auto-deploys after an authorized merge, so no separate deploy approval is needed)
4. **Pilot** (`--until adopted`; stages **Roll out**, **Watch it run**; gate
   **User confirms** (the pilot user, by name))

### Escalating, for every contract

Ask the user only when you are truly blocked: access you do not have, a decision
only they can make (a spec trade-off, a product call), or a conflict between
the spec and a reviewer. Then set `--state needs-user` on the stage that is
blocked, with a decision card. Everything else is yours, silently.

### Commander reviews every owned tree

When a Manager takes ownership (or reshapes an owned job), Commander checks
the tree in beads against its contract before the user looks: the right finish
line, its approval gates present, with who approves each and the work on
each side of it, every stage present and in order, dependencies that are real waits,
states that are true (a stage closed only if it holds), and every in-progress
stage owned by whoever is actually on it. Anything off goes
back to the Manager to fix; only a real disagreement about scope goes to the user.
