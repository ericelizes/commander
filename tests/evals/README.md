The Commander harness uses beads_rust 0.7.6 for private fixture databases.
Before running Go tests or ship checks, explicitly provision a trusted executable:

```sh
python3 scripts/setup-eval-backend.py /absolute/path/to/trusted/beads_rust
# equivalent: just setup-eval-backend /absolute/path/to/trusted/beads_rust
```

Python 3 is required for provisioning. The setup command copies the selected
binary into the user cache (`$XDG_CACHE_HOME/commander/eval-tools` or
`~/.cache/commander/eval-tools` on Linux; `~/Library/Caches/commander/eval-tools`
on macOS), validates exact `br 0.7.6` output on that copy, and atomically publishes
`backend.json` with schema, absolute path, version and SHA-256. Re-run setup to
replace or repair it. Use the same cache/user for setup and tests.

Direct `go test ./tests/evals`, `go test ./...`, Justfile `test-evals`, `test-go`
and `test`, and internal ship verification all resolve this record through the
shared harness, before test HOME isolation. Ship's temporary checkout uses the
same user cache. No developer environment variable is needed. The bounded
`eval-backend` CI job provisions the pinned crate with a pinned Rust toolchain,
then runs the normal activation scenarios without an override.

The harness checks the recorded SHA-256 before execution, validates the version,
logs path/version/hash, and installs a private `br` launcher before inherited
PATH. It never discovers `br` through PATH or changes the host routing wrapper.
Absent records, unavailable executables, changed bytes, and wrong versions fail
closed. Setup selects trusted test tooling; its version/hash record detects
accidental replacement and is not authentication against a malicious same-user
writer. An explicit absolute `COMMANDER_EVAL_BR_BIN` remains supported for
one-off private fixtures, with executable/version validation and identity logging;
standard entrypoints do not depend on it.

`TestEvalBRDefaultEntrypoint` exercises real provisioning, default selection,
quoted paths/arguments, copy independence and hash/missing-file refusal.
`TestEvalBRUnavailableDefault` checks missing setup never invokes host `br`.
`TestEvalBRSelection` covers explicit selection and incompatible identities.
Existing `activate-commander-explicit` and `activate-commander-pathless`
scenarios cover activation with unchanged assertions and deadlines.
