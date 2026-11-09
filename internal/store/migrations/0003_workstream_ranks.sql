-- the user's fleet-wide priority order for workstreams (top-level objectives).
-- Keyed by project + bead id; rank 1 is the top. Workstreams without a row are
-- new and sort below every ranked one. Not a beads label: the order spans
-- projects.

CREATE TABLE workstream_ranks (
  project_id INTEGER NOT NULL,
  bead_id TEXT NOT NULL,
  rank INTEGER NOT NULL,
  PRIMARY KEY (project_id, bead_id)
);

PRAGMA user_version = 3;
