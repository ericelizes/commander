-- Live state of each Claude agent pane, written by its Claude Code hooks
-- (UserPromptSubmit / PreToolUse -> working, Stop -> idle). One row per mail role.

CREATE TABLE agent_state (
  role TEXT PRIMARY KEY,
  state TEXT NOT NULL,           -- working | idle
  detail TEXT NOT NULL DEFAULT '', -- what it is doing: tool name and a short target
  since INTEGER NOT NULL,        -- when it entered this state
  updated INTEGER NOT NULL       -- last hook event, to spot a silent (dead/stuck) agent
);

PRAGMA user_version = 7;
