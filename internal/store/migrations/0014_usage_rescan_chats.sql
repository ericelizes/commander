-- The first ledger counted a chat turn's cumulative model_usage, which a
-- resumed process restarts from an old total; drop those rows and rescan
-- the chat logs from the top with per-turn usage.
DELETE FROM usage_turns WHERE source = 'chat';
DELETE FROM usage_scan WHERE path LIKE '%/chat/%.jsonl';

PRAGMA user_version = 14;
