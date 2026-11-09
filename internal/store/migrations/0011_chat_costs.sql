-- Cost of the native chats (Commander and Managers), one row per finished turn.
-- (session_id, total_usd) is the idempotency key: total_usd is claude's
-- cumulative session cost, so a replayed or backfilled result is the same row.
CREATE TABLE chat_costs (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  role           TEXT    NOT NULL,
  session_id     TEXT    NOT NULL,
  total_usd      REAL    NOT NULL,           -- cumulative session cost after this turn
  delta_usd      REAL    NOT NULL,           -- this turn's cost
  input_tokens   INTEGER NOT NULL DEFAULT 0,
  output_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_read     INTEGER NOT NULL DEFAULT 0,
  cache_create   INTEGER NOT NULL DEFAULT 0,
  occurred_at    INTEGER NOT NULL,           -- unix s
  UNIQUE (session_id, total_usd)
);
CREATE INDEX idx_chat_costs_role_time ON chat_costs(role, occurred_at);

PRAGMA user_version = 11;
