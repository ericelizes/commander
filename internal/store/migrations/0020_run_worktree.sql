-- The worktree a run works in: a follow-up Operator takes over another's
-- worktree under its own name, so the name does not say. NULL = recorded before
-- this column: the run's worktree is .worktrees/<its name>.
ALTER TABLE agent_runs ADD COLUMN worktree_path TEXT;

PRAGMA user_version = 20;
