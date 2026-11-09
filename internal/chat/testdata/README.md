# Synthetic chat protocol fixtures

`stream_synthetic.jsonl` and `transcript_synthetic.jsonl` are invented examples.
They cover denied Write, accepted Bash, interrupted streaming, cumulative costs,
local slash commands, compaction and transcript hydration. All identifiers,
usage values and prose are synthetic; there is no account inventory or signature.

Regenerate them and the four frontend adapter examples with
`python3 scripts/generate-synthetic-chat.py` from the repository root.
The frontend examples live in `app/src/features/chat/__tests__/testdata/`.

Fixture identities
use a fixed sample UUID and sequential message/tool/request ids; the fields keep
the wire relationships that the parser, reducer and hydration tests exercise.
