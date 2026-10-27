> **Reviewed change specification / historical planning record.** Implementation status, line numbers, former behavior and open-question wording below record the planning pass. Owner rollout decision: previous-ID runs are not accounted for; every pool starts in dry run and real growth may begin only after verifying none remain open. The [current design](../design/herzi-design.md) and [operating guide](../ops/operating.md) describe the implemented contract. Retired terms below exist only to specify their migration and deletion.

# Plan: add-only controller and the merged workflow ids

Produced by a read-only Codex planning pass (2026-10-27), reviewed by the lead. Owner decisions: (1) follow the
merged workflows ids `install-server-<mce>_<infraEnv>_<slot>` / `install-server-<mce>_<server>`; (2) the controller
only ADDS servers: no ceiling, no removal, no `lowPct`/`minBand`; one percentage `highPct` (default 30) is the
single target buffer.

**1. New behavior and arithmetic**

herzi will only add servers. For each managed pool, `buffer = max(minFree, (highPct*demand+50)/100)`, `target = demand + buffer`, and `need = target - (assigned + claimable + settling + open runs)`, using integer half-up rounding. With valid observations and an unblocked pool, positive need enters the existing Full/Probe growth protocol; otherwise the decision is `Hold("at or above target")`. There is no ceiling or removal intent. `highPct` remains 30 and `minFree` remains 2. Workflow IDs become `install-server-<mce>_<infraEnv>_<slot>` and `install-server-<mce>_<lowercased-server>`; the separator immediately after `install-server` is `-`, and the separator immediately after the MCE is `_`.

| Demand | Rounded percentage calculation | Buffer | Target |
|---:|---|---:|---:|
| 0 | `(30×0+50)/100 = 50/100 = 0` | `max(2,0)=2` | `0+2=2` |
| 4 | `(30×4+50)/100 = 170/100 = 1` | `max(2,1)=2` | `4+2=6` |
| 6 | `(30×6+50)/100 = 230/100 = 2` | `max(2,2)=2` | `6+2=8` |
| 10 | `(30×10+50)/100 = 350/100 = 3` | `max(2,3)=3` | `10+3=13` |
| 12 | `(30×12+50)/100 = 410/100 = 4` | `max(2,4)=4` | `12+4=16` |
| 20 | `(30×20+50)/100 = 650/100 = 6` | `max(2,6)=6` | `20+6=26` |
| 50 | `(30×50+50)/100 = 1550/100 = 15` | `max(2,15)=15` | `50+15=65` |

**2. File-by-file changes**

Verified through repository-wide `rg` searches, including case-insensitive searches and hidden files outside `.git`, for every requested term, plus `HoldInsideBand`, `"inside band"`, `15%`, ID consumers and string parsing. The changes below are proposed; none have been implemented.

**Workflow IDs**

| File and current location | Planned change |
|---|---|
| [internal/ids/ids.go:16](/home/toto/code/herzi/internal/ids/ids.go:16) | Change `prefix` to `"install-server"` **and change each builder’s first separator to `"-"`**. Merely changing the constant would incorrectly produce `install-server_<mce>_...`. `PoolSlot` becomes `prefix+"-"+mce+"_"+infraEnv+"_"+slot`; `Named` retains `strings.ToLower(server)`; `PoolPrefix` becomes `install-server-<mce>_<infraEnv>_`. Rewrite package examples and separator explanation. |
| [internal/ids/ids_test.go:7](/home/toto/code/herzi/internal/ids/ids_test.go:7) | Replace all four old-prefix expectations. Expand the Python-pinned tests as listed in section 6. Keep existing collision and hub/slot distinction tests. |
| [internal/temporalx/sweeper.go:108](/home/toto/code/herzi/internal/temporalx/sweeper.go:108) | No ordinary production change needed: Describe already calls `ids.PoolSlot`. Verify the acquisition-history lookup at line 279 uses the same new workflow ID and the described run ID. |
| [internal/temporalx/temporalx_test.go:89](/home/toto/code/herzi/internal/temporalx/temporalx_test.go:89) | Strengthen `TestStatusMappingAndIDs` with literal final-ID expectations. Currently it compares against the same Go builder, which cannot independently detect an incorrectly changed builder. Its final-underscore slot parsing remains valid. |
| [internal/temporalx/round2_test.go:84](/home/toto/code/herzi/internal/temporalx/round2_test.go:84) | Pin the literal final workflow ID in acquisition-history expectations, retaining run-ID pinning/cache assertions. |
| [docs/PLAN.md:20](/home/toto/code/herzi/docs/PLAN.md:20) | Replace old workflow-ID decision. |
| [docs/design/herzi-design.md:234](/home/toto/code/herzi/docs/design/herzi-design.md:234) | Replace both ID examples, growth-protocol Describe example at line 439 and decision-log entry at line 723. Delete the blanket statement at line 241 that old `install-server-*` IDs are ignored: the authoritative current IDs now also begin that way. Explain exact slot IDs versus named IDs and document the rollout policy. |

These are **all files containing `add-server-to-mce`**: `internal/ids/ids.go`, `internal/ids/ids_test.go`, `docs/PLAN.md`, and `docs/design/herzi-design.md`.

The authoritative Python definitions are [workflow_ids.py:65](/home/toto/code/workflows/shared/workflow_ids.py:65) and [workflow_ids.py:94](/home/toto/code/workflows/shared/workflow_ids.py:94); pinned examples and isolation assertions are in [test_bond_selection.py:323](/home/toto/code/workflows/tests/test_bond_selection.py:323). No changes to the workflows repository are required.

`PoolPrefix` has no production caller in the searched herzi tree. Production discovers runs by exact fixed IDs, without parsing names or listing by prefix. `Named` remains useful as a contract builder; named installations remain outside pool-slot accounting.

**Pipeline and configuration**

| File and current location | Planned change/deletion |
|---|---|
| [internal/pipeline/types.go:154](/home/toto/code/herzi/internal/pipeline/types.go:154) | Delete `WouldRemove`, its `Key`, `Surplus`, `Total`, `Target`, `Ceiling` fields **as members of that deleted type**, and `(WouldRemove).isIntent`. Retain `Grow`, `Hold`, `PoolFacts.Settling`, `State` and all ports. |
| [internal/pipeline/decide.go:10](/home/toto/code/herzi/internal/pipeline/decide.go:10) | Delete `HoldInsideBand` and string `"inside band"`; replace with `HoldAtOrAboveTarget = "at or above target"`. Replace `low`/`high`/`ceiling` calculations with the single buffer formula. Delete the complete `total > ceiling` branch, `surplus` calculation and `WouldRemove` return. Retain the early `need <= 0` return **before** failure/probe logic. `total` may remain as a supply subtotal; it is not inherently removal-specific. |
| [internal/pipeline/helpers_test.go:13](/home/toto/code/herzi/internal/pipeline/helpers_test.go:13) | Delete `LowPct` and `MinBand` fixture fields; retain `HighPct:30`, `MinFree:2` and timing settings. |
| [internal/pipeline/decide_test.go:12](/home/toto/code/herzi/internal/pipeline/decide_test.go:12) | Rewrite design table, buffer tests and properties; replace removal test cases as detailed in section 6. Delete test-only `low`/`high` pairs and minimum-band arithmetic. Use one explicit `buffer` for custom-buffer rows, implemented with `HighPct=0`, `MinFree=buffer`. Make the large-wave row use the new default buffer 15: `Grow 65`, Plan 15. |
| [internal/pipeline/attribute_test.go:169](/home/toto/code/herzi/internal/pipeline/attribute_test.go:169) | Replace three `HoldInsideBand` expectations in `TestSnapshotSafetyInvariants`; preserve reconstruction, settling and open-run assertions. |
| [internal/config/types.go:29](/home/toto/code/herzi/internal/config/types.go:29) | Delete `PoolConfig.LowPct`, `PoolConfig.MinBand`, `PoolOverride.LowPct`, `PoolOverride.MinBand` and their JSON tags. Update the configuration example at line 64 to `highPct:30`. Explain `HighPct` as the target buffer percentage. |
| [internal/config/defaults.go:7](/home/toto/code/herzi/internal/config/defaults.go:7) | Delete default values `LowPct:15`, `MinBand:1` and both corresponding `merge` branches. Preserve explicit-zero handling for `HighPct` and `MinFree`. |
| [internal/config/load.go:26](/home/toto/code/herzi/internal/config/load.go:26) | Delete `poolInput.LowPct`, `poolInput.MinBand`, their YAML tags and `convert` assignments. Preserve strict loading. Add explicit obsolete-key diagnostics as described in section 4, without restoring those fields as supported configuration. |
| [internal/config/validate.go:103](/home/toto/code/herzi/internal/config/validate.go:103) | Delete checks `LowPct>=0`, `LowPct<=HighPct`, and `MinBand>=1`, including diagnostic field names `"lowPct/highPct"` and their messages. Retain `HighPct>=0`, `MinFree>=0`, load limits and every timing relation. There is no separate ceiling validation field or rule to retain. |
| [internal/config/store.go:75](/home/toto/code/herzi/internal/config/store.go:75) | Delete the two obsolete pointer-clone assignments. Preserve snapshot ownership and last-good behavior. |
| [internal/config/config_test.go:153](/home/toto/code/herzi/internal/config/config_test.go:153) | Rewrite `TestGoldenSample`, `TestMergeEveryField`, `TestValidatePoolRules`, `TestValidationCollectsAllProblems`. Delete obsolete struct fields, YAML keys and validation rows. Replace invalid-pool fixtures with `highPct:-1` or `minFree:-1`. Keep two independently identified pool errors in the aggregate-errors test. Extend `TestLoad` with obsolete-key rejection cases. |
| [internal/config/store_test.go:65](/home/toto/code/herzi/internal/config/store_test.go:65) | Delete obsolete pointers from `TestStoreOwnsInitial`; extend ownership assertion to `HighPct` so the remaining percentage’s pointer is explicitly guarded. |
| [internal/config/round2_test.go:69](/home/toto/code/herzi/internal/config/round2_test.go:69) | In `TestF10InvalidPoolDoesNotRejectPauseUpdate`, replace `minBand:0` with `highPct:-1`; assert that accepted document still pauses A and publishes B’s semantic error. |
| [internal/config/sample_manifest_test.go:27](/home/toto/code/herzi/internal/config/sample_manifest_test.go:27) | Change the example override assertion from `.LowPct==25` to `.HighPct==25`. |
| [internal/config/testdata/sample.yaml:18](/home/toto/code/herzi/internal/config/testdata/sample.yaml:18) | Delete `lowPct`, `minBand`; change the example pool override to `highPct:25`. |
| [config/samples/herzi-config.yaml:17](/home/toto/code/herzi/config/samples/herzi-config.yaml:17) | Same key deletions and override change; retain all dry-run/paused safeguards. Explain that 25 is that example pool’s target percentage. |

`internal/pipeline/{attribute,validate,plan,state,ports,errors}.go`, `internal/config/watch.go`, and their otherwise unaffected tests need no production behavior change.

**Controller and metrics**

| File and current location | Planned change/deletion |
|---|---|
| [internal/controller/reconciler.go:198](/home/toto/code/herzi/internal/controller/reconciler.go:198) | Delete the `case pipeline.WouldRemove`, `"Would remove machines"` log, its `surplus`/`ceiling` fields and metric call. Keep Hold and Grow execution. |
| [internal/controller/reconciler.go:227](/home/toto/code/herzi/internal/controller/reconciler.go:227) | Replace duplicated low/high formula in `observe`; remove `ceiling` argument. Preserve the existing nonnegative **metric** need, `max(0, signedNeed)`. The decision’s signed need remains the expression in section 1. Prefer a small pure target helper shared with `Decide` to prevent future formula divergence. |
| [internal/controller/reconciler_test.go:164](/home/toto/code/herzi/internal/controller/reconciler_test.go:164) | Rename `"remove"` to `"above target"`; delete its removal-counter assertion and assert no POST plus correct target/need gauges. Replace `Defaults.MinBand=0` in `TestBlockedPools` with `Defaults.HighPct=-1`. Add demand-20 coverage that distinguishes the new target from the old one. |
| [internal/controller/round2_test.go:56](/home/toto/code/herzi/internal/controller/round2_test.go:56) | `TestF1SlowRecheckCannotSpendStaleSweep`: demand 10/supply 10 now orders **3**, not 2; retain the delayed-recheck race. At line 214, replace `minBand:0` with `highPct:-1` in `TestF10ConfigErrorBlocksOnlyInvalidPool`. |
| [internal/controller/envtest_reconciler_test.go:115](/home/toto/code/herzi/internal/controller/envtest_reconciler_test.go:115) | Replace 15% comments with 30%. Existing demand-4/7/8 counts remain unchanged because the two-machine floor still wins or ties. Add a demand-10 or demand-20 transition to exercise the changed formula through watches. |
| [internal/metrics/metrics.go:26](/home/toto/code/herzi/internal/metrics/metrics.go:26) | Delete field `removes`, registration of `herzi_would_remove_total`, method `(*Metrics).WouldRemove`, its `ForgetPool` cleanup, gauge-map entry `"ceiling"` / `herzi_pool_ceiling`, and the `ObservePool` `ceiling` parameter/value. Keep `machines` and every other metric. |
| [internal/metrics/metrics_test.go:12](/home/toto/code/herzi/internal/metrics/metrics_test.go:12) | Update every `ObservePool` call, remove removal-counter invocation/catalogue entry and `"ceiling"` gauge expectation. Replace `HoldInsideBand`. Explicitly assert both removed metric families are absent after observations. Retain ForgetPool/cardinality/registration tests. |
| [internal/metrics/round2_test.go:28](/home/toto/code/herzi/internal/metrics/round2_test.go:28) | Remove obsolete `ObservePool` argument; preserve unknown-class assertions. |
| [cmd/main.go:98](/home/toto/code/herzi/cmd/main.go:98) | No ID/formula wiring change is needed. For the already documented round-2 contract, add `stuckAfter` to restart-required diagnostics. If exposing obsolete-key reload diagnostics, log only a typed, sanitized migration message; do not log arbitrary decoder errors. |
| [cmd/main_test.go:23](/home/toto/code/herzi/cmd/main_test.go:23) | Extend `TestStartupBoundChanged` and `TestReloadAcceptedOnlyAndRestartDedup` for those diagnostics, retaining rejected-update/no-requeue behavior. |

`internal/controller/gather.go` and `watches.go` need no change: attribution, cache scope and reconcile keys are independent of ceilings and ID spelling. No installer request schema, queues, RBAC, CRDs, dependencies or generated files need changing.

**Documentation, alerts and historical records**

| File and current location | Planned change/deletion |
|---|---|
| [docs/PLAN.md:9](/home/toto/code/herzi/docs/PLAN.md:9) | State permanent add-only scope. Delete shrink/cooldown backlog language; retain VM creation and route-idempotency backlog. Delete `_ceiling` and `herzi_would_remove_total` from the metrics catalogue at lines 71/77. |
| [docs/design/herzi-design.md:27](/home/toto/code/herzi/docs/design/herzi-design.md:27) | Replace opening formula and buffer table. Remove ceiling/removal prose. Recompute worked examples, not only the formula: case 1 needs three free Agents at demand 10; case 2 target becomes 21 at demand 16; case 3 target becomes 65; case 4 target at demand 12 becomes 16 and the shrink sentence disappears. Retain settling accounting. |
| [docs/design/herzi-design.md:282](/home/toto/code/herzi/docs/design/herzi-design.md:282) | Delete pseudocode `WouldRemove` type and execution branch. Replace Stage 3 formula/return at line 380; update config at line 512 and validation at line 544. |
| [docs/design/herzi-design.md:568](/home/toto/code/herzi/docs/design/herzi-design.md:568) | Delete surplus/removal invariant; replace with “at/above target always Holds, regardless of supply composition.” Rewrite the test table with one buffer column, matching Go tests. |
| [docs/design/herzi-design.md:625](/home/toto/code/herzi/docs/design/herzi-design.md:625) | Part 3: rewrite row 12 around a single buffer floor; rewrite L3 solely around orphan bindings; delete L9’s band-width solution while retaining half-up rounding coverage. Update numeric examples such as `need 58`. Delete Cooldown decision-log row and both cooldown/real-shrink backlog bullets at lines 730–731. State add-only behavior and persistent excess capacity explicitly. |
| [README.md:21](/home/toto/code/herzi/README.md:21) | Replace shrink status/backlog statement with add-only scope. Delete `lowPct`/`minBand` config rows; redefine `highPct` and `minFree`. Replace two-buffer decision table and removal outcomes. Delete both removed metrics. Include the formula/default-buffer change and migration instructions. |
| [docs/ops/operating.md:38](/home/toto/code/herzi/docs/ops/operating.md:38) | Replace obsolete validation rules; list all round-2 timing relationships. Update pending-marker/state explanation, acquisition proof, upgrade/rollback instructions for deleted config keys and workflow-ID transition. Explain that lowering demand or buffer settings stops additions but does not reclaim machines. |
| [docs/ops/runbook.md:44](/home/toto/code/herzi/docs/ops/runbook.md:44) | Preserve shortage/failure/block/orphan procedures. Require acquisition evidence for running-run recovery proof; replace fixed stuck threshold at line 110 with startup-configured threshold. Add obsolete-key remediation under config-invalid and explain normal excess free capacity versus excess bound Agents. |
| [config/prometheus/herzi-alerts.yaml:74](/home/toto/code/herzi/config/prometheus/herzi-alerts.yaml:74) | **Verified: no ceiling/removal alert exists to delete.** Preserve orphan alert. Replace hard-coded `>7200` with the round-2 threshold gauge comparison and remove fixed-two-hour wording. Since the threshold is an unlabeled gauge, use explicit vector matching: `herzi_oldest_open_run_seconds > ignoring(pool) group_left herzi_stuck_after_seconds`. Validate with promtool cases. |
| [docs/ops/packaging.md:1](/home/toto/code/herzi/docs/ops/packaging.md:1) | Already historical. Add a brief supersession note that current configuration has nine pool fields and point to current alerts/config documentation; preserve its recorded past verification. |
| [docs/design/inventory-controller-original.md:24](/home/toto/code/herzi/docs/design/inventory-controller-original.md:24) | Mark prominently as superseded historical input; link current add-only design. Its low/high, removal and cooldown passages must not remain apparent current instructions. |
| [docs/review/controller-review.md:129](/home/toto/code/herzi/docs/review/controller-review.md:129) | Add a historical/supersession note identifying the former shrink behavior as retired. |
| [docs/review/independent-review-round-2-findings.md:1](/home/toto/code/herzi/docs/review/independent-review-round-2-findings.md:1) | Preserve the explicitly verbatim review; add a note that its ceiling/minBand/formula examples describe the reviewed revision. |
| [docs/review/independent-review-round-2-fixes.md:10](/home/toto/code/herzi/docs/review/independent-review-round-2-fixes.md:10) | Add an addendum explaining changed fixture arithmetic and obsolete `minBand` examples; retain the safety contracts at lines 29–57. |

Search hits about deleting files, cache eviction, scaffold markers, stale-label repair, unmanaging pools or `unstructured.RemoveNestedField` are unrelated and stay. This includes `AGENTS.md`, `test/utils/utils.go`, `test/e2e/README.md`, `internal/kube/transform.go`, `internal/config/watch_test.go`, and the cleanup comment in `envtest_block_test.go`. `docs/ops/rbac.md` remains accurate.

Historical documents should retain evidence, with explicit supersession notices. Active code/config/docs must contain no removal mechanism or future shrink promise.

**3. Behavior that must not change**

The current safety contracts are verified in [round-2 fixes:29](/home/toto/code/herzi/docs/review/independent-review-round-2-fixes.md:29).

- **Probe mode:** one-machine probes, schedule persistence, unknown failures preventing Full spending, three distinct other failures, acquisition proof and post-failure start ordering stay intact. Guarded by `TestProbeProofTable`, `TestF5UnknownHistoryNeverAllowsFull`, `TestF6ProbeMustAcquireMachine`, `TestF9OldHungWorkDoesNotStarveProbe` and Temporal acquisition tests.
- **Early return:** retain `need<=0` before probe checks. A full pool with old failures must Hold without another probe. Removing the ceiling must not accidentally make every failure trigger a probe.
- **Settling:** retain attribution, `SettlingMax`, subtraction from need, stuck-agent transition and both gauges. Guarded by `TestAttributeAgentRules`, `TestSnapshotSafetyInvariants`, `TestAgentBuckets`.
- **Durable state:** retain bounded JSON, distinct run-ID deduplication, monotonic merge, failure expiry and uncached merge before writes. Guarded by `TestF4SlotReuseRetainsCapacityEvidence`, `TestF4HundredDistinctFailuresReusingTwoSlots`, `TestF4StateDegradationAndMerge`, `TestF4StatePersistsAcrossRestartAndProbePrewrite`, `TestF4CachedStateCannotEraseDurableFailures`.
- **Pre-POST marker:** retain durable prewrite, failure preventing POST, cancellation-detached bounded patches, null clear and outcome-time extension. Guarded by `TestF2PendingMarkerPrecedesPOSTAndSurvivesCancellation`, `TestF2ExactClearAndPrewriteFailure`, `TestF2OutcomeTimeExtendsMarkerAndPatchError`.
- **Fresh sweep:** retain observation strictly newer than the previous attempt, checked after uncached recheck; invalidate after every attempted POST. Guarded by `TestF1SlowRecheckCannotSpendStaleSweep`, `TestWatchBurstCannotSpendAgainstCachedSweep`, and the three Temporal F1 invalidation tests. Only F1’s demand-10 order count changes.
- **Pool isolation:** retain identity-first parse errors, pool-local semantic errors and hub-wide API failure behavior. Guarded by `TestF8MalformedAgentIsolatedToItsPool`, `TestF10ConfigErrorBlocksOnlyInvalidPool`, `TestPoolIsolationAndForget`, `TestMappings`. Replace deleted-key semantic-error fixtures with supported invalid values; otherwise these tests would exercise whole-document rejection instead.
- **Load controls and timing:** retain `maxSlots==100`, open-run headroom, API count cap 20, one request per pass, `failureMemory>=2*probeInterval`, `drawGrace<probeInterval`, `settlingMax>=probeInterval`, and `interval<=probeInterval`. Guarded by `TestF3AllHundredSlotsMustRemainVisible`, `TestF7TimingRelationships`, `TestPlanLimits`, `TestPlanProperties`.
- **Observation and execution:** preserve blocked-pool gauges, bounded labels, cleanup on unmanage/delete, frozen configuration/clock, recheck caps, dry run and leader-election behavior. Existing controller hardening tests guard these.

**Orphan alert remains meaningful.** [HerziAssignedExceedsDemand](/home/toto/code/herzi/config/prometheus/herzi-alerts.yaml:113) compares **bound Agents** with active demand. Ordinary accumulated spare machines are claimable/settling, not assigned. Sustained excess assigned Agents still suggests stale bindings, deleted/paused NodePools or upgrade effects; it is an investigation signal, not proof of orphans. Preserve its three-hour duration and alert-only behavior. Also, `assigned>demand` need not mean no growth: demand 20, assigned 21, no other supply still has need 5 against target 26.

**4. Config compatibility and defaults**

Recommend **explicit rejection with a clear migration error**, rather than silently ignoring:

> `defaults.lowPct was removed: use highPct as the single target buffer percentage; delete lowPct`

> `pools.<name>.minBand was removed: the controller no longer has a ceiling; delete minBand`

Detect key presence, including `null` and zero values, at the supported defaults/pool paths using YAML-node inspection. Then retain `KnownFields(true)` for all other keys. Do not keep obsolete fields in `PoolConfig`, `PoolOverride` or the supported loader schema.

Reasons:

- Silent ignoring could accept a typo or deploy substantially different capacity behavior without an operator noticing.
- Deleted keys are document/schema errors, including under one pool. They reject the entire update and retain last-good configuration.
- Supported keys with invalid merged values remain pool-local semantic errors.
- Startup with obsolete keys fails; migration must accompany deployment. Rollback instructions must restore the corresponding old config as well as the image.
- Live reload currently hides raw decoder errors in [cmd/main.go:254](/home/toto/code/herzi/cmd/main.go:254). Surface only a safe typed migration diagnostic, preserving error-redaction tests.

Keep default `highPct:30`, as the owner specified. At demand 20:

- Before: `max(2,(15×20+50)/100)=3`; target **23**, three spare.
- After: `max(2,(30×20+50)/100)=6`; target **26**, six spare.

This is an intentional capacity increase. An operator wanting the former target can explicitly set `highPct:15` after deleting obsolete keys. Do not automatically translate configurations to 15.

**5. Ordered work plan and acceptance checks**

1. **IDs first.** Update builders, Python-pinned examples and independent sweeper assertions.  
   Acceptance: `go test ./internal/ids ./internal/temporalx`; literal IDs match Python; hub-prefix isolation holds; no active old-prefix examples remain. Record the rollout decision before enabling growth.

2. **Pipeline/config contract together.** Remove obsolete fields/type/reason; implement the target formula and migration diagnostics; rewrite pipeline/config tests and fixtures.  
   Acceptance: IDs, config and pipeline package tests pass; seven buffer rows match section 1; at/above target never grows; obsolete keys fail clearly; invalid supported pool values remain isolated. This is a contract-changing intermediate step: downstream controller/metrics may not compile until step 3.

3. **Controller/metrics integration.** Delete removal execution/collectors, update observation formula/signatures, fix controller fixtures, strengthen formula/gauge tests and update command diagnostics.  
   Acceptance: targeted controller/metrics/cmd tests and round-2 regressions pass; demand 20 produces target 26; removed families are absent; stale sweeps still cannot spend. Run envtest with configured assets and verify the suite actually executes.

4. **Docs/config/alerts.** Update all active documentation, sample configuration and alert/runbook consistency; label historical records.  
   Acceptance: sample loader tests, manifest rendering, promtool rule validation and alert cases pass; active docs/test tables agree with code; searches find obsolete terms only in intentional migration tests/messages or clearly historical material.

5. **Final integration verification.** Run repository-required `make lint-fix`, `make test`, integration tests and relevant manifest/alert checks in the implementation workspace; review formatter changes. Run build/vet and the round-2 concurrency race checks. No regeneration is necessary unless markers change, which this plan does not require.

Safe parallel work, once contracts are agreed:

- Pipeline implementation/tests and config implementation/tests touch disjoint files; agree new field/reason/helper names first.
- Controller and metrics edits can run concurrently after agreeing the new `ObservePool` signature.
- During step 3, prose/alert work can proceed in disjoint files. Keep the sample/config fixtures with the config work to avoid competing edits.
- ID tests in `temporalx` and controller/metrics work are disjoint.

These are opportunities for implementation work; no agents were delegated for this planning task.

**6. Risks and exact tests to add or rewrite**

Primary risks are wrong ID punctuation, self-referential tests, invisible pre-migration runs, divergent decision/metric formulas, accidentally probing a satisfied pool, weakening timing validation, converting pool-local errors into document rejection, and forgetting the larger default target.

Exact proposed tests:

| File | Test changes |
|---|---|
| `internal/ids/ids_test.go` | Rewrite `TestPinnedToThePythonBuilders` with Python literals: `install-server-ocp4-mce-tlv-01_dell-r650-tlv-64c-1024gb_1`, `install-server-ocp4-mce-tlv-01_ocp-dell-x`, and lowercased HP serial. Rewrite prefix expectation to `install-server-ocp4-mce-tlv-01_ie_`. |
| Same | Keep `TestHyphensCannotMakeTwoPoolsShareAnId`, extend `TestDifferentHubsAndSlotsDiffer` to different InfraEnvs. Add `TestHubPrefixDoesNotMatchExtendedHubName`, `TestEveryRunOfHubSharesHubPrefix`, `TestNamedServerCaseDoesNotChangeID`, `TestDifferentNamedServersDiffer`, `TestNamedAndPoolIDsDiffer`, `TestPoolPrefixMatchesOnlyItsPool`. Copy corresponding Python inputs, including `ocp4-a`/`ocp4-a-b` and HP serials 1592/1593. |
| `internal/temporalx/temporalx_test.go` | Rewrite `TestStatusMappingAndIDs` to assert literal new IDs; preserve status mapping and slot parsing. |
| `internal/temporalx/round2_test.go` | Strengthen `TestF6LazyAcquisitionProofAndCache` with literal workflow-ID and run-ID history checks. |
| `internal/pipeline/decide_test.go` | Rewrite `TestDecideDesignTable`, `TestDecideBuffers`, `TestDecideProperties`; retain `TestProbeProofTable`. Delete `TestShrinkGuards`, replacing it with `TestAtOrAboveTargetAlwaysHolds`. |
| Same | Delete/replace subtest names `"03 remove four"`, `"05 no removal with running"`, `"07 no removal with settling"`, `"21 ceiling equality"`, `"22 above ceiling"`, `"no free victims"`, `"cap by claimable"`, `"settling prevents shrink"`, `"open prevents shrink"`. Use above-target, settling-supply and target-boundary cases instead. |
| Same | Add `TestTargetBoundary` for supply `target-1`, `target`, `target+1`; `TestSatisfiedPoolDoesNotProbe` with capacity/unknown/three-other failure evidence; `TestZeroBuffer` for `highPct=0,minFree=0`; retain exact-half rounding (`demand=5,highPct=10,minFree=0` gives target 6). |
| Same | Properties: use only `HighPct`/`MinFree`; assert exact Hold when need≤0, exact Full need when healthy and positive, Probe count 1, determinism and nonincreasing order count when assigned/claimable/settling/open supply increases. |
| `internal/config/config_test.go` | Rewrite `TestGoldenSample`, `TestMergeEveryField`, `TestValidatePoolRules`, `TestValidationCollectsAllProblems`. Add `TestRemovedPoolKeysRejected`: defaults and pool overrides, both keys, zero/null/nonzero values, field-path diagnostics; retain unknown-key rejection. |
| `internal/config/store_test.go` | Extend `TestStoreOwnsInitial`; add `TestRemovedKeysKeepLastGoodConfig`. |
| `internal/config/round2_test.go` | Rewrite fixture/assertion in `TestF10InvalidPoolDoesNotRejectPauseUpdate`; retain `TestF7TimingRelationships` unchanged. |
| `internal/config/sample_manifest_test.go` | Rewrite `TestShippedSampleLoads` override assertion. |
| `internal/controller/reconciler_test.go` | Rewrite `TestReconcileDecisions` removal row and `TestBlockedPools` invalid-config fixture. Add `TestReconcileUsesHighPctTarget`: demand20/assigned18/claimable3 → order5, target26, need5. Add `TestAssignedExceedsDemandCanStillNeedBuffer`: demand20/assigned21 → order5. |
| `internal/controller/round2_test.go` | Rewrite F1 first count to3 and F10 invalid key fixture; preserve all safety assertions. |
| `internal/controller/envtest_reconciler_test.go` | Extend `"grows, respects pause, watches replicas, and reloads every pool"`: after current supply10/demand8, demand10 should order3 and reach target13. |
| `internal/metrics/metrics_test.go` | Rewrite `TestCatalogueAndForget`, `TestProbeAndLatestFailure`, `TestBoundedReasons` for signature/reason changes. Add `TestRemovedMetricsAreNotRegistered`; retain blocked/probe/class/cardinality behavior. |
| `internal/metrics/round2_test.go` | Update signature in `TestF5UnknownFailureMetric`. |
| `cmd/main_test.go` | Extend `TestStartupBoundChanged`, `TestReloadAcceptedOnlyAndRestartDedup`; add `TestRemovedKeyReloadDiagnosticIsSafe` if the typed migration message is implemented. |
| New `config/prometheus/herzi-alerts.test.yaml` | promtool cases for `HerziStuckOpenRuns` using multiple pool series and one unlabeled threshold, and `HerziAssignedExceedsDemand`: free excess alone does not fire; sustained assigned excess does. |

The design/README table should mirror the rewritten single-buffer test table. Custom buffer-two scenarios can retain their existing numerical needs; the **default** demand-50 row must become 65. This avoids disguising custom-buffer rows as default behavior.

**7. Open question**

One deployment fact needs confirmation: **could any previous-ID pool runs still be open when this version starts ordering?**

Verified: the Python docstrings say existing runs keep their original IDs; herzi currently sweeps only one exact ID family. Unknown: whether any such runs exist in the deployed Temporal namespace.

Recommended rollout is to pause growth and drain/verify previous-ID pool runs before enabling the new controller. If uninterrupted coexistence is required, plan temporary multi-family accounting separately: it must preserve each run’s actual workflow ID for history reads, count all open executions, and merge failure evidence. Simply switching builders cannot account for those runs.

No questions remain about the requested formula, defaults or removal scope.

No files were modified and no tests were executed. The session identifies me as Codex based on GPT-6; the exact model variant and effort setting are not exposed.

