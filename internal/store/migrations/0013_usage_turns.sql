-- Token use of every agent, Claude or Codex, one row per finished turn: what
-- the Usage tab breaks down by account, agent and model. turn_key makes a
-- rescan idempotent (chat: <role>:<seq>; Operator: <log path>:<offset>).
CREATE TABLE usage_turns (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  turn_key     TEXT    NOT NULL UNIQUE,
  source       TEXT    NOT NULL,            -- chat | operator
  role         TEXT    NOT NULL,            -- commander, manager-<bead>, operator-<flat>
  runtime      TEXT    NOT NULL,            -- claude | codex
  account      TEXT    NOT NULL DEFAULT '', -- Claude account name, or codex
  model        TEXT    NOT NULL DEFAULT '',
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read   INTEGER NOT NULL DEFAULT 0,
  cache_create INTEGER NOT NULL DEFAULT 0,
  usd          REAL    NOT NULL DEFAULT 0,
  occurred_at  INTEGER NOT NULL             -- unix s
);
CREATE INDEX idx_usage_turns_time ON usage_turns(occurred_at);

-- How far each log has been read, and what the reader carries between passes
-- (a Claude process's cumulative totals, the model in use), as JSON.
CREATE TABLE usage_scan (
  path   TEXT    PRIMARY KEY,
  offset INTEGER NOT NULL DEFAULT 0,
  state  TEXT    NOT NULL DEFAULT ''
);

-- The model an Operator run used (from its stream's init line).
ALTER TABLE agent_runs ADD COLUMN model TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 13;
