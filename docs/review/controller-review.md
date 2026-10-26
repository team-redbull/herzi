# WP6 adversarial review

Scope: `internal/controller/`, `internal/metrics/`, `cmd/main.go`, and
`cmd/main_test.go`. Reviewed against design Part 1 cases 1–15, Part 2 Reconcile,
Growth protocol and invariants, and Part 3 A–G. No dependency, generated manifest,
Makefile, test CRD, or git changes were made.

## Findings and evidence

1. **FIXED — per-key serialization and startup namespace.** Previously an accepted
   namespace change moved map-function keys while the manager's `For` watch and
   cache stayed in the original namespace. The reconciler now freezes startup-bound
   hub fields at construction and uses that namespace for both secondary mappings,
   reload listing, `For`, and gathers. Live timing and pool overrides still apply.
   `TestStartupNamespaceFrozen`, `TestMappings`, `TestUnlabeledMalformedMap`, and
   `TestCacheOptions` prove the key, cross-namespace NodePool mapping, unlabeled
   behavior, reload mapping, and cache scope.

2. **FIXED — repeated spending against a cached sweep.** Plan already returns at
   most one request; uncached recheck revalidates, caps by the first Need, and
   preserves Probe. However, a watch burst could spend again against Temporal's
   pre-order cached sweep. A per-pool spending throttle now spans the production
   sweeper's explicitly configured 10s cache window. Its timestamp includes elapsed
   gather time so a slow first gather cannot shorten protection. Gauges still update
   on throttled passes; dry run sends nothing and counts its planned request.
   Evidence: `TestWatchBurstCannotSpendAgainstCachedSweep`, `TestReconcileDecisions`,
   `TestRecheck`, `TestProbeClock`, `TestNewAndNamespace`.

3. **FIXED — timestamp precision; remaining patch behavior VERIFIED-OK.** RFC3339
   truncation could erase a tiny valid ambiguous wait. Round up to the next second
   when needed, never shortening the wait. Merge patch type and exact JSON contain
   only `metadata.annotations["herzi.redbull.io/blocked-until"]`; no whole-object
   Update occurs. Errors from patching propagate through a safe-text error wrapper
   that preserves errors.Is/As. Evidence: `TestOutcomes` (ambiguous, rejected, patch
   failure), `TestBlockSurvivesRestartAndExpires`, `TestBlockedPools` (future/expired),
   `TestNewBlockRequeueAndRounding`.

4. **VERIFIED-OK — partial outcomes.** Failed items do not trigger an immediate
   replacement or mean that nothing started. Only Started contributes to the machine
   counter, once per POST; a successful short result increments result=short.
   `TestPartialOutcomeCountsExactlyOnce`, `TestFailedSlotsResolvedBySweep`, and
   `TestOutcomes` prove mixed Started/Busy/Failed handling and accounting.

5. **FIXED — metric cardinality and cleanup.** Free-text reasons previously exposed
   object names, timestamps and external errors as labels. `metrics.BlockReasons`
   maps them into 15 fixed categories (including `other`), deduplicated and sorted.
   Unmanaging a pool now removes its series even if the following API call fails.
   No series is recreated after a recheck NotFound. Existing parse-error identity
   fallback ensures malformed InfraEnvs retain their actual pool name in gauges.
   Evidence: `TestBoundedReasons`, `TestReasonVocabulary`, `TestCatalogueAndForget`,
   `TestBlockedMetricsAndSingleObservations`, `TestUnmanagedMetricsDisappear`,
   `TestUnmanageForgetsEvenWhenAPIUnavailable`, `TestNotFoundFreshLeavesNoSeries`,
   `TestPoolIsolationAndForget`. Successful gathers publish gauges even when blocked;
   API read failures return errors and retain prior managed-pool observations rather
   than publishing fabricated zero inventory.

6. **VERIFIED-OK — shared poller checks.** One in-flight check is shared across pools;
   successful results live 30s and errors 5s. A successful check reporting zero
   pollers still has the normal TTL. Poller errors block all pools, while sweep
   errors block their own pool. Evidence: `TestConcurrentPollers`, `TestPollersTTL`,
   `TestPollerErrorBlocksAllPools`, `TestBlockedPools`, `TestPoolIsolationAndForget`.
   The concurrency test is also run 20 times with the race detector.

7. **FIXED — block-expiry scheduling.** Existing and newly written blocks now wake
   at the earlier of the frozen interval or block expiry. Elapsed API latency is
   subtracted from the expiry delay; overdue wakeups use a positive nanosecond delay.
   The injected decision clock is read exactly once. RawSnapshot.At, parsing,
   pipeline decisions, annotations and liveness use that value. Real elapsed-time
   measurements serve instrumentation and scheduling only; no pure decision stage
   reads wall time. Evidence: `TestOneClockAndFrozenConfig`,
   `TestBlockedExpiryRequeue`, `TestNewBlockRequeueAndRounding`, `TestProbeClock`.

8. **FIXED — frozen effective config, restart diagnostics, cancellation.** One Store
   snapshot supplies each pass, overlaid with the startup-bound identity/cache/port
   fields. Restart-required field names are logged once per changed field set;
   reverting that set permits a later warning again. All external calls receive
   the reconcile deadline, including recheck and patch; the reload list also has a
   deadline. Explicit cancellation checks prevent subsequent poller checks or
   spending after a canceled sweep/poller call. Evidence:
   `TestOneClockAndFrozenConfig`, `TestStartupNamespaceFrozen`,
   `TestReloadAcceptedOnlyAndRestartDedup`, `TestStartupBoundChanged`,
   `TestExternalDeadlinesAndCancellation`.

9. **FIXED — safe, change-only logging.** Block Info logs use bounded categories;
   arbitrary error bodies cannot leak credentials. Rejected installs log the
   classification sentinel rather than the HTTP response body. Returned reconcile
   errors expose safe text while preserving the original wrapped cause. Repeated
   hub warnings are debug logs; their counters still increment once per distinct
   warning per pass. Startup/config failures omit arbitrary scalar/error text.
   Evidence: `TestSafeChangeOnlyLogging`, `TestMalformedFreshReadAndDedup`,
   `TestReloadAcceptedOnlyAndRestartDedup`. Captured controller messages have a
   capital first character, no trailing period, and balanced key/value pairs.

10. **FIXED — main wiring and lifecycle.** Leader election now defaults on, with
    stable ID `herzi.redbull.io`, ReleaseOnCancel=true, and zero graceful shutdown
    timeout. Only main invokes os.Exit; run returns through Temporal cleanup on
    setup errors as well as manager shutdown. Reading the final control flow:
    after mgr.Start returns, only error logging/return and the deferred tc.Close
    remain, then main exits; no new reconciliation or other operational work is
    scheduled. Initial config failure returns exit code 1. FileWatcher remains a
    non-leader runnable; rejected reloads emit no signal. Installer timeout is
    max(1ns, ReconcileTimeout/2), still bounded by the parent context. Removed the
    duplicate sweeper observer in main: the reconciler owns sweep observations.
    Flag parsing retains --kubeconfig; every explicit flag has help, zap flags are
    bound, and all five credential/TLS environment variables are described beside
    their constants. HTTP/2 defaults disabled; it remains explicitly opt-in.
    Evidence: `TestManagerSafetyOptions`, `TestFlagDefaultsAndTLS`,
    `TestInitialConfigFatal`, `TestInstallTimeout`, `TestWatcherEveryReplica`,
    `TestReloadAcceptedOnlyAndRestartDedup`, `TestBlockedMetricsAndSingleObservations`.
    Registration of healthz/readyz with healthz.Ping and shutdown ordering were
    checked by reading run; actual HTTP probes and lease handoff were not exercised
    in this socket-restricted environment.

11. **VERIFIED-OK, with cancellation hardening in item 8 — failure modes.**
    `TestBlockedPools` and `TestMalformedFreshReadAndDedup` cover malformed objects;
    `TestNotFoundFreshLeavesNoSeries` covers disappearance during recheck;
    `TestAPIFailures` covers cached/uncached Agent-list API failures;
    `TestNewAndNamespace` and `TestBadMetricsRegistration` cover nil/invalid metrics;
    `TestClosedReloadChannel` exercises the actual controller-runtime channel
    source without sockets and verifies no duplicate zero-value reloads;
    `TestExternalDeadlinesAndCancellation` covers deadline expiry at each external
    boundary and cancellation during a sweep. The main-owned Reload channel is
    never closed by its producer; closing a source channel is supported.

12. **FIXED — design mismatches and redundant work.** The restart-only namespace
    mismatch, missing spending throttle, default-disabled leadership, default manager
    drain, and duplicate sweep histogram observation are addressed above. Reload
    uses the authorized file-watcher channel rather than a ConfigMap API watch;
    no ConfigMap RBAC is introduced. Shrink remains log-only. NeedLeaderElection
    documents the reconciler's intended mode; controller-runtime's built controller
    supplies the actual default leader-only behavior. Pure pipeline and port APIs
    remain unchanged. Evidence: items 1–11 and their named tests.

## Verification and limits

The regression check temporarily restored the old behavior for startup snapshot
handling, block wakeup/precision, raw reason labels, spending throttling, zero
installer timeout, and leadership default. The new regression tests failed for
those behaviors. Hardened sources were restored before final verification.

The required final command builds and vets the whole module and race-tests only
controller, metrics, and cmd. GOCACHE is redirected into /tmp because the default
cache is read-only; GOMAXPROCS=2 keeps compilation within sandbox resources.

Envtest remains build-tag-free and skips without KUBEBUILDER_ASSETS. Its Eventually
budget now allows the spending throttle. It was compiled, but not executed here;
the lead runs envtest and RBAC generation outside this sandbox. No claim is made
that a live Temporal service, real install route, lease loss, HTTP probes, or an
uncooperative port was exercised. Context deadlines require cooperative clients;
the production clients accept and honor those contexts.

Accepted design risks remain: no lease fencing or route idempotency key; a request
already accepted remotely may start after a later sweep. A failed block patch is
logged and returned for backoff, but persistence cannot be guaranteed during a
Kubernetes outage. The spending throttle protects its normal 10s sweep window;
custom ports using a longer sweep TTL would require matching throttle settings.
Poller liveness is delayed by Temporal's poller retention. Unattributable warnings
never block; malformed object ReadErrors conservatively block the hub, as allowed
by the WP6 mechanics. Startup-bound identity changes require restart and remain
visible in the loaded Store even though effective reconciler settings stay pinned.
Rounding a block annotation can extend a wait by less than one second. Disabling
leader election or enabling HTTP/2 remains possible through explicit flags.

Final coverage from `go build ./... && go vet ./... && go test -race -cover
./internal/controller/... ./internal/metrics/... ./cmd/...`:

| Package | Statement coverage |
| --- | ---: |
| internal/controller | 96.9% |
| internal/metrics | 100.0% |
| cmd | 64.2% |

The lower cmd coverage reflects the socket-dependent manager/Temporal/bootstrap
path, which was reviewed but not run. Pure flag, TLS, timeout, manager-option,
reload-handler and fatal-initial-config paths run without sockets. Removal of
main's duplicate sweep callback was source-checked; the unit test separately
asserts the reconciler observes each sweep exactly once. Unmanaged pools are still
gathered and validated, but their pool metric series are deleted, following the
explicit WP6 cardinality requirement rather than retaining unmanaged-pool gauges.

Repeated concurrency verification passed:
`go test -race -count=20 -run TestConcurrentPollers ./internal/controller/...`.
