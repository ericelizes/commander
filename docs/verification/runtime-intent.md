# Runtime intent and effective execution audit

This is an invented verification exercise: a catalog Operator explicitly chooses
Codex, temporarily executes on a Claude account, and later resumes its saved
Codex intent. The call-site and coverage references below describe the design;
they are not a record of a private branch or a completed verification run.

## Runtime writers (all production Go call sites)

| Caller | Writer | Purpose |
| --- | --- | --- |
| cmd/cmdr/runtime.go: runRuntime offline fallback | WriteRuntime | Explicit CLI runtime selection; records pin and effective runtime |
| internal/chat/runtime.go: Engine.SetRuntime | WriteRuntime | Explicit API/chat menu runtime selection, refuses busy process |
| internal/spawn/codex.go: resolveOperatorRuntime, nonempty req.Runtime | WriteRuntime | Explicit spawn/provider choice; pin survives subsequent hooks fallback |
| internal/spawn/codex.go: explicit or pinned resolution | WriteEffectiveRuntime | Actual launch runtime after hooks/account selection, preserves intent |
| internal/spawn/codex.go: automatic resolution | WriteAutoRuntime | Fresh service/share/default or unpinned respawn; no explicit intent |
| cmd/cmdr/account.go: accountUse offline fallback | WriteEffectiveRuntime | Claude account execution; preserves saved runtime choice |
| internal/chat/account.go: Engine.SetAccount | WriteEffectiveRuntime | Online account switch and autoswitch; preserves saved runtime choice |
| internal/chat/runtime.go: WriteRuntime | WriteEffectiveRuntime | Shared effective file/handoff implementation before saving explicit intent |
| internal/chat/runtime_share.go: WriteAutoRuntime | WriteEffectiveRuntime | Effective selection before clearing explicit pin and adding auto marker |
| internal/chat/runtime_share.go: dropDrawnCodex | WriteAutoRuntime | Automatic Manager returns to Claude at zero share; explicit and adopted hosts stay |
| internal/chat/runtime_share.go: AssignNewManagerRuntime | WriteAutoRuntime | Never-started Manager draw; existing runtime files untouched |

Rebalance goes through Engine.SetRuntime, then MarkRuntimeAuto. Managers retain
legacy marker semantics: their explicit-pin file does not override the auto
marker. Operators require a valid explicit-pin file. Account-only provider
requests pass Runtime empty, and the resolver selects effective Claude before
consulting the saved pin; a later ordinary respawn can recover pinned Codex.
Explicit runtime plus account remains an explicit runtime request.

## Coverage matrix

These are source coverage references, **not passing-test evidence** for this head.

| Behavior | Automated coverage |
| --- | --- |
| Fresh explicit launch and bridge guard args | spawn: TestSpawnCodeHost_CodexRuntimeRunsTheBridge |
| Explicit/resumed Codex without hooks, account switch, recovery | spawn: TestExplicitCodexRequiresHooksAndAccountSwitchKeepsIntent |
| Pinned versus automatic resume launcher | spawn: TestSpawnCodeHost_RespawnUsesPinnedRuntimeAndHooks |
| Shares, no hooks, explicit priority | spawn: TestOperatorRuntimeSplitsByBead |
| Service tasks stay automatically Claude | spawn: TestServiceTaskForcesClaudeOnACodexDraw, TestServiceTaskExplicitCodexWins |
| Hooks fallback handoff and restored hooks | spawn: TestOperatorFallbackHandoffAndRecovery, TestHooksFallbackNoteExplainsGuardReadiness |
| Provider account-only request has no explicit Runtime | cmdr: TestOperatorProviderRespawnsAManagersOwnOperator |
| Provider requested runtime from agent/old host environment | cmdr: TestOperatorProviderAllowsRequestedCodexFromAnAgent, TestOperatorProviderAllowsRequestedCodexFromAHostOperator |
| Engine account switch preserves Codex pin | chat: TestSetAccountFromCodexRunsClaudeOnThatAccount |
| Offline account switch preserves intent | cmdr: TestOfflineAccountUsePreservesRuntimeIntent |
| CLI runtime offline persistence and API request serialization | eval: TestRuntimeChoiceScenario, scenarios/runtime-choice.json (API fake does not assert persistence) |
| API runtime persistence/restart/refusal | chat: TestSetRuntimeRestartsARunningChatOnCodexAndBack, TestSetRuntimeOnAStoppedChatOnlyRecordsIt, TestSetRuntimeRefusesMidTurn |
| Rebalance respects pins, hooks and Commander preference | cmdr: TestRebalanceMovesIdleUnpinnedSkipsMidTurnNeverPinnedOrCommander, TestRebalanceNeverMovesToCodexWithoutHooks, TestRebalanceAtShareZeroNeverMovesToCodex |
| Mail uses actual fallback runtime | cmdr: TestMailPinnedCodexFallbackUsesClaudeClassification |
| Report folds hooks fallback into Claude | cmdr: TestRuntimeReportFoldsHooksFallbackIntoClaude |
| Legacy Operator file and per-role reassertion | spawn: TestOperatorLegacyRuntimeIsNotExplicit |
| Legacy Manager pins and automatic/adopted host behavior | chat: TestDropDrawnCodexAtShareZero, TestDrawnCodexHostIsAdoptedAsCodexAtShareZero; cmdr: TestRuntimePinning |
| Repeated effective write retains thread and intent | chat: TestEffectiveRuntimeIdempotencePreservesThreadAndIntent |
| Pause, usage limit, capacity and user runtime rules | prompts: TestRuntimeChoicesDoNotEvadeUserLimits |

## Verification gate

In the invented exercise, a busy test host cannot admit the verification job.
The Operator records that tests did not run and waits for capacity. Formatting
alone does not establish readiness: the final committed head must pass targeted
packages, the runtime-choice eval, a slot-controlled full suite, and isolated
detached cockpit text with a synthetic pin. Results from an older head do not
substitute for those checks. The exercise changes no real role pins or accounts.
