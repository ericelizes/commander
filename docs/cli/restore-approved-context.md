# Restore retained approved context

`cmdr internal-restore-approved-context --project P --bead B --generation N --answer-id ID --dry-run`

After checking the dry run, omit `--dry-run` to restore the Description from the
exact stored decision card's Context. No text/file argument is accepted. The
result identifies the generation, answer ID and SHA-256 of the original context.
A Description already equal to that context succeeds without writing; any other
nonempty Description refuses, including whitespace-only values. The guard reads
exact JSON bytes separately from display-normalized Show. Explicit null, malformed,
or ambiguous data refuses without text fallback. Native bd 1.3.1 (c1c4b642a)
omits empty descriptions from its array-form show records. For that exact version
and a complete native issue shape (identity, title, status, priority, issue type,
creation/update timestamps), an omitted field triggers a separate
`--readonly sql "SELECT id, description FROM issues WHERE id = 'B'" --json`.
Only one matching row with an explicit string proves the exact value; missing,
null, unavailable or ambiguous query results refuse. Identifiers for this query
must contain only ASCII letters, digits, hyphens, underscores or dots. Unknown
versions and partial show shapes refuse. No omission is inferred to mean empty.

Only the owning Manager (fresh edge ancestry), Commander, or user may use this
command, and only on a closed gate with `approver:user`. Both legacy `gate` and
`kind:gate` labels are accepted. The latest pitch must have a matching, completed
resolution with the requested generation and answer ID. A direct approval must
have its recorded `Approved` answer; a decision choice must match an explicitly
approving option in that exact card. Missing, superseded, rejected, newer
unanswered, or mismatched evidence refuses. The card must have nonempty Context.

State metadata is opened with SQLite `mode=ro` and `query_only`, using only columns
available at schema 20; no migrations, state writes or schema overrides occur.
The only write is Description through the ordinary Beads client and its audited
write door. Status, labels, answer history and gate relationships stay intact.
This restores the original scope verbatim; it grants no new approval and does not
establish that any proposed production operation is covered by that scope.

Like other CLI field edits, the fresh check and Beads write are separate backend
operations. The backend has no conditional Description update; concurrent outside
writers must be excluded for the whole command, including write retries. Project
mapping, ancestry, pitch/resolution evidence, gate status/labels and Description
must all remain stable. There is no atomic still-owned, still-closed, latest-
generation or non-overwrite guarantee against racing changes. No raw database write is used to work
around that limitation.

Validation: `TestRestoreApprovedContextCommandSchema20`, `TestRestore`,
`TestRetainedRefusesUnprovenApprovals`, `TestProvenanceSchema20ReadOnly`, and
`tests/evals/scenarios/restore-approved-context.json` via
`TestRestoreApprovedContextScenario`. `TestRestoreExactBackend` covers the real command client with a fake backend;
independent review is required before binary delivery or operational use.

Backend contract proof (read-only, 2026-11-03): `bd --version` returned
`bd version 1.3.1 (c1c4b642a)`. `bd schema --help` states that its schema is
reflected from the serialized Go types; `bd schema` reports schema version 1,
issue.description type string, outside the required fields. On an existing local
empty-description issue, `show ID --json` omitted description while
`--readonly sql "SELECT id, description FROM issues WHERE id = 'ID'" --json`
returned a matching row with `"description":""`. The nonempty task brief also
returned its full description through this query. No live restoration or dry run
was performed. Tests use invented identifiers and values with the observed shape:
`TestExactDescriptionNativeOmission`, `TestRestoreExactBackend` and
`TestRestoreNativeDescriptionScenario` (`restore-native-description.json`).
