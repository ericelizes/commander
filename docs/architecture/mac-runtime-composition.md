# Mac source on the canonical runtime lineage

This source composes the reviewed custody candidate b6a345cb, Mac service/app
slice ab0b747c..158f0064, own-run ca72ca82..3e064c92 and gate withdrawal
db4c96b6..e442a2f48 onto canonical main 64d90ffc (which includes 21756bbe and 2324ad7c).
The historical review of b6a345cb by operator-exec-vm-16-4deda3fcfee4 remains
attributed to that reviewer; it is not a review of this new composition.

The PostgreSQL migration order is:

1. Core `postgres_schema.sql` (shipped, unchanged).
2. Jurisdiction, questions, execution and supervision SQL (shipped, unchanged).
3. Runtime integrity SQL (shipped, unchanged).
4. `postgres_0004_places.sql`: exact reserved Mac places DDL, now following the
   canonical runtime history.
5. `postgres_0005_qualified.sql`: qualified dependency endpoints, target FK,
   local parent constraint, indexed traversal and serialized graph revision.

SQLite retains its shipped 1–22 bytes and adds migration 0023 for places.
`TestShippedMigrationBytesUnchanged` freezes all SQL present on the base main.

The opener takes the existing migration advisory transaction lock before
checking catalog shape. Embedded fingerprints in `internal/store/schema_shapes`
record each canonical historical shape, generated on private PostgreSQL 16.15
from the unchanged numbered SQL. Each fingerprint covers an entire catalog
object definition: column type/nullability/default and generated/identity expressions, constraints,
index definitions/validity, trigger definitions/enabled state or function
body/signature. Unrelated additional objects are allowed;
missing or changed required objects refuse. The final shape is checked before
commit and on every reopen. These fingerprints intentionally fail closed if a
PostgreSQL version renders incompatible catalog definitions; a future supported
server version needs explicit fixture verification, not a skipped check.

Historical b6a345cb databases recorded places as 2 and qualified custody as 3.
They are explicitly refused before any migration, marker change or event write.
The error names that historical lineage and requires reconciliation on a private
recovery database. Renumbering its marker is not a supported recovery procedure.
The own-run reader also checks the current catalog shape while retaining its
read-only, context-bounded, no-initialization contract.

Canonical 0/1/2/3/4/5 fixtures converge to 5, retaining dependency backfills,
place records, active claims, heartbeats and comment deduplication. Native
companion tests verify wrong-shape refusal, historical-numbering refusal,
transaction rollback, lock serialization, runtime integrity preservation and
execution admission. The JSON scenario is `postgres-composed-schema.json`.

The source preserves main's planning receipts, authority/hold/completion checks
inside the custody transaction. Mac offers share canonical run claims and do
not bypass active-mode capacity admission. This composition does not establish
native Mac execution or active Mac scheduler dispatch: remote target proof and
release/recovery verification remain separate work. It authorizes no install,
restart, live database change, target access or merge. Old schema-2/3 binaries
are incompatible rollback tools for schema 5; recovery consumers must use this
exact cumulative source or a separately verified compatible build.
