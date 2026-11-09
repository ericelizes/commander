-- Role -> am-name mapping for the agent-mail server.
--
-- The am server assigns RANDOM agent names (adjective+noun) and rejects
-- descriptive role names like "manager-commander". commander still thinks in
-- role terms, so we persist the mapping here: given an am project key and a
-- role (e.g. "manager-commander"), resolve the server-assigned name
-- (e.g. "GoldTiger") to address it. See docs/agent-mail-routing.md.

CREATE TABLE am_agents (
  id INTEGER PRIMARY KEY,
  am_project_key TEXT NOT NULL,
  role TEXT NOT NULL,
  am_name TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(am_project_key, role)
);

PRAGMA user_version = 2;
