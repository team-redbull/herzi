# herzi

Deployment source of truth during the documentation migration: [Helm chart guide](charts/herzi/README.md).

herzi is the inventory controller for an MCE hub. It keeps each opted-in
InfraEnv's spare machines aligned with HyperShift NodePool demand. NodePools
express worker demand, Agents represent machines, and Temporal install runs
account for machines still on their way.

For each pool, herzi counts assigned, claimable and settling Agents plus open
install runs. It targets demand plus a configurable spare buffer, rechecks live
Kubernetes data before ordering, and calls the workflows install-server API in
bounded batches. Missing hardware triggers a one-machine probe every 20 minutes;
uncertain reads and missing workers block growth rather than guessing.

herzi owns no CRD and watches the operators' InfraEnv, Agent and NodePool resources
as unstructured objects. One elected controller works across pools with bounded
parallelism. Configuration opts pools in, supports per-pool overrides and dry run,
and preserves the last good snapshot when an update cannot be parsed.

## Status

**Permanently add-only:** herzi only adds servers. Capacity is never reclaimed;
lowering demand or the buffer only stops additions. VM creation, orphan-binding
detection and an idempotency key on the install route remain backlog items.

The controller, metrics, manager wiring, RBAC and packaging are implemented and tested:
unit tests, an envtest suite against a real kube-apiserver, `golangci-lint` and
`govulncheck` are clean. What has **not** been exercised: a container build, a real
cluster, a live Temporal service and the install route, and the hosted CI workflow
(none of them are available in the development workspace). Roll out as the
[operating guide](docs/ops/operating.md#integration-status-and-release-gates) describes:
every pool starts with `dryRunGrow: true`.

## Architecture

```text
NodePools (demand) ----+
Agents (supply) -------+--> Kubernetes watches / live recheck
InfraEnvs (pool) ------+                  |
ConfigMap ------------+                  v
                               Attribute -> Validate -> Decide -> Plan
                                         ^                 |
Temporal Describe slots + queue pollers --+                 v
                                                 workflows install-server
                                                          |
                                                          v
                                                   Temporal -> Agents

Lease: one active controller     HTTPS metrics: Prometheus -> alerts -> runbook
```

## Quickstart

Use the Go version in go.mod or newer. Build/test need Go and downloadable modules;
integration uses pinned setup-envtest and a local API server/etcd, without Docker.

```bash
export PATH=/home/linuxbrew/.linuxbrew/bin:$PATH
make build
make test                     # unit race tests, cover.out
make test-integration         # envtest: a real kube-apiserver and etcd, CRD stubs from test/crds
make ci                       # lint, vet, unit tests, govulncheck
make lint-manifests
```

Before running locally, complete the release gates, adapt every hub endpoint and
queue in `config/samples/herzi-config.yaml`, and set `dryRunGrow: true` on every
managed pool. Supply Temporal TLS file paths and optional API token as described
in the [operating guide](docs/ops/operating.md#secrets-and-secure-monitoring).
Local execution uses your current kubeconfig identity and can order real machines.

```bash
make run CONFIG=config/samples/herzi-config.yaml
# Equivalent after building:
./bin/manager --leader-elect=false --config=config/samples/herzi-config.yaml \
  --health-probe-bind-address=:8081 --metrics-bind-address=:8443
```

Deployment requires the hub's real external CRDs, reviewed generated RBAC and
provisioned TLS Secrets. Envtest CRDs are permissive test fixtures only. Select
an immutable image and a deliberately chosen kubeconfig context.

```bash
make manifests                # controller markers generate config/rbac/role.yaml
# Review docs/ops/rbac.md and enable the generated role and binding after review.
make docker-build IMG=<registry>/herzi:<version> VERSION=<version> COMMIT=<commit>
make docker-push IMG=<registry>/herzi:<version>
kubectl kustomize config/default
make deploy IMG=<registry>/herzi:<version>
kubectl -n herzi-system rollout status deployment/herzi
# Optional, with Prometheus Operator CRDs and monitoring credentials installed:
kubectl apply -k config/prometheus
```

The sample has three pools, all with dry-run growth enabled or paused. The optional
NetworkPolicy must be edited for actual endpoint IPs before applying. Never apply
`test/crds` to a real hub. There is no CRD install/uninstall target.

## Configuration

Hub settings sit at the YAML top level. Every pool setting lives under `defaults`
and may be overridden under `pools.<InfraEnv name>`; the presence of that name opts
in. Explicit zero/false are overrides; omissions inherit. Durations are strings.
“Required/site-specific” means no portable default is defined by the design or
shared contract. The sample uses illustrative values, not discovered endpoints.

| Setting | Default | Scope | Meaning |
|---|---|---|---|
| interval | 120s | Hub | Normal requeue period |
| reconcileTimeout | 60s | Hub | Overall reconcile deadline |
| hubName | Required/site-specific | Hub | Canonical MCE name, ocp4-... |
| agentNamespace | inventory | Hub | InfraEnv/Agent namespace sent to installer |
| installURL | Required/site-specific | Hub | Full /workflows/server-lifecycle/install-server route |
| maxSlots | 100 | Hub | Must equal the API route slot range |
| ambiguousWait | 5m | Hub | Persisted block after an unclear POST |
| stuckAfter | 2h | Hub | Startup open-run warning age; exported as herzi_stuck_after_seconds; restart to change |
| temporalAddress | Required/site-specific | Hub | Temporal host:port (plain gRPC unless the TLS component is used) |
| temporalNamespace | default | Hub | Temporal namespace |
| activityQueue | server-lifecycle-activity-<hubName> | Hub | Hub activity queue, derived from hubName when not written |
| workflowQueue | install-server-workflow | Hub | Install workflow queue |
| segmentQueue | segment-lifecycle-activity | Hub | Segment activity queue |
| highPct | 10 | Pool | Single target buffer percentage, integer half-up rounding |
| minFree | 2 | Pool | Minimum spare buffer; include Replace upgrade surge |
| maxOpenRunsPerPool | 15 | Pool | Concurrent install load cap, at most maxSlots |
| probeInterval | 20m | Pool | Minimum interval between capacity probes |
| drawGrace | 2m | Pool | Minimum age of a post-failure Running run with completed acquire_servers |
| failureMemory | 6h | Pool | Closed failure evidence window |
| settlingMax | 60m | Pool | Unclaimable Agent settling allowance |
| dryRunGrow | false | Pool | Log Would install without posting |
| paused | false | Pool | Observe but never order |
| defaults | Pool defaults above | Hub structure | Base for every listed pool |
| pools | Empty; no managed pools | Hub structure | InfraEnv name to pool override mapping |

See the complete [sample](config/samples/herzi-config.yaml), including all Temporal
fields in the shared config contract. TLS paths and API bearer token are supplied
via Secret mounts/environment, not YAML. `maxSlots` must equal 100. Validate
`0 <= highPct <= 1000`, `0 <= minFree <= 100000`, `1 <= maxOpenRunsPerPool <= 100`, positive durations,
`failureMemory >= 2*probeInterval`, `interval <= probeInterval`,
`settlingMax >= probeInterval`, and `drawGrace < probeInterval`, for defaults and merged pools.

A pool can also be paused with `herzi.redbull.io/paused: "true"`.
`herzi.redbull.io/state` stores bounded durable failure/probe evidence;
`herzi.redbull.io/blocked-until` is written BEFORE every POST, cleared with JSON null
after a definite parsed outcome, and extended from outcome time for ambiguous/rejected
outcomes. Failure to prewrite prevents the POST; failed clear leaves expiry protection.
Running recovery proof requires post-failure start, age >= drawGrace, and completed
`acquire_servers`; completion of a post-failure workflow also proves recovery.
Unreadable failed history is `class="unknown"` and prevents Full spending.
See [operations](docs/ops/operating.md) for durable state and strict loading.

Pool IDs are `install-server-<mce>_<infraEnv>_<slot>` (slots 1–100); named IDs
are `install-server-<mce>_<lowercased-server>`. The first separator is `-`, then `_`
after the MCE and InfraEnv. Exact slot Describes account for pool runs; named runs
are outside pool accounting. The authoritative builders are
`/home/toto/code/workflows/shared/workflow_ids.py`.

Runs started before the workflows PR keep their previous IDs and are not accounted
for by this controller. Start every pool in dry run. Enable real growth
(`dryRunGrow: false`) only after verifying in Temporal that no previous-ID install
runs are still open. There is no automatic multi-family accounting.

### Configuration migration

Delete `lowPct` and `minBand` from both `defaults` and every pool override, even if
set to zero or null. They are rejected with field-path migration diagnostics, for example:

```text
defaults.lowPct was removed: use highPct as the single target buffer percentage; delete lowPct
pools.<name>.minBand was removed: the controller no longer has a ceiling; delete minBand
```

These are document/schema errors: startup fails, and a reload rejects the entire update,
retaining the last good configuration. Invalid values of supported keys in merged pools
instead publish with `PoolErrors` and block only those pools (`reason="config_invalid"`).
Deploy the migrated config with the new image. Rollback requires the matching old config
as well as the old image; rollout undo alone does not restore ConfigMaps. Choose
`highPct: 15` explicitly for the former target buffer; the new default is 10.

## Decision table

From design Part 2, Test table for Decide. Validation runs first: failed reads,
invalid selectors, early-bound/deleting pools, pause, opt-out, active ambiguity
blocks and missing pollers prevent growth. The rows below describe the pure decision
stage after successful validation; supply always includes open runs.

```text
buffer = max(minFree, (highPct*demand + 50)/100)  # integer half-up rounding
target = demand + buffer
need   = target - (assigned + claimable + settling + open)
```

With valid observations and an unblocked pool, positive need enters Full/Probe growth.
At or above target, the controller holds (`HoldAtOrAboveTarget = "at or above target"`),
before considering failures or probes. Capacity is never reclaimed: lowering demand or
the buffer only stops additions. Spare capacity can persist indefinitely.

With defaults `highPct: 10` and `minFree: 2`:

| Demand | Rounded percentage | Buffer | Target |
|---:|---:|---:|---:|
| 0 | 0 | 2 | 2 |
| 4 | 0 | 2 | 6 |
| 6 | 1 | 2 | 8 |
| 10 | 1 | 2 | 12 |
| 12 | 1 | 2 | 14 |
| 20 | 2 | 2 | 22 |
| 24 | 2 | 2 | 26 |
| 25 | 3 | 3 | 28 |
| 30 | 3 | 3 | 33 |
| 50 | 5 | 5 | 55 |
| 100 | 10 | 10 | 110 |

The default target buffer is now 10%, lower than the former 15% for large pools
and equal to the `minFree` floor for small ones. Demand 20 previously had three spare
machines (target 23); now it has two (target 22), the floor. With integer half-up
rounding, the floor decides the buffer through demand 24; the percentage takes over
at demand 25: `(10*25+50)/100 = 3`. Set `highPct: 15` explicitly to retain the former
target behavior. To raise the buffer, set an explicit `highPct: 30` or increase
`minFree`. Because the controller never removes servers, a larger buffer adds
permanent capacity. Capacity is never reclaimed.

Defaults and merged pools cap `highPct` at 1000 and `minFree` at 100000 (both
must be non-negative). Invalid defaults reject the document; invalid merged pool
settings block only that pool with `config_invalid`. Summed eligible demand above
10,000,000 blocks its pool with an `implausible demand` problem. Target and supply
arithmetic is checked even for inputs outside these limits: an unrepresentable
calculation holds with `HoldTargetOutOfRange = "target out of range"` and cannot grow.

Each custom-buffer row uses `highPct: 0, minFree: <buffer>`; the demand-50 row
uses defaults (`highPct: 10, minFree: 2`). Probe rows use the default timings.
Validation runs first; unmanaged or blocked pools never order.

| demand | assigned | claimable | settling | open | recent evidence | buffer | expect |
|---|---|---|---|---|---|---|---|
| 20 | 18 | 3 | 0 | 0 | – | 2 | Grow 1 Full (target 22) |
| 20 | 20 | 2 | 0 | 0 | – | 2 | Hold (at or above target) |
| 20 | 20 | 6 | 0 | 0 | – | 2 | Hold (at or above target) |
| 20 | 18 | 3 | 0 | 1 | – | 2 | Hold (supply 22) |
| 20 | 20 | 6 | 0 | 2 | – | 2 | Hold (supply 28) |
| 20 | 4 | 3 | 16 | 0 | – | 2 | Hold (supply 23) |
| 12 | 4 | 3 | 16 | 0 | – | 2 | Hold (supply 23) |
| 0 | 0 | 0 | 0 | 0 | – | 2 | Grow 2 Full (seed) |
| 0 | 0 | 0 | 0 | 0 | unmanaged | 2 | Blocked (Validate) |
| 6 | 0 | 0 | 0 | 0 | capacity fail 3 min ago, no proof | 2 | Hold "probe wait" |
| 6 | 0 | 0 | 0 | 0 | capacity fail 21 min ago | 2 | Grow 1 Probe |
| 6 | 0 | 0 | 0 | 1 | capacity fail 21 min ago, run started 1 min ago | 2 | Hold "probe running" |
| 6 | 0 | 0 | 0 | 1 | capacity fail 21 min ago, run started 3 min ago, acquire_servers completed | 2 | Grow 7 Full |
| 10 | 4 | 0 | 0 | 6 | capacity fails at 10:00:20; open work started 10:00:00 | 2 | Hold "probe wait" until scheduled probe; older open work does not suppress it |
| 6 | 0 | 1 | 0 | 0 | capacity fail 30 min ago; Completed run started after failure | 2 | Grow 7 Full |
| 6 | 0 | 0 | 0 | 0 | capacity fail 30 min ago; Completed run started before failure | 2 | Grow 1 Probe |
| 6 | 0 | 0 | 0 | 0 | 2 distinct other failures since success | 2 | Grow 8 Full |
| 6 | 0 | 0 | 0 | 0 | 3 distinct other failures since success | 2 | Grow 1 Probe |
| 50 | 0 | 0 | 0 | 0 | – | 5 (default) | Grow 55 Full → Plan one request of 15 |
| 9 | 12 | 0 | 0 | 0 | – | 2 | Hold; sustained assigned > demand alerts |
| 6 | 6 | 1 | 0 | 0 | – | 2 | Grow 1 Full (target − 1) |
| 6 | 6 | 2 | 0 | 0 | – | 2 | Hold (target equality) |
| 6 | 6 | 3 | 0 | 0 | capacity/unknown/three-other failures | 2 | Hold (target + 1); no probe |
| 6 | 6 | 4 | 0 | 0 | – | 2 | Hold (at or above target) |

Additional coverage: zero buffer (`highPct: 0, minFree: 0`); exact-half rounding
(demand 5, `highPct: 10, minFree: 0` gives target 6); and at/above target Holds
for every supply composition, including failure evidence. Under defaults,
demand 20 / assigned 18 / claimable 3 orders 1 toward target 22; even assigned 21
with no other supply needs 1. More supply never increases the order count.


## Metrics

The catalogue below is copied from [PLAN.md](docs/PLAN.md) and implemented in `internal/metrics`.
Prefix `herzi_`. Labels: `pool` = InfraEnv name. Slash abbreviations share the
preceding `herzi_pool_` prefix (for example `_assigned` means `herzi_pool_assigned`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `herzi_pool_demand` / `_assigned` / `_claimable` / `_settling` / `_stuck_agents` / `_open_runs` | gauge | pool | the counted terms |
| `herzi_pool_target` / `_need` | gauge | pool | the formula outputs |
| `herzi_pool_blocked` | gauge 0/1 | pool, reason | pool blocked this pass |
| `herzi_pool_probe_mode` | gauge 0/1 | pool | hardware looks missing |
| `herzi_pool_failure_class` | gauge 0/1 | pool, class (`capacity`,`other`,`unknown`) | last failure class |
| `herzi_orders_total` | counter | pool, mode (`full`,`probe`), result (`started`,`short`,`ambiguous`,`rejected`,`dry_run`) | install requests |
| `herzi_machines_ordered_total` | counter | pool | machines requested |
| `herzi_reconcile_duration_seconds` | histogram | — | per reconcile |
| `herzi_sweep_duration_seconds` | histogram | — | per pool sweep |
| `herzi_stuck_after_seconds` | gauge | none | Startup stuckAfter threshold in seconds; process constant |
| `herzi_oldest_open_run_seconds` | gauge | pool | for the stuck-run alert |
| `herzi_pollers` | gauge | queue (`activity`,`workflow`,`segment`) | poller counts |
| `herzi_warnings_total` | counter | kind | hub-scoped findings (stray Agent, unattributable NodePool, ...) |
| `herzi_config_valid` | gauge 0/1 | — | last config update accepted |
| `herzi_last_successful_reconcile_timestamp_seconds` | gauge | pool | liveness of a pool |


Alerts use only these names. See [alert rules](config/prometheus/herzi-alerts.yaml)
and the [runbook](docs/ops/runbook.md). Platform scrape-target alerts are also
needed to detect a missing controller before pool series exist.

## Documentation

- [Design, Parts 1–3](docs/design/herzi-design.md)
- [Plan, ownership, metric contract and production definition of done](docs/PLAN.md)
- [Operating guide and real-hub verification](docs/ops/operating.md)
- [Alert runbook](docs/ops/runbook.md)
- [Minimal RBAC and marker review](docs/ops/rbac.md)
- [Packaging validation, assumptions and scaffold changes](docs/ops/packaging.md)
- [Future isolated Kind e2e coverage](test/e2e/README.md)
