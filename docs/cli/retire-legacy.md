# Retire redundant legacy stage markers

An ordinary stage can retain legacy gate and approver labels after a genuine
user approval gate was attached. The owning Manager, Commander or user can remove
those two redundant markers without granting approval:

    cmdr internal-gate retire-legacy --project sample --bead sample-flow.2 \
      --replacement sample-flow.3 --reason "Redundant legacy stage markers" --dry-run

Run the same command without --dry-run after inspecting the validation result.
The command removes only gate and approver:user from the stage. It never closes
either bead, answers a card, changes a wait, or modifies replacement data.

The stage must be an open ordinary objective with both legacy markers, no
separate card, no verifier or gate-of authority, and exactly one blocking wait
on the replacement. Both beads must share a real parent under open, unheld
objective ancestors. The replacement must be the sole attached gate (even a
second closed gate refuses), open, unheld, unproposed, and a user-verifier gate
with a retained live card matching its latest unanswered pitch. A current
answer journal or decision:answered marker refuses. Ambiguous or missing
evidence refuses; no dotted-ID ancestry fallback is used.

Before writing labels the door records gate-retire-legacy@v1 intent on the
stage, binding the exact request, actor, root and evidence digest. Between
writes it reads fresh evidence again. Retry the exact command as the same actor
after an interrupted write, lost response or completion-audit failure. An absent
marker counts as already removed only with matching retained intent. Changed
replacement evidence or request refuses recovery once either marker is absent.
A completion comment is idempotent. The ordinary comment door reserves this
audit prefix; generic label restrictions remain in place.

Label removal and validation are separate backend operations, not an atomic
compare-and-swap. A concurrent writer can change state after the last check;
post-write validation detects observed drift but does not roll back or restore
markers automatically. Coordinate with the owning Manager and inspect a refusal.
This command does not authorize cross-workstream cleanup.

Coverage: cmd/cmdr TestGateRetireLegacy*, TestRetireLegacyAuditReserved and
TestRetireLegacyScenario. The scenario reads
tests/evals/scenarios/retire-legacy.json and exercises the actual private command
handler with synthetic beads and an isolated real SQLite store; it asserts the
sole live card and byte-preserved replacement/card/pitch evidence. It does not
claim real-bd transactional or concurrent-writer isolation.
