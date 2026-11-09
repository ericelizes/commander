-- Schema version 1: initial commander state schema.
-- Sets PRAGMA user_version = 1 at the end so re-application is a no-op.

CREATE TABLE projects (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  type TEXT NOT NULL CHECK(type IN ('code', 'knowledge')),
  path TEXT NOT NULL,
  activated_at INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'active'
);

CREATE TABLE objectives (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  description_path TEXT,
  status TEXT NOT NULL DEFAULT 'open',
  created_at INTEGER NOT NULL,
  closed_at INTEGER
);

CREATE TABLE objective_tasks (
  objective_id INTEGER NOT NULL REFERENCES objectives(id),
  project_id INTEGER NOT NULL REFERENCES projects(id),
  task_bead_id TEXT NOT NULL,
  is_derived INTEGER NOT NULL DEFAULT 0,
  derived_from_task_bead_id TEXT,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (objective_id, project_id, task_bead_id)
);

CREATE TABLE cost_events (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  task_bead_id TEXT,
  agent_name TEXT NOT NULL,
  event_type TEXT NOT NULL,
  reason TEXT,
  delta_usd REAL DEFAULT 0,
  delta_tokens INTEGER DEFAULT 0,
  occurred_at INTEGER NOT NULL
);

CREATE TABLE agent_runs (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  task_bead_id TEXT NOT NULL,
  agent_name TEXT NOT NULL,
  opencode_session_id TEXT NOT NULL,
  container_id TEXT,
  tmux_pane_id TEXT,
  spawn_time INTEGER NOT NULL,
  tokens_used INTEGER NOT NULL DEFAULT 0,
  usd_used REAL NOT NULL DEFAULT 0,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'running'
);

CREATE TABLE tui_state (
  id INTEGER PRIMARY KEY CHECK(id=1),
  current_view TEXT NOT NULL DEFAULT 'commander',
  selected_project_id INTEGER,
  selected_task_bead_id TEXT,
  scroll_state TEXT
);

PRAGMA user_version = 1;
