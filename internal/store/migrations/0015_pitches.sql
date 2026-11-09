-- Every version of every pitch (a decision card on a step), kept after it is
-- answered, with the user's answer on the version they answered. needs_you
-- holds only the open card; this is the record.
CREATE TABLE pitches (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,
  bead_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  title TEXT NOT NULL,
  decision TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  answer TEXT NOT NULL DEFAULT '',
  answered_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE (project_id, bead_id, version)
);
CREATE INDEX pitches_bead ON pitches(project_id, bead_id);

-- The cards open now are each pitch's first version.
INSERT INTO pitches(project_id, bead_id, version, title, decision, created_at)
SELECT project_id, bead_id, 1, ask, decision, since FROM needs_you WHERE decision != '';

PRAGMA user_version = 15;
