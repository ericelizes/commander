-- Optional git checkout for a project whose path (the beads root) is not itself
-- a git repo: Operator worktrees come from here. '' = the project path.
ALTER TABLE projects ADD COLUMN repo_path TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 12;
