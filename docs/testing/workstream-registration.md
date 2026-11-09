# Workstream Manager registration repair

Manager registration is authorized by fresh parent edges and an open
`kind:objective` bead, never by a dotted ID or a display row. Tasks, gates and
tracks cannot register. Display-only orphan promotion in `NestObjectives` is
not proof that a bead is a root. `Prepare`, `Adopt`, `Open`, and automatic native
preparation check this before writing registration/session/runtime state.
An unreadable or cyclic ancestry refuses the action. A gate made with
`internal-create-objective --gate` (`kind:objective` plus `gate`) is refused too.
`PrepareAll` (cmdr start and soft restart) skips a refused target, reports it,
and still prepares every valid workstream.

The supported repair command is diagnostic by default:

```sh
cmdr internal-unregister-workstream --project sample --bead sample-task
```

The owning Manager, Commander, or user can apply the exact repair using the
`root` returned by the diagnostic:

```sh
cmdr internal-unregister-workstream --project sample --bead sample-task --confirm-root sample-root
```

Both forms freshly resolve ancestry and refuse actual roots, Operators, other
Managers, and unexpected registration role names. A mismatched confirmation
refuses without writing. Repeating a successful repair reports `removed:false`.
The apply transaction deletes only the exact `workstream_managers` row and
records its role, session path, actor, verified root and timestamp in
`manager_unregistrations`. It retains beads, dependencies, tasks, worktrees,
agent runs, runtime/session files, ranks, mail mappings, and processes. It does
not stop an already-running stray chat. Managers must not bypass this command
with database edits or use it to stop processes.

The audit requires schema 21. Source verification does not authorize installation
or migration of the live database. Owning Managers run real repairs only after
the reviewed binary is installed through the user's normal approval flow.

Coverage: `TestRegistrationRejectsNestedAndNonObjectives`,
`TestRegistrationAncestryFailsClosed`, `TestStrictRootMissingAndCyclicAncestry`,
`TestOpenDoesNotTrustStaleNestedDisplayRoot`, `TestUnregisterWorkstreamCLI`,
`TestUnregisterNestedManagerIsPreciseAndIdempotent`, and eval
`TestManagerRegistrationScenario` (`scenarios/manager-registration.json`).
The eval emits a read-only summary of its invented fixture; it launches no
agents, browser or tmux server and touches no live project state.
