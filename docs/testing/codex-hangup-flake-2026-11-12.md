# Codex hangup interrupt flake disposition — 2026-11-12

Canonical execution: commander-il9.206.4.26.

Independent reviewer .206.4.25 reported V1 (P2): the integration failed the
existing graceful-interrupt assertion twice, while its main control passed
130/130. That original observation and attribution remain valid. This follow-up
reproduces the same failure on main at an equal observed rate; it does not erase
the original review or approve an installation.

## Controlled comparison

| Snapshot | Commit | Pass | Fail | Observed failure rate |
| --- | --- | ---: | ---: | ---: |
| Mac candidate review branch | ae6d1df5b4446b54d453bd6a85659c67f1e385b6 | 199 | 1 | 0.5% |
| Main, frozen at experiment start | ad47ec0d0b6ae2ca951058d611b4978d0eceb758 | 199 | 1 | 0.5% |

The candidate ran in its existing review worktree. Main ran from an exact
`git archive` of the recorded commit in the assigned execution's private
workspace; the live main checkout was not changed. These are Linux-host tests
of the Mac candidate source, not actual macOS execution.

Both used Go 1.27.3 linux/amd64, `GOMAXPROCS=4`, `-p 2`, the same inherited
non-identity environment, and the same machine under its ordinary ambient load
(load average sampled during execution: 16.98 / 17.28 / 18.11 on 16 CPUs).
No synthetic competing workload was started. Batches alternated candidate/main,
then main/candidate, to distribute time-dependent ambient load. Tests themselves
ran sequentially; compilation time is included below. No failed batch was
retried or excluded.

Every Go subprocess cleared `COMMANDER_RUN_ID`, `COMMANDER_RUN_SESSION`,
`COMMANDER_MANAGER_ROLE` and `TMUX`. Exact per-batch command:

```sh
env -u COMMANDER_RUN_ID -u COMMANDER_RUN_SESSION \
  -u COMMANDER_MANAGER_ROLE -u TMUX GOMAXPROCS=4 \
  go test -p 2 -json -count=50 -timeout 5m ./internal/chat \
  -run '^TestCodexBridgeHangupEndsCodexAndItsTool$'
```

| Batch | Snapshot | Pass | Fail | Wall seconds |
| --- | --- | ---: | ---: | ---: |
| 1 | candidate | 50 | 0 | 14.807 |
| 1 | main | 50 | 0 | 17.670 |
| 2 | main | 50 | 0 | 8.056 |
| 2 | candidate | 50 | 0 | 7.712 |
| 3 | candidate | 49 | 1 | 7.930 |
| 3 | main | 50 | 0 | 7.782 |
| 4 | main | 49 | 1 | 8.516 |
| 4 | candidate | 50 | 0 | 8.286 |

Both failures were exactly the V1 signature:

```text
codexbridge_stop_test.go:142: the bridge never asked Codex to end its turn:
stat .../turn-interrupted: no such file or directory
```

The process-stopped assertions preceding this marker check passed on both
snapshots, as did process-leak cleanup. The failing test took 0.120s on the
candidate and 0.100s on main. There were no startup timeouts, process-survival
failures, package timeouts or skipped runs. Six batches exited 0; the two
batches containing these failures exited 1, as expected.

## Disposition and causal limits

This is a demonstrated pre-existing main flake with equal observed failure
rates in the requested 200-run comparison. The sample does not prove exact
population-rate equality or rule out arbitrarily small differences. It provides
no evidence that the Mac candidate worsens V1.

`git diff --exit-code ad47ec0d ae6d1df5 -- internal/chat internal/agentrt
internal/testguard internal/pgbind` passed with no differences. The same check
against the review's original pre-candidate main cb89c79c also passed. Thus the
bridge, signal/process helpers and tests relevant to V1 are unchanged from
pre-candidate main.

A plausible race exists in the unchanged test synchronization: the fake provider
writes its `turn-started` file after writing a `turn/started` notification to the
pipe. `awaitHoldingCodex` observes that file and immediately signals the bridge,
but the bridge may not yet have consumed the notification and set `b.turn`.
`endTurn` returns immediately when `b.turn` is empty, before the launcher kills
the provider group. This explains how cleanup could succeed while the graceful
interrupt marker is absent. This is source-based analysis, not a traced proof of
the exact failing interleaving. No socket, Mac custody or migration cause was
established.

No runtime or test assertion was changed: the assigned acceptance explicitly
allows reporting an equal pre-existing flake. A future repair of this existing
signal/readiness race can be scoped separately. V1 is disposed here as an equal
baseline flake, not as a repaired graceful-interrupt guarantee. The original
review's V2 renderer floor and other release limitations remain outside this task.

## Evidence and verification boundary

Private evidence directory:
`/home/exedev/code/commander/.worktrees/exec-vm-22-45790f845912/.commander/hangup-investigation/`.
It contains `repeat.py` (exact controller), `results.json`, eight complete
`candidate-*.jsonl` / `main-*.jsonl` test logs, `sha256.json`, the frozen `main/`
archive, and `relevant-source.diff` (empty). The SHA-256 manifest is reproduced
below to bind this report to the full logs.

```json
{
  "candidate-0.jsonl": "20b62d2516a6afb7c600264e33a1e94ea001adf742b90cf877d7be24a5ee0e9a",
  "candidate-1.jsonl": "3b5e45ef9c09834c5310e449566025d60a5c0134b8409829e40b4cfe0b74a4c0",
  "candidate-2.jsonl": "b5fe661f90a6234bcb389aa6c14cf408b3605d50c0cab8c091c4c0f338d6f302",
  "candidate-3.jsonl": "34c9828b10bb5611280464605e0a6914c3949062852c0ab41e8ff5fc8906a198",
  "main-0.jsonl": "7f2dea2af921e0b5ebe156b6f771f04b2d8abba02d6c9375a095d9cc5bae3703",
  "main-1.jsonl": "1ab59cb08869f53d5253eb654939c5a88c684fd11f648df91edb3afd319fa2f3",
  "main-2.jsonl": "29fc781c85cf807180726ffd9508be6f2cc63acf3d134629489971b26ed552f8",
  "main-3.jsonl": "be15ed351317a0efd45f6c48f54929570ba8e93b04ab626a58d72d801e0d01b2",
  "repeat.py": "12c2eed319bb9438034277f2cadc1cc4b1db4d518f39db1b3a28c9b728f1ce44"
}
```

Only this report is a tracked change. Validation is the 400 targeted test
executions and source-identity checks above, plus `git diff --check` before
publication. No feature or interaction flow changed, so no full Go/eval suite or
cockpit session was rerun for this documentation-only disposition. The prior
review's broad-suite limitations remain recorded in
[the independent review](mac-install-independent-review-2026-11-12.md).

No installation, restart, main update, live schema change, actual Mac/model
launch, gate passage or bead closure was performed.
