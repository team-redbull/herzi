# herzi — build plan

herzi is the **inventory controller**: it keeps each InfraEnv's pool of spare machines matched to
HyperShift NodePool demand by calling the workflows `install-server` API. The full specification is
[`design/herzi-design.md`](design/herzi-design.md) (Part 1 logic with examples, Part 2 pseudocode, Part 3
flaws and edge cases) and [`design/inventory-controller-original.md`](design/inventory-controller-original.md).
**The design document is the source of truth; if the code and the design disagree, fix one of them on purpose.**

## Scope of v1
Grow is real. Shrink is **log-only** ("WOULD REMOVE"). The cooldown, real shrink, VM creation and an
idempotency key on the route are in the design's backlog and are NOT built.

## Decisions already made (do not reopen)
- Go, Kubebuilder scaffold, controller-runtime, **no CRD of our own**.
- NodePool / Agent / InfraEnv are read as **unstructured** and parsed into the views in `internal/kube`
  (importing the typed API modules costs >100 modules and an ORM).
- A NodePool selects ONLY by the InfraEnv label; anything else blocks that pool.
- All Agents on the hub are auto-approved. Disk wipe on release happens before an Agent registers.
- Leader election ON. One reconcile per key. Pools reconcile in parallel (`MaxConcurrentReconciles` 4).
- Workflow ids: `add-server-to-mce_<mce>_<infraenv>_<slot>` (see `internal/ids`).
- Probe mode: one machine every 20 min while hardware looks missing.

## Architecture
```
cmd/main.go                    manager, flags, config store, wiring                       (controller WP)
internal/kube/                 views over unstructured InfraEnv/NodePool/Agent            (WP3)
internal/config/               Config, merge, validation, strict load, Store              (WP2)
internal/ids/                  workflow-id builders (DONE, pinned to Python)
internal/pipeline/             PURE stages Attribute/Validate/Decide/Plan + ports          (WP1)
internal/installer/            HTTP client of the install-server route                    (WP3)
internal/temporalx/            slot sweep (Describe), failure types, queue pollers         (WP4)
internal/metrics/              Prometheus metrics (names below)                           (controller WP)
internal/controller/           reconciler, watches/mapping, annotations, recheck          (controller WP)
config/, Dockerfile, Makefile, .github/, docs/ops, test/crds                              (WP5)
```

## Work packages and FILE OWNERSHIP
Each agent edits ONLY the paths it owns. Shared contracts already exist and are **read-only** for the
packages: `internal/pipeline/{types,ports,errors,stages}.go`, `internal/kube/types.go`,
`internal/config/types.go`, `internal/ids/*`. If a contract needs to change, STOP and say so in the report.

| WP | Owns | Delivers |
|---|---|---|
| WP1 pipeline | `internal/pipeline/{attribute,validate,decide,plan}.go` and `*_test.go` | the four pure stages; table tests for EVERY row of the design's test table; property tests (rapid); boundary tests |
| WP2 config | `internal/config/*.go` except `types.go`; `internal/config/testdata/` | `Defaults()`, `Load([]byte)` strict YAML with duration strings and unknown-key rejection, `(Config).ForPool/Manages/Validate`, `Store` (atomic last-good snapshot), tests |
| WP3 kube + installer | `internal/kube/*.go` except `types.go`; `internal/kube/testdata/`; `internal/installer/` | parsers `ParseInfraEnv/ParseNodePool/ParseAgent` from `*unstructured.Unstructured`; the HTTP `Installer` with error classification; tests |
| WP4 temporalx | `internal/temporalx/` | `Sweeper` (RunSweeper) and `Queues` (QueueChecker) over `go.temporal.io/sdk/client`; tests with the SDK mocks |
| WP5 ops | `config/`, `Dockerfile`, `Makefile`, `.github/`, `.golangci.yml`, `docs/ops/`, `test/crds/`, `README.md` | kustomize manifests, hardened Deployment, RBAC, ConfigMap sample, alerts, CI, container build, envtest CRD stubs, runbook |
| WP6 controller (phase 2) | `cmd/`, `internal/controller/`, `internal/metrics/` | wiring, reconciler, mapping funcs, recheck, annotations, metrics, envtest suite |

## Rules for every agent
1. Read `docs/design/herzi-design.md` Parts 1-3 first. Quote the design section you implement in the doc comment.
2. Build and test **only your own packages**: `go build ./internal/<pkg>/... && go test ./internal/<pkg>/...`.
   Never run `./...` (other packages may be mid-edit), never run `go mod tidy`, `go get` or `go mod edit`.
   If you need a module that is not in `go.mod`, do not add it: say so in your report.
3. Standard library `testing` + table tests; `github.com/stretchr/testify` is available; `pgregory.net/rapid` for properties.
   No Ginkgo outside envtest suites.
4. `gofmt`, `go vet` clean. Errors are wrapped with `%w`. No `panic` outside tests. No global mutable state.
   Every external call takes a `context.Context` and honours its deadline. Log with the logr logger passed in.
5. No TODO placeholders in delivered code. If something is unknowable from the repo, write it as an explicit
   `// ASSUMPTION:` comment and list it in your report.
6. Do NOT commit, do NOT push, do NOT touch git. Do NOT edit files you do not own.
7. Final report: files written, what is tested, what is NOT tested, assumptions, anything that contradicts the design.

## Metrics catalogue (names are a contract; WP5 writes alerts against them, WP6 implements them)
Prefix `herzi_`. Labels: `pool` = InfraEnv name.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `herzi_pool_demand` / `_assigned` / `_claimable` / `_settling` / `_stuck_agents` / `_open_runs` | gauge | pool | the counted terms |
| `herzi_pool_target` / `_ceiling` / `_need` | gauge | pool | the formula outputs |
| `herzi_pool_blocked` | gauge 0/1 | pool, reason | pool blocked this pass |
| `herzi_pool_probe_mode` | gauge 0/1 | pool | hardware looks missing |
| `herzi_pool_failure_class` | gauge 0/1 | pool, class (`capacity`,`other`,`unknown`) | last failure class |
| `herzi_orders_total` | counter | pool, mode (`full`,`probe`), result (`started`,`short`,`ambiguous`,`rejected`,`dry_run`) | install requests |
| `herzi_machines_ordered_total` | counter | pool | machines requested |
| `herzi_would_remove_total` | counter | pool | shrink log lines |
| `herzi_reconcile_duration_seconds` | histogram | — | per reconcile |
| `herzi_sweep_duration_seconds` | histogram | — | per pool sweep |
| `herzi_stuck_after_seconds` | gauge | none | startup-configured open-run alert threshold in seconds (process constant) |
| `herzi_oldest_open_run_seconds` | gauge | pool | for the stuck-run alert |
| `herzi_pollers` | gauge | queue (`activity`,`workflow`,`segment`) | poller counts |
| `herzi_warnings_total` | counter | kind | hub-scoped findings (stray Agent, unattributable NodePool, ...) |
| `herzi_config_valid` | gauge 0/1 | — | last config update accepted |
| `herzi_last_successful_reconcile_timestamp_seconds` | gauge | pool | liveness of a pool |

## Phases
0. **Bootstrap (done by the lead):** scaffold, contracts, design copy, this plan.
1. **Parallel build:** WP1-WP5 at the same time, disjoint files.
2. **Integration:** WP6 controller wiring against the finished packages, envtest suite.
3. **Review:** Codex + a second reviewer read the whole tree against the design; fixes; `make lint test`.

## Definition of done (production ready)
- `go vet`, `golangci-lint`, `govulncheck` clean; unit coverage of `internal/pipeline` and `internal/config` >= 90%.
- Every invariant and every row of the design's test table is a test.
- envtest suite covers: pool grows, probe mode, blocked pool, paused pool, unmanaged pool, lease/restart behaviour of the block annotation.
- Container is distroless, non-root, read-only root filesystem, seccomp RuntimeDefault, all capabilities dropped.
- Metrics, probes, RBAC minimal (get/list/watch on the three kinds, get/update/patch on InfraEnv metadata for the block annotation, Lease), leader election with `LeaderElectionReleaseOnCancel`.
- README, runbook and alert rules exist.

## Round 2 safety contracts

The InfraEnv stores `herzi.redbull.io/state` (bounded durable failure/probe evidence) and
`herzi.redbull.io/blocked-until` (pre-POST pending marker). Every attempted POST invalidates its
sweep; the next order requires an observation newer than the previous attempt, including the recheck.
A malformed object blocks only its identifiable pool; unattributed malformed objects warn.
Semantic pool configuration errors publish with other changes and use
`herzi_pool_blocked{reason="config_invalid"}`. Document/hub/default errors retain the last good document.

`maxSlots` must equal 100. Pool limits remain <= 100. Timing relations are
`failureMemory >= 2*probeInterval`, `drawGrace < probeInterval`,
`settlingMax >= probeInterval`, and `interval <= probeInterval` for defaults and every pool.
Acquisition proof uses a completed `acquire_servers` activity, read lazily from run-pinned history.
The alert compares `herzi_oldest_open_run_seconds > herzi_stuck_after_seconds`.
