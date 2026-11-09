-- A needs-user flag carries a decision card (JSON: question, context, options,
-- input, steps) that the inspector renders as a form. Empty = no card.
ALTER TABLE needs_you ADD COLUMN decision TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 8;
