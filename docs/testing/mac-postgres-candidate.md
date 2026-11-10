# Mac source candidate on PostgreSQL

This candidate transplants only the reviewed Mac feature delta from foundation
`07ffcce26037660f497844da54df0832b57ba4af` through
`9aa9ab5d2381ecf52d1dccafd9d81d955765ef9e` onto PostgreSQL main
`77d816f0165025a1a9e610204e88aae62e6f9272`. Historical full review
commander-il9.206.4.4 and cumulative fix review commander-il9.206.4.6 cover the
historical sources. They do not certify the new PostgreSQL integration.
The retained `mac-quit-late-report` minor is unchanged.

The initial transplant is commit `ab0b747c`. Its 69 paths retain 58 reviewed
postimage blobs exactly. Eleven paths also contain current main changes:
CLAUDE.md, app shared snapshot, CLI main/spawn/mail hook, API options, daemon,
livedata board/loader/snapshot, and spawn. Only the mail-hook append conflicted;
both the activity-only PostgreSQL state hook and Mac mail helper are retained.
No unrelated history was merged.

PostgreSQL adaptations require independent current-delta review:

- PostgreSQL schema 1 upgrades to 2 transactionally under the existing migration
  lock. New databases create the existing core schema and then the place tables.
  Place/pairing changes publish through the existing transactional event trigger.
- Place dispatch locks its paired device and composes the existing canonical bead
  claim in the same transaction as the offer. A partial unique index enforces one
  offered/running run per place. Concurrent first pairing is serialized by name.
- Place comments use portable conflict handling; run IDs use RETURNING; integer
  flag values preserve SQLite export/digest compatibility. Imports accept schema
  22 and 23, including place rows for schema 23.
- Database presence reads use the place heartbeat for remote runs. Local process
  sampling cannot mark a healthy Mac absent. An offer is a known waiting dialog,
  not process presence. A heartbeat older than two minutes is unknown. Heartbeat
  presence does not create working-turn activity or bypass run-aware state hooks.

Verification commands include the existing fake-Mac `TestPlaceRunScenario`,
Electron runner/main/menu tests, app typechecking, targeted Go packages, the full
Go suite through a build slot, and the isolated cockpit startup/text driver.
`TestPostgresPlaceClaimAndPresence` and `TestPostgresPlacesUpgrade` require
`COMMANDER_POSTGRES_TEST_DSN` selecting **commander_trial**; each creates and
removes its own schema. Never point these at the selected live database.
The previous ownership-guard refusal to add a synthetic feature pane is retained;
startup/text is not feature-specific cockpit proof.

## Inputs required before any target activation

Source readiness does not establish a live Mac feature. No native Mac, SSH, live
PostgreSQL, installation, restart or activation is part of this preparation.
The owner must retain these target facts before a separately authorized rehearsal:

- Mac OS/CPU architecture and app build, discovered cmdr path/build revision,
  Claude executable/login capability, and native process-group/dialog support.
- Approved server URL/authentication and pairing custody. Server and Mac cmdr
  revisions must match; the app pins each accepted run to its recorded URL.
- User-approved task scope and pairing. A place run remains Claude-only and uses
  the Mac's own login; server account selection does not configure it.
- A private PostgreSQL backup and a tested schema-2-compatible rollback binary.
  The current main binary accepts only schema 1, so it refuses schema 2 after this
  upgrade. Rolling back by selecting old SQLite/Dolt files loses new writes and
  is not an option. Preserve PostgreSQL writes and repair forward, or prepare and
  independently verify a compatible rollback binary before migration.

Actual target capabilities, rollback artifacts, disposable PostgreSQL execution
results and feature-specific cockpit coverage must be reported explicitly as
present or missing. No historical CLEAN verdict supplies those missing facts.
