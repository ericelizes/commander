-- What the user must do for each step flagged needs-user: a short verb-first ask
-- (<= 30 chars, shown in the sidebar) beside the full note (shown in the
-- inspector). One row per flagged step; deleted when the step leaves needs-user.

CREATE TABLE needs_you (
  project_id INTEGER NOT NULL,
  bead_id TEXT NOT NULL,
  ask TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  since INTEGER NOT NULL,
  PRIMARY KEY (project_id, bead_id)
);

PRAGMA user_version = 6;
