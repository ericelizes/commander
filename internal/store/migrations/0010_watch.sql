-- cmdrd's minute watch over running Operators, and each Manager's next
-- check-in on them.
ALTER TABLE agent_runs ADD COLUMN active_at INTEGER NOT NULL DEFAULT 0;  -- last write to its transcript (unix s)
ALTER TABLE agent_runs ADD COLUMN quiet_told INTEGER NOT NULL DEFAULT 0; -- the active_at its Manager was told it went quiet at

CREATE TABLE manager_checkins (
  role    TEXT    PRIMARY KEY,       -- manager-<workstream bead>
  due     INTEGER NOT NULL,          -- unix s of its next check-in
  checked INTEGER NOT NULL DEFAULT 0 -- unix s of the watch's last look at its Operators
);

-- What the minute watch finds wrong across workstreams, for Commander: one row
-- per possible issue from the moment it is seen, active once it has lasted.
CREATE TABLE watch_issues (
  key    TEXT    PRIMARY KEY,         -- <workstream bead>:<kind>[:<detail>]
  ws     TEXT    NOT NULL,            -- workstream bead id
  text   TEXT    NOT NULL,            -- "stalled: nobody on its open work"
  since  INTEGER NOT NULL,            -- unix s it began
  active INTEGER NOT NULL DEFAULT 0,  -- 1 once it has lasted long enough to matter
  told   INTEGER NOT NULL DEFAULT 0   -- 1 once Commander was told
);

PRAGMA user_version = 10;
