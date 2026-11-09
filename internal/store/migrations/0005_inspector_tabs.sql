-- Inspector (right column) tabs, keyed like workstream_ranks / workstream_managers:
-- project_id 0 and workstream_bead_id '' are the Commander window.

CREATE TABLE inspector_tabs (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,
  workstream_bead_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  title TEXT NOT NULL,
  kind TEXT NOT NULL,                     -- url | cmd | (v2) term | image
  target TEXT NOT NULL,                   -- URL, shell command, or path
  renderer TEXT NOT NULL DEFAULT '',      -- '' = auto-detect from target
  refresh_secs INTEGER NOT NULL DEFAULT 60,
  added_by TEXT NOT NULL,                 -- 'user' | am role e.g. manager-<bead>
  created_at INTEGER NOT NULL,
  UNIQUE (project_id, workstream_bead_id, target)
);

-- Which tab is showing, per window, plus the cached card for it.
CREATE TABLE inspector_state (
  project_id INTEGER NOT NULL,
  workstream_bead_id TEXT NOT NULL,
  active_tab_id INTEGER,
  cache_json TEXT,
  cached_at INTEGER,
  PRIMARY KEY (project_id, workstream_bead_id)
);

PRAGMA user_version = 5;
