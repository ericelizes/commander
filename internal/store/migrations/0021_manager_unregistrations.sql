CREATE TABLE manager_unregistrations (
 id INTEGER PRIMARY KEY,
 project_id INTEGER NOT NULL,
 bead_id TEXT NOT NULL,
 root_id TEXT NOT NULL,
 actor TEXT NOT NULL,
 manager_role TEXT NOT NULL,
 session_file TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
PRAGMA user_version = 21;
