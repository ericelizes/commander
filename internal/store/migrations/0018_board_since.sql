-- When each board row (a workstream or track) entered the state it shows, for
-- states cmdrd does not time otherwise ("Stalled", "Readying"): the sidebar
-- and the agents read the same age, across restarts.
CREATE TABLE IF NOT EXISTS board_since (
  key   TEXT    PRIMARY KEY, -- project:bead of the row
  state TEXT    NOT NULL,    -- kind|text it shows
  since INTEGER NOT NULL     -- unix s it began showing it
);

PRAGMA user_version = 18;
