# Mac places: schema-two release prerequisites

This document preserves historical candidate evidence. Current source uses canonical runtime 1–3, places 4 and qualified custody 5; historical places 2/custody 3 refuse. See [current composition](../architecture/mac-runtime-composition.md) for the superseding migration contract.

Prepared for commander-il9.206.4.12. **Readiness: false.** This is a source and
retained-record contract, not permission to migrate, install, restart or activate.
No target Mac, SSH, credentials, selected live database or endpoint was inspected.
The schema-one initial batch is independent and explicitly excludes Mac places.

## Accepted evidence and its limits

| Evidence | What it establishes |
| --- | --- |
| FULL .206.4.9, run 4281, head `f01ace9317ae9830cdd04d73e48e9c64a2c72566` | Current PostgreSQL integration source reviewed CLEAN with five minor findings; original twelve-lens coverage retained. |
| Independent .206.4.11, run 4303, head `158f0064d08c0e5bfd815917c081ca22fa7b67ac`, base f01ace93 | Full-depth fix-delta CLEAN; fixes MAC-PG-PRESENCE-NULL. Does not supersede the unchanged original scope. |
| .206.4.10 and independent .4.11 native execution | Exact f01ace93 fails SQLSTATE22023; exact158f0064 passes five native PostgreSQL tests, zero failures/skips, plus three SQLite controls. Private PostgreSQL16.15 socket-only fixture cleaned up. |
| .206.4.7 candidate/build record | Source transplant, targeted Go, Electron31, typecheck and fake-Mac eval passed; isolated cockpit startup/text passed. Feature-specific cockpit proof absent after a retained guard refusal. |

Native PASS names: TestPostgresPlaceClaimAndPresence, TestPostgresPlacesUpgrade,
TestPostgresClaimPreventsDuplicateDispatchAndLateHooks,
TestPostgresRuntimePresenceExpiresAndDoesNotInferActivity,
TestPostgresNativePresenceNilEmptyAndRoles. Later heartbeat, stale presence,
comment deduplication, end and replacement assertions ran. This is Linux-native
trial database evidence, not a native Mac or production lifecycle rehearsal.

Retain minors mac-brief-truncation, mac-terminal-persistence, mac-report-retry,
mac-timeout-fence, pg-mac-offered-dead, plus older .206.4.8 mac-quit-late-report.
No additional minor fix pass or review roster is proposed here. The prior full
suite's four reproduced baseline failures also remain recorded, not converted to
passes (approvedcontext, beads, gates and layout fixtures).

## Target and protocol evidence

The Manager's retained .4.7 input says the existing Mac audit has **no** measured
architecture, endpoint, protocol or compatible rollback inputs. Candidate/build
records and immutable source inspected here do not supply them either.

| Item | Source contract | Actual target evidence |
| --- | --- | --- |
| CPU/OS | `app/electron/place-main.ts` starts only on darwin; `app/build/electron-builder.yml` packages mac arm64 dmg/zip/dir. Packaging supports a build target, not a measured device. | Missing CPU architecture, OS version, compatible Electron runtime and native dialog/process-group behavior. No x64 support claim. |
| App | Main/preload plus runner must be packaged, not just renderer assets. App version is sent in hello. | Missing signed/ad-hoc artifact identity, SHA256, source revision, actual installed app version and runner inclusion. |
| cmdr/server | `internal/place/service.go` requires nonempty server revision equal to hello.cmdr_version; app obtains it using `cmdr place revision`. | Missing target cmdr absolute path, embedded revision and server cmdrd revision; no matching deployed pair proved. |
| Protocol | WebSocket challenge, Ed25519 signed hello, welcome/refusal; app_version is metadata, not an independently enforced protocol compatibility version. Per-run HTTP calls pin the recorded server URL. | Missing approved reachable base URL, HTTP/WebSocket routing/TLS behavior, handshake observation and authenticated run-endpoint evidence. No endpoint invented. |
| Local execution | WebSocket implementation, discovered cmdr and Claude executables, own Claude login, local place policy, pairing custody, explicit per-run native confirmation. | Missing availability/login-capability attestation, policy/consent evidence and approved pairing custody; no credentials requested or inspected. |

Exact158f0064 source has the reviewed protocol, but version equality alone does
not attest app bundle provenance. Future artifacts need reproducible source-to-
artifact manifests for app, cmdr and cmdrd, including build flags/embedded revision.
A future separately authorized native rehearsal must demonstrate dialog accept/
decline, guarded execution, heartbeat, mail/report, exit/stop/revoke and settlement.
Do not retry the retained feature-cockpit guard refusal through another route.

## Coordinated consumer contract

`internal/store/postgres.go` is authoritative: opening the candidate store can
migrate, even for a CLI verb that looks read-only. Never invoke candidate CLI
against default/live COMMANDER_HOME during preparation.

| Consumer/source | Current schema behavior | Required disposition before places activation |
| --- | --- | --- |
| Installed baseline `77d816f0165025a1a9e610204e88aae62e6f9272` | Accepts only1, rejects2. Installed identity is retained Manager evidence, not re-probed here. | Replace the complete cmdr/cmdrd pair and every long-lived store reader/writer with the coordinated release, or stop/retire them. Include process-resident old daemons, hosts and operator hooks, not only PATH links. |
| Frozen private helper `3e064c92c21c5d9c91a801ecc8448e3ebccc54b5` | Accepts only1, rejects2. | Produce independently checked compatible replacement preserving its private helper behavior, or formally retire its use and all launch references. |
| Withdrawal helper `e442a2f48b27316ff277d7515a86e2860c6ddb02` | Accepts only1, rejects2. Historical .4.9/.4.11 name older5c7150f3; this audit checks requested e442 directly and reaches the same incompatibility. | Preserve exact retained unanswered-pitch withdrawal behavior in a compatible replacement, or retire it. A plain158f0064 binary is not evidence it includes the e442-specific repair. |
| Mac/server candidate `158f0064d08c0e5bfd815917c081ca22fa7b67ac` | Accepts2; upgrades1 transactionally; fresh initialization creates core1 then places2. | Release candidate only. Do not open it on the schema-one initial batch. Matching target app/cmdr/server and compatible helper replacements are still absent. |

Future sequence, requiring separate authorization: inventory exact consumers and
launch references; build and verify replacements or record retirement; validate
all consumers on one disposable fixture before and after migration; freeze
writers/dispatch and drain affected processes; retain backup plus rollback/repair
artifacts; perform one coordinated migration/start sequence; verify resumed hooks,
claims, mail, read/write paths and Mac lifecycle. Never leave a schema1-only helper
as the recovery tool for a schema2 database. If readiness evidence is incomplete,
keep schema1 running and Mac excluded. No action in this sequence was executed.

## Resolve migration numbering by source contract

Reserve PostgreSQL **2 = places** as implemented/reviewed at158f0064:
`postgres_places.sql` creates places, place_runs, place_comments, active-place
uniqueness and transactional event triggers. It does not create qualified graph
columns. The existing opener accepts any recorded2 without reapplying that SQL;
therefore a different migration also stamping2 would silently claim a shape the
binary never verified. SQLite migration23 is a separate namespace.

Qualified lifecycle .204.6.38.2.1 checkpoint
`db59e797fd216a52c4b299d2c76f4d837a90e275` still commits schema1 and only
unwired qualified-graph primitives. Its proposed2 exists in private unreviewed
qualified-draft, explicitly PROVISIONAL; .38.2.2 is continuing implementation.
It is not an alternate deployed schema2, nor compatible by number alone.

The release allocation is places2, qualified lifecycle **3 or a later reserved
version**. A future lifecycle writer must rebase/compose onto the accepted places
schema contract and preserve its tables/triggers, then implement a distinct2->3
migration under the same migration lock. Fresh0->1->2->3, existing1->2->3 and
places2->3 paths must converge on the same schema and behavior. The original Mac2
binary rejects3 and is consequently not a rollback for the combined release.
Any future consumer accepting3 needs the qualified mutation/read/cache contract,
not merely a constant bump. Do not relabel an already stamped2 in place.

This resolves the proposed allocation for this contract, not an applied migration
or an assertion that another writer has adopted it. Manager must communicate and
confirm the reservation with .38.2.2 before its candidate is release-qualified.
No lifecycle draft was edited, no foreign worktree write or source integration
occurred. If release order changes, a single independently reviewed cumulative
migration contract must replace this allocation before deployment.

## Artifact availability and recovery requirements

Available now: immutable Git sources for all five revisions above; retained
.4.7 patch/mapping/build/test logs; .4.10 and .4.11 native provenance, result and
cleanup records. The .4.11 `pg/after-store.test` is an executable **test harness**,
not a cmdr/cmdrd recovery pair or a Mac application. Existing Electron tests and
packaging configuration are not an installed Mac artifact.

No supplied record establishes an exact158f0064 packaged Mac app, a coordinated
schema2-compatible77/3e/e442 replacement set, or an independently verified
operational rollback/repair-forward cmdr/cmdrd artifact. No such availability is
claimed. No build was staged merely to disguise missing target or compatibility
evidence; building158 alone would not compose the helper-specific behavior.

Preferred recovery after schema2 writes: retain schema2 and repair forward using
a separately built/verified recovery release preserving all canonical PG state,
claims/fences, places and required private helper operations. It must carry its
own source revision, OS/CPU, binary SHA256, embedded version, helper behavior and
trial-schema compatibility evidence. A feature-disabled compatible recovery is
possible only after independent validation that it safely drains/settles existing
place runs. Neither77 nor3e nor e442 nor pre-fix f01ace93 qualifies as-is.

Before authorized migration retain a private custom-format PG backup, schema and
migration inventory, version/artifact manifests, necessary role/extension restore
inputs and app/pairing configuration custody. Do not copy secrets into reports.
`scripts/postgres-backup.sh` provides dump plus archive-index validation; this is
not a completed backup or restore rehearsal. Require a dated checksum, restricted
location/retention and isolated restore proof with row/event/claim validation.
No backup path or verified recoverable backup was supplied here.

Restoring a pre-upgrade backup with schema1 binaries is only a separately
approved outage/data-reconciliation recovery: it discards subsequent PG writes
unless those are preserved and reconciled. Prefer repair forward. Never select
retired SQLite/Dolt state as rollback, never manually lower commander_schema,
and never drop places or qualified columns to appease an old opener. Back up
post-upgrade writes before any recovery action as well.

## Remaining release blockers

1. Actual target CPU/OS, app and cmdr artifact provenance, server revision and
   endpoint/protocol/local execution capability evidence listed above.
2. Coordinated compatible consumer replacements or explicit retirement,
   including corrected e442 withdrawal helper and every resident old process.
3. Manager-confirmed allocation of places2 / later qualified version, coherent
   cumulative source and native migration/consumer compatibility validation.
4. Actual compatible recovery artifacts and private backup/restore proof.
5. Feature-specific cockpit evidence and separately authorized native Mac
   end-to-end lifecycle rehearsal. Existing native PG tests do not replace these.

No speculative user approval card or new access-owner lookup roster is needed.
Missing target/recovery records are explicit prerequisites. Documentation-only
preparation adds no interaction flow; no new automated feature test, eval,
cockpit run or broad suite was performed. Retained execution is attributed above.
