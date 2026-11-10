# Schema-one repair assembly

This is private source preparation on `op/assemble-commander-il9.204.6.53`,
starting at `77d816f0165025a1a9e610204e88aae62e6f9272`. It grants no release,
installation, restart, migration or production authority. Independent full
integration review must assess the actual union and the semantic resolutions.

## Source inventory

| Feature | Exact original head | Applied history |
| --- | --- | --- |
| Capacity | `39a2b5294df0400674f872072f90df11bf66db96` | one main-based commit |
| Sidebar failure ownership | `12200ec200e6b35f7e185b5d1b9208cadedccbe7` | seven commits from `11717b75` |
| Scoped PG review helper | `3e064c92c21c5d9c91a801ecc8448e3ebccc54b5` | `ca72ca82`, then handshake correction |
| Inspector focus | `68ebee93824879d4e5d8b1a687959f1a4184a548` | one commit |
| Baseline fixtures | `c37ae931a0a3c053618f2d7955b55856b750d74f` | `d53abd52`, then fixture corrections |
| Withdrawal (acceptance pending at preparation) | `e442a2f48b27316ff277d7515a86e2860c6ddb02` | `db4c96b6`, `5c7150f3`, inactive correction |
| Mail fixtures (acceptance pending at preparation) | `c7c59aa83b3070ca9583df41d7437c3b9fda07c6` | ONLY `d53abd52..c7c59aa8` delta |

No duplicate original baseline, Mac/schema-two, cross-project custody,
SIGINT/reparent or failed-peer policy changes are included. PostgreSQL schema
remains version one. Core PG fences, account planner, disk guard, process/cgroup
limits and secret scan remain authoritative.

## Integration decisions requiring review

The capacity patch adjusts the Manager to idle when exhausted; the sidebar
patch removes the unread/active Manager recoloring of failed handoffs. The
`row` conflict retains capacity's idle adjustment and sidebar's removal.
Automatic merging also reintroduced the obsolete override inside `trackLevel`.
The combined board golden failed before correction. The integration removes
that track override, preserving capacity's per-nested-owner recursion, collapsed
owner inheritance and final capacity decoration. A duplicate `trackWorkstream`
owner assignment introduced by the union is removed.

Capacity's original unit/eval expected unread mail to produce “Manager picking
it up” after clearing capacity. That conflicts with the sidebar failure rule.
Combined unit and JSON-backed eval now require stalled rows/tracks with recovery
ownership after capacity clears. This is an explicit semantic integration delta,
not original-head equivalence or an inherited CLEAN verdict. Existing capacity
suppression of user asks, external waits and live Operators remains tested.

Both CLAUDE append conflicts retain the original automation-failure, scoped
review-bootstrap and withdrawal paragraphs, separated by blank lines. Other
capacity CLAUDE edits retain the upstream account/start semantics. Operational
beads from sidebar and withdrawn filtering both remain in livedata projection.

## Evidence and checks

Private `.commander/assembly/coverage.json` records original and union blob IDs
for every feature path; original patches and every differing original-to-union
file diff are retained alongside it. Exact production blobs cover inspector,
scoped helper, withdrawal implementation and mail fixtures. Shared board and
projection functions require the integration review above; no blanket patch
identity is claimed. The final report records final-head equivalence counts.

Targeted checks include combined capacity/recovery units and board goldens,
withdrawal active/inactive CLI scenarios, inspector scenario and store tests,
own-run checks, review context sealing/private-Git scenarios, automation-failure,
reported-gap, nested capacity and private spawn-mail evals. Renderer checks run
`derived-diagram.test.tsx` and `overview.test.tsx`. Raw logs retain failures before
correction and later results. A no-tests-selected result is not coverage.
Private cockpit up/text/down is startup smoke only; it does not establish native
PG withdrawal or actual installed-app behavior. No configured live PG test,
real-mail credential test, paid actor or QA wake is authorized.

Do not freeze or run the final full suite until both required source reviews
are CLOSED with valid exact-head clear receipts. A changed source head requires
an explicit exact delta and renewed review. Once accepted, freeze a candidate,
record its environment/head, and run one isolated slot-held full suite with
process/cgroup guards intact and bounded package watchdog (20 minutes permitted
for documented long evals). No assertion weakening or unbounded retry.

## Delivery topology and staged inputs

Read-only observation on this host found installed `cmdr` and `cmdrd` at
`/home/exedev/.commander/bin`, reached through `.local/bin` links, both stamped
`77d816f0165025a1a9e610204e88aae62e6f9272`, `vcs.modified=false`.
The running daemon executable matched that location. `REPO` points at the main
checkout. A live Vite renderer process runs from the main checkout's `app`,
serving the existing source, not this worktree. No user assets were changed.
The observation, SHA256s and selected non-secret delivery environment are in
`delivery-readonly.json`.

Private candidate Go artifacts are `.commander/assembly/staged/{cmdr,cmdrd,VERSION}`;
renderer build output is this worktree's `app/dist`, with Electron main/preload
under `app/out`. Both Go binaries require exact candidate revision and
`vcs.modified=false`. `artifacts.json` records their SHA256/build provenance and
all built asset hashes. Dependencies are borrowed read-only via an ignored
`app/node_modules` link; build outputs stay in the worktree. Builds use a slot.

A later authorized delivery must account for BOTH Go and renderer paths.
`daemon.appDir` serves `COMMANDER_APP_DIR`, else main-checkout `app/dist`;
`appDevURL` defaults to the loopback Vite server, which takes precedence while
healthy. Electron loads `ELECTRON_RENDERER_URL` when supplied, otherwise its
watched dev renderer or bundled `dist/index.html`. Go installation alone cannot
update that renderer. A separate decision must select and verify the actual
renderer route, deliver reviewed assets there, and account for desktop bundles
on any remote client. This preparation does not copy into main `app/dist`,
change a live dev server, install an Electron bundle or assume a remote route.

## Rollback limits

Private `rollback77/{cmdr,cmdrd}` preserves byte copies of the observed compatible
schema-one binaries, with hashes tied to the observation. `rollback-source/app`
is exported from exact baseline 77 and built privately for compatible renderer
inputs; it is not copied from a possibly stale main `dist`. Hashes are retained
with candidate assets. The installed `.prev` names an older revision and is NOT
assumed to be this compatible pair. Any later installation needs an explicit
atomic paired-binary and matching-renderer plan, with version/hash verification
and a separately authorized restart. These are inputs, not executable approval.

After new PostgreSQL writes, never restore retired SQLite/Dolt state or an old
PG snapshot blindly. Keep current PG data and repair forward or use a compatible
binary; backend rollback requires explicit reconciliation. Withdrawal writes
`withdrawn` labels and removes active asks without granting approval. Baseline
77 lacks those semantics: rolling its binaries back while such labels/state
remain may expose or run an obsolete gate. Quiesce relevant mutation paths and
reconcile withdrawal labels, asks, intents and current dependencies under a
separate authorized plan before using baseline behavior. Do not silently remove
labels or recreate asks as a binary rollback side effect. No automatic state or
withdrawal rollback is claimed.
