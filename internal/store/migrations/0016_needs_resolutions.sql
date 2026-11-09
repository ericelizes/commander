-- A saved answer survives bead/queue failures and carries pending Manager mail.
CREATE TABLE needs_resolutions (
  project_id INTEGER NOT NULL REFERENCES projects(id),
  bead_id TEXT NOT NULL,
  payload TEXT NOT NULL,
  applied INTEGER NOT NULL DEFAULT 0,
  mailed INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(project_id, bead_id)
);
PRAGMA user_version = 16;
