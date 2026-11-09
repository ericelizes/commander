-- Each bead's priority among its siblings; rank 1 is first. Beads without a row
-- follow in the order they were added. Top-level workstreams keep workstream_ranks,
-- whose order spans projects.

CREATE TABLE bead_ranks (
  project_id INTEGER NOT NULL,
  bead_id TEXT NOT NULL,
  rank INTEGER NOT NULL,
  PRIMARY KEY (project_id, bead_id)
);

PRAGMA user_version = 22;
