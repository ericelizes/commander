-- Paired places (a Mac running the desktop app) and the runs offered to them.
-- A place run is an agent_runs row (status running from the offer until it
-- ends) plus one place_runs row with what only a place run has: its device,
-- its state on the Mac, the run token's hash and its heartbeats.

CREATE TABLE places (
  name TEXT PRIMARY KEY,          -- "mac:<slug>"
  kind TEXT NOT NULL,             -- "mac"
  public_key TEXT NOT NULL,       -- Ed25519, base64
  paired_at INTEGER NOT NULL,
  paired_by TEXT NOT NULL,
  revoked_at INTEGER              -- NULL = paired
);

CREATE TABLE place_runs (
  run_id INTEGER PRIMARY KEY,     -- agent_runs.id
  place TEXT NOT NULL,
  state TEXT NOT NULL,            -- offered | running | declined | exited | stopped | died
  reason TEXT NOT NULL DEFAULT '',
  step_id TEXT NOT NULL,
  manager_role TEXT NOT NULL,
  prod_gate TEXT NOT NULL DEFAULT '',
  brief_sha TEXT NOT NULL,
  token_hash TEXT NOT NULL DEFAULT '',
  offered_at INTEGER NOT NULL,
  accepted_at INTEGER NOT NULL DEFAULT 0,
  last_beat INTEGER NOT NULL DEFAULT 0,
  ended_at INTEGER NOT NULL DEFAULT 0,
  reported INTEGER NOT NULL DEFAULT 0,       -- a report comment landed
  unreachable_mailed INTEGER NOT NULL DEFAULT 0,
  settled INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX place_runs_place ON place_runs(place, state);

-- Comments a run posted, by the client id it sent, so a retried send is
-- written once.
CREATE TABLE place_comments (
  run_id INTEGER NOT NULL,
  client_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, client_id)
);

PRAGMA user_version = 23;
