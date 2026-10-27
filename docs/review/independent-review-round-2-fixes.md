> **Historical verification record; add-only addendum.** The safety contracts below remain applicable. Fixture arithmetic has since changed: demand 10 targets 13 (three additions at supply 10), demand 50 targets 65. Obsolete minBand fixtures must now use a supported invalid setting such as highPct: -1; removed keys reject the entire document. The former ceiling/removal behavior is retired. The current alert requires explicit vector matching: `herzi_oldest_open_run_seconds > ignoring(pool) group_left herzi_stuck_after_seconds`. See the [current design](../design/herzi-design.md). Recorded tests/results below refer to the original round-2 revision.

# Independent review round 2: implementation and verification

Scope: internal packages, design, plan, and this report. No git commands, dependency changes,
flags, generated manifests, deployment changes, or writes to the lead-owned paths.

## Findings and regression evidence

| Finding | Implementation | Regression tests |
|---|---|---|
| F1 | `SweepResult.ObservedAt` survives cache hits. `SweepInvalidator` drops the pool cache after every attempted POST. Spending after the uncached recheck requires observation strictly after the preceding attempt; elapsed time cannot authorize it. | `TestF1SlowRecheckCannotSpendStaleSweep`: demand 10, supply 10, target 12; a t=0 observation cannot authorize another order at t=11 after the two-second recheck. `TestF1InvalidationAndObservedAt`, `TestF1ConcurrentInvalidation` |
| F2 | Pending marker is patched before POST. All annotation operations get independent cancellation-detached, ten-second contexts. Prewrite failure prevents POST. Parsed definite results clear the marker using null; failed clear logs and expires. Ambiguous/rejected outcomes renew from outcome time. | `TestF2PendingMarkerPrecedesPOSTAndSurvivesCancellation` (15-machine deficit), `TestF2ExactClearAndPrewriteFailure` (exact patch bodies; success/short/ambiguous/rejected, prewrite and clear failures), `TestF2OutcomeTimeExtendsMarkerAndPatchError` (five-minute POST followed by a full five-minute wait), `TestF2AnnotationMarshalError` |
| F3 | `maxSlots` must equal the route's fixed 100 slots, including after restart. `maxOpenRunsPerPool` remains the load control and must not exceed maxSlots. | `TestF3AllHundredSlotsMustRemainVisible` rejects 15 and other reduced ranges; accepts all 100 visible slots. Existing validation/sample tests remain green. |
| F4 | `State` is compact bounded JSON in one InfraEnv annotation. Pure `MergeState` retains capacity/unknown failure time, at most three recent distinct other failed execution IDs, qualifying completion/proof, and probe schedule. Evidence expires after FailureMemory. Changed state is persisted with its own bounded context; uncached state is merged before writes to avoid informer rollback. Probe schedule and pending marker share one prewrite. | `TestF4SlotReuseRetainsCapacityEvidence` (7 Agents, demand rises to 10, slot 1 overwritten by ten-second probe), `TestF4HundredDistinctFailuresReusingTwoSlots` (100 BmcCredentialsMissingError failures; probe mode from the third distinct run), `TestF4StatePersistsAcrossRestartAndProbePrewrite`, `TestF4StateDegradationAndMerge`, `TestF4CachedStateCannotEraseDurableFailures`. The old test blessing Full after overwritten evidence now expects probe-running Hold with durable state. |
| F5 | Failed history reads produce `FailUnknown`, without failing the whole sweep. Unknown evidence uses the capacity spending guard, and metrics expose `class="unknown"`. | `TestF5FailedHistoryClassifiedUnknown` and `TestF5UnknownHistoryNeverAllowsFull`: two unreadable failed slots yield Probe rather than Full. `TestF5UnknownFailureMetric` checks the label. |
| F6 | `SlotRun.Acquired` requires a completed `acquire_servers` activity linked to its scheduled event. Run-pinned all-event history is bounded by deadline and 100,000 events; only old-enough Running work in capacity/unknown pools incurs the read. Positive results share the bounded run-ID cache. Missing/error history gives no proof. | `TestF6ProbeMustAcquireMachine`: demand 6/target 8; an unacquired three-minute probe cannot release seven Full orders. `TestF6LazyAcquisitionProofAndCache`, `TestF6AcquisitionReadFailuresAndYoungRuns`, `TestF6ReconcilerUsesStatefulSweeper` |
| F7 | Validate enforces all four timing relationships on defaults and merged pool settings. | `TestF7TimingRelationships` tests failing/passing boundaries for each relation and rejects failureMemory 1m with probeInterval 20m; existing sample manifest test remains green. |
| F8 | Identity is extracted before optional parsing. Determinable parse errors become pool-scoped ReadErrors; unattributable malformed objects become deduplicated warnings. Actual list/API errors still fail the gather. | `TestF8MalformedAgentIsolatedToItsPool`: a malformed unmanaged Agent leaves all 20 managed pools ordering; an Agent of pool 0 blocks only that pool (19 still order); unlabeled malformed Agent does not block. Existing API-failure tests remain green. |
| F9 | Only open work started after the failure that establishes probe mode suppresses a scheduled probe. All open runs still count against need and headroom. | `TestF9OldHungWorkDoesNotStarveProbe`: run starts 10:00, failure closes 10:00:20, recovery at 10:30 permits one probe with 14 available positions; a full open-run cap still prevents it. |
| F10 | Load rejects document/hub/default errors, while retaining merged invalid pools in Config and exposing PoolErrors. Reconciliation maps each pool error to a pipeline Problem. | `TestF10InvalidPoolDoesNotRejectPauseUpdate`: pause A and invalidate B in one accepted update/startup document. `TestF10ConfigErrorBlocksOnlyInvalidPool`: paused pool does not order, invalid pool gets config_invalid, healthy pool orders. |
| F11 | Process-wide threshold gauge initializes before reconciliation; dead import-only stages.go removed and its documentation retained in the package comment. Design wave arithmetic now says 10 Agents + 5 open. | `TestF11StuckAfterGauge` (30 minutes = 1800 seconds), `TestF11ConfiguredThresholdAvailableBeforeReconcile` (constant for the process), catalogue tests |

For all eleven findings, a targeted check temporarily restored the defective behavior in the relevant
production function, ran its regression, and observed an assertion failure (not a compilation failure).
Every source was restored in a finally block. This is defect-restoration testing, not a checkout of the
complete historical tree. Full fixed-suite validation follows below.

## Contracts for the lead

Annotations (merge patches contain only metadata.annotations):

- Existing `herzi.redbull.io/blocked-until`: RFC 3339 UTC deadline, now prewritten before POST;
  definite-result clear is JSON null. Subsecond deadlines round upward, never shorten the wait.
- New `herzi.redbull.io/state`: compact JSON <= 1 KiB, fields `capFailAt`, `otherFails`
  (`runId`, `closedAt`, at most three distinct executions), `lastOKAt`, `probeNotBefore`.
  Unknown JSON keys are ignored. Missing, corrupt, or oversized annotations degrade to sweep-only
  evidence. Capacity recovery requires a run that started after the failure.

Metrics:

- New `herzi_stuck_after_seconds`, no labels; initialized from startup stuckAfter and constant for
  the process. Changing the effective threshold requires a restart. The lead should include this
  in cmd's restart-required diagnostics. Alert expression:
  `herzi_oldest_open_run_seconds > herzi_stuck_after_seconds`.
- Existing `herzi_pool_failure_class{pool,class}` adds `class="unknown"` alongside capacity/other.
- Reused `herzi_pool_blocked{pool,reason="config_invalid"}` for invalid merged pool configuration;
  this replaces the old bounded reason label `config`. No separate configuration-error gauge.

Configuration rules:

- `maxSlots == 100` for this route; retain the field.
- `1 <= maxOpenRunsPerPool <= maxSlots` (the load control).
- `failureMemory >= 2*probeInterval`.
- `drawGrace < probeInterval`.
- `settlingMax >= probeInterval`.
- `interval <= probeInterval` for defaults and every merged pool.
- Bad YAML, unknown keys, bad duration syntax, invalid hub settings, or invalid defaults reject a
  document. Invalid merged pools remain published with PoolErrors; healthy changes still apply.

No new flag or dependency is required. The real activity name is **acquire_servers**, verified in
`/home/toto/code/workflows/shared/interfaces/server_lifecycle.py:70` (plain activity.defn, no rename)
and its invocation in `workflow_domains/server_lifecycle/install_server.py:618`.

## Verification and limits

Production internal packages are lint-clean (including goimports and gocyclo); long touched production
lines were wrapped to <= 120 characters. Build/vet/race/coverage and repeated concurrency results are
recorded below after final verification.

The socket-restricted environment does not run envtest, a live Temporal service, or the install API.
The existing envtest suites skip without KUBEBUILDER_ASSETS; the lead runs them separately. No claim
is made about lease fencing or route idempotency: those remain accepted design limitations. A remotely
accepted request can still start after a later sweep. An unavailable Kubernetes API prevents the
prewrite and therefore prevents sending an additive POST. Failed outcome renewal still retains the
previously persisted pending marker; a failed clear leaves it to expire.

Final commands passed (PATH includes /home/linuxbrew/.linuxbrew/bin; GOCACHE and lint cache are in
/tmp; GOMAXPROCS=2):

```sh
go build ./... && go vet ./... && go test -race -count=1 -cover ./internal/...
go test -race -count=20 -run 'TestF1Concurrent|TestConcurrentPollers' \
  ./internal/controller ./internal/temporalx
./bin/golangci-lint run ./internal/...
```

The repeated run includes `TestF1ConcurrentInvalidationDuringCanceledSweep`: canceling a superseded
in-flight sweep cannot evict the newer cached observation. Lint reports **0 issues**.

| Package | Final race-test statement coverage |
|---|---:|
| internal/config | 95.9% |
| internal/controller | 97.3% |
| internal/ids | 100.0% |
| internal/installer | 95.7% |
| internal/kube | 100.0% |
| internal/metrics | 100.0% |
| internal/pipeline | 100.0% |
| internal/temporalx | 96.0% |

All eleven requested findings are implemented and have regressions. Live/envtest execution and the
lead-owned alert/operational-document updates remain with the lead as requested; no implementation
finding is left open in the authorized scope.
