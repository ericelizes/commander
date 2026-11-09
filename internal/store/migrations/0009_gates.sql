-- A gate is a check a stage (step bead) must pass before it can close. Labels
-- hold at most 50 characters, so the check and the evidence live here.
CREATE TABLE gates (
  project_id INTEGER NOT NULL,
  bead_id    TEXT    NOT NULL,
  name       TEXT    NOT NULL,
  check_text TEXT    NOT NULL,           -- a shell command (auto) or a rule to verify (reviewer/user)
  verifier   TEXT    NOT NULL,           -- auto | reviewer:<login> | user
  status     TEXT    NOT NULL DEFAULT 'open',  -- open | passed | failed
  evidence   TEXT    NOT NULL DEFAULT '',
  by_who     TEXT    NOT NULL DEFAULT '',
  at         INTEGER NOT NULL DEFAULT 0,
  created    INTEGER NOT NULL,
  PRIMARY KEY (project_id, bead_id, name)
);

PRAGMA user_version = 9;
