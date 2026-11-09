-- Preserve old receipts while giving each card generation its own journal.
ALTER TABLE needs_resolutions RENAME TO needs_resolutions_old;
CREATE TABLE needs_resolutions (
  project_id INTEGER NOT NULL REFERENCES projects(id),
  bead_id TEXT NOT NULL,
  generation TEXT NOT NULL,
  payload TEXT NOT NULL,
  state INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(project_id, bead_id, generation)
);
INSERT INTO needs_resolutions(project_id, bead_id, generation, payload, state)
SELECT project_id, bead_id, 'legacy', payload,
  CASE WHEN mailed THEN 3 WHEN applied THEN 2 ELSE 0 END
FROM needs_resolutions_old;
DROP TABLE needs_resolutions_old;
PRAGMA user_version = 17;
