-- One Manager per workstream (top-level objective), keyed by project + bead id.
-- am_role is the mail role (manager-<bead id>); session_file is the Claude
-- session-id file, which for an adopted Manager points at the project's
-- original manager.claude.session-id.

CREATE TABLE workstream_managers (
  project_id INTEGER NOT NULL,
  workstream_bead_id TEXT NOT NULL,
  am_role TEXT NOT NULL UNIQUE,
  session_file TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, workstream_bead_id)
);

PRAGMA user_version = 4;
