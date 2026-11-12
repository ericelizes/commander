-- Paired places (a Mac running the desktop app) and the runs offered to them.
-- A place run is an agent_runs row (status running from the offer until it
-- ends) plus one place_runs row with what only a place run has: its device,
-- its state on the Mac, the run token's hash and its heartbeats.

CREATE TABLE places (
  name TEXT PRIMARY KEY,          -- "mac:<slug>"
  kind TEXT NOT NULL,             -- "mac"
  public_key TEXT NOT NULL,       -- Ed25519, base64
  paired_at BIGINT NOT NULL,
  paired_by TEXT NOT NULL,
  revoked_at BIGINT              -- NULL = paired
);

CREATE TABLE place_runs (
  run_id BIGINT PRIMARY KEY,     -- agent_runs.id
  place TEXT NOT NULL,
  state TEXT NOT NULL,            -- offered | running | declined | exited | stopped | died
  reason TEXT NOT NULL DEFAULT '',
  step_id TEXT NOT NULL,
  manager_role TEXT NOT NULL,
  prod_gate TEXT NOT NULL DEFAULT '',
  brief_sha TEXT NOT NULL,
  token_hash TEXT NOT NULL DEFAULT '',
  offered_at BIGINT NOT NULL,
  accepted_at BIGINT NOT NULL DEFAULT 0,
  last_beat BIGINT NOT NULL DEFAULT 0,
  ended_at BIGINT NOT NULL DEFAULT 0,
  reported BIGINT NOT NULL DEFAULT 0,       -- a report comment landed
  unreachable_mailed BIGINT NOT NULL DEFAULT 0,
  settled BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX place_runs_place ON place_runs(place, state);

-- Comments a run posted, by the client id it sent, so a retried send is
-- written once.
CREATE TABLE place_comments (
  run_id BIGINT NOT NULL,
  client_id TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (run_id, client_id)
);


CREATE UNIQUE INDEX place_runs_one_active ON place_runs(place) WHERE state IN ('offered','running');
CREATE TRIGGER places_state_event AFTER INSERT OR UPDATE OR DELETE ON places FOR EACH ROW EXECUTE FUNCTION commander_state_event();
CREATE TRIGGER place_runs_state_event AFTER INSERT OR UPDATE OR DELETE ON place_runs FOR EACH ROW EXECUTE FUNCTION commander_state_event();
