# Completion of five fixes for f237669

The initial working-tree diff was inspected before any edits. Correct partial work was
retained. No Git state was changed, no dependencies were changed, and no files outside
internal/, cmd/, docs/, README.md, and config/prometheus/ were edited.

| Finding | Already applied when this run started | Completed in this run | Regression evidence |
|---|---|---|---|
| F-A | Config caps, checked Target with ok flag, checked supply additions, implausible demand guard, controller Target call update, arithmetic tests | Default/merged-pool cap boundary and last-good/store tests; documented caps and failure behavior; corrected lint in partial demand regression | TestTargetOverflowFailsClosed, TestTargetLiteralArithmetic, TestAttributeImplausibleDemand, TestTargetConfigCaps |
| F-B | Generic effective-map migration inspection, deterministic pool ordering, yaml.v3 cycle rejection, effective-shadowing comment, Load/Store merge tests, reload fixture correction | Startup and sanitized reload tests for block/flow maps, anchors/aliases, document merges, pool-container merges, merge sequences and null/zero values; retained legal removed-key pool-name tests; removed an irrelevant assertion | TestRemovedKeyEffectiveDocument, TestRemovedKeyReloadDiagnosticIsSafe, TestMergedRemovedKeysStartupAndReload |
| F-C | Correct ignoring(pool) expression and documentation expression updates | Two controller targets with different thresholds; clarified matching on scrape target labels | promtool check rules: 10 rules; promtool test rules: all cases pass, including new two-target case |
| F-D | Negative replica parser rejection and parser regression cases | Two NodePools with replicas 20 and -10: demand remains 20, affected pool has a read error and never orders, healthy second pool orders eight | TestNegativeReplicasBlockOnlyTheirPool, TestMalformedFields |
| F-E | Sweeper assigns unknown to TimedOut, pipeline timeout/state regression, dry-run wording corrections and slot-model comment | Asserted unknown timeout classification in sweeper status test; documented timeout behavior explicitly in design and operating guide | TestStatusMappingAndIDs, TestTimedOutUnknownNeverAllowsFull; no WOULD INSTALL matches remain in docs/ or README.md |

## Exact contracts for sample configuration and CI

- Defaults remain highPct=30 and minFree=2.
- Defaults and merged pools require 0 <= highPct <= 1000 and 0 <= minFree <= 100000.
  Validation messages are `highPct: must be between 0 and 1000` and
  `minFree: must be between 0 and 100000`, with default/pool context added by the caller.
  Invalid defaults reject the document. Invalid merged pools remain pool-scoped config errors.
- Demand above 10,000,000 creates a pool Problem ending in `: implausible demand`.
  Exactly 10,000,000 is accepted. Checked accumulation never wraps.
- `pipeline.Target(demand, config) (buffer, target int, ok bool)` returns zero values
  and false for negative inputs or unrepresentable arithmetic. Decide also checks supply
  additions, returning `pipeline.HoldTargetOutOfRange = "target out of range"` on failure.
  Regression inputs include demand 1 with highPct=MaxInt64, demand 1 with
  minFree=MaxInt64, and demand 307445734561825861 with highPct=30.
- `config.RemovedKeyError` remains the typed migration diagnostic. Exact messages:
  `defaults.lowPct was removed: use highPct as the single target buffer percentage; delete lowPct`
  and `pools.<name>.minBand was removed: the controller no longer has a ceiling; delete minBand`.
  Both keys are rejected at either schema path, including null/zero values. Effective
  explicit-key shadowing follows yaml.v3. A pool named lowPct or minBand is legal.
  Unsafe pool names are rendered as `<invalid>`. Reload logs only the typed message,
  excluding wrapped error text; startup fails with the actionable migration message.
- Negative replicas return a parse error containing `spec.replicas: must be >= 0`.
  Identity-first gathering attaches that read error only to its identifiable pool.
- TimedOut slots get `FailType = pipeline.FailUnknown` (`"unknown"`) without a failure
  history read. Existing MergeState/Decide handling retains unknown evidence and guards
  Full spending. Demand 6, supply 0 and one recent timeout produce Probe 1, not Full 8.
- The literal dry-run log message is `Would install`.
- Alert expression:
  `herzi_oldest_open_run_seconds > ignoring(pool) group_left herzi_stuck_after_seconds`.
  The new test has two job/instance targets, thresholds 1800 and 7200, both ages 2000;
  only the first target's pool fires after five minutes.

## Verification

The requested build, vet and race command passed using PATH with Linuxbrew and
GOCACHE=/tmp/herzi-go-build, GOTMPDIR=/tmp, GOFLAGS=-buildvcs=false, GOMAXPROCS=2:

```sh
go build ./... && go vet ./... && go test -race -count=1 -cover ./cmd/... ./internal/...
```

`./bin/golangci-lint run ./internal/... ./cmd/...` passed with zero issues using a
writable /tmp lint cache. The shipped PrometheusRule spec was extracted in a temporary
directory; both promtool check rules and promtool test rules passed. All existing alert
cases were retained. An initial new-test failure was a missing poller fixture; it was
corrected before the final successful complete run.

| Package | Race-test statement coverage |
|---|---:|
| cmd | 65.4% |
| internal/config | 96.2% |
| internal/controller | 97.5% |
| internal/ids | 100.0% |
| internal/installer | 95.7% |
| internal/kube | 100.0% |
| internal/metrics | 100.0% |
| internal/pipeline | 100.0% |
| internal/temporalx | 96.0% |

All five behavioral fixes and their requested regressions are complete. All internal
coverage targets are met. The cmd package does not meet a blanket 95% threshold: external
manager startup/wiring remains untested, while migration startup and reload paths are
covered. Envtest suites skip without configured assets and were not run against a real
API server in this socket-restricted sandbox. The lead still owns envtest execution,
sample configuration comments and CI changes; their restricted paths were left alone.
