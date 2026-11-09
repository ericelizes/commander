-- What a card was raised against (JSON: spec hash, generation, raise time),
-- so the board can tell it went out of date. NULL = raised before this
-- column: only the age rule applies to it.
ALTER TABLE pitches ADD COLUMN basis TEXT;

PRAGMA user_version = 19;
