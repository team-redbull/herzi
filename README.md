# herzi

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

**v1 shrink is log-only (`WOULD REMOVE`).** Grow is the intended real operation;
VM creation, cooldown, real shrink and route idempotency are backlog items.
This work package delivers packaging, not completed controller wiring. The current
entrypoint still lacks `--config`, secret loading, the controller/metric wiring
and required lease shutdown settings. The generated controller RBAC and envtest
CRD path also await WP6. Do not deploy this image for production orders until the
[release gates](docs/ops/operating.md#integration-status-and-release-gates) pass.
Docker builds, cluster behavior and hosted CI were not executed in this workspace.

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
make test-integration         # envtest; requires the WP6 suite gate
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
./bin/manager --leader-elect --config=config/samples/herzi-config.yaml \
  --health-probe-bind-address=:8081 --metrics-bind-address=:8443
```

Deployment requires the hub's real external CRDs, reviewed generated RBAC and
provisioned TLS Secrets. Envtest CRDs are permissive test fixtures only. Select
an immutable image and a deliberately chosen kubeconfig context.

```bash
make manifests                # WP6 controller markers generate role.yaml
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
| stuckAfter | 2h | Hub | Open-run warning age; shipped alert fixed at 2h |
| temporalAddress | Required/site-specific | Hub | Temporal host:port; sample uses TLS port 7233 |
| temporalNamespace | Required/site-specific | Hub | Temporal namespace; sample uses default |
| activityQueue | Required/site-specific | Hub | Hub activity queue; server-lifecycle-activity-<mce> |
| workflowQueue | Required/site-specific | Hub | Install workflow queue; sample install-server-workflow |
| segmentQueue | Required/site-specific | Hub | Segment activity queue; verify actual name |
| lowPct | 15 | Pool | Lower spare percentage, integer half-up rounding |
| highPct | 30 | Pool | Upper spare percentage |
| minFree | 2 | Pool | Lower spare floor; include Replace upgrade surge |
| minBand | 1 | Pool | Minimum band between target and ceiling |
| maxOpenRunsPerPool | 15 | Pool | Concurrent install load cap, at most maxSlots |
| probeInterval | 20m | Pool | Minimum interval between capacity probes |
| drawGrace | 2m | Pool | Running-after-failure proof of new hardware |
| failureMemory | 6h | Pool | Closed failure evidence window |
| settlingMax | 60m | Pool | Unclaimable Agent settling allowance |
| dryRunGrow | false | Pool | Log WOULD INSTALL without posting |
| paused | false | Pool | Observe but never order |
| defaults | Pool defaults above | Hub structure | Base for every listed pool |
| pools | Empty; no managed pools | Hub structure | InfraEnv name to pool override mapping |

See the complete [sample](config/samples/herzi-config.yaml), including all Temporal
fields in the shared config contract. TLS paths and API bearer token are supplied
via Secret mounts/environment, not YAML. A pool can also be paused with
`herzi.redbull.io/paused: "true"`; `herzi.redbull.io/blocked-until` persists the
ambiguous-request wait on its InfraEnv. See [operations](docs/ops/operating.md)
for strict loading, override validation, rollout, upgrades, rollback and lease loss.

## Decision table

From design Part 2, Test table for Decide. Validation runs first: failed reads,
invalid selectors, early-bound/deleting pools, pause, opt-out, active ambiguity
blocks and missing pollers prevent growth. The rows below describe the pure decision
stage after successful validation; supply always includes open runs.

Each row carries its own buffers (`low` / `high`), so it does not depend on the percentages.
Columns: demand, assigned, claimable, settling, open, recent closed runs, then the expected intent.

| demand | assigned | claimable | settling | open | recent | low / high | expect |
|---|---|---|---|---|---|---|---|
| 20 | 18 | 3 | 0 | 0 | – | 2 / 4 | Grow 1 Full (21 < 22) |
| 20 | 20 | 2 | 0 | 0 | – | 2 / 4 | Hold (22 = target) |
| 20 | 20 | 6 | 0 | 0 | – | 2 / 4 | WouldRemove 4 (26 > 24, claimable 6) |
| 20 | 18 | 3 | 0 | 1 | – | 2 / 4 | Hold (21 + 1 = 22) |
| 20 | 20 | 6 | 0 | 2 | – | 2 / 4 | Hold (running > 0) |
| 20 | 4 | 3 | 16 | 0 | – | 2 / 4 | Hold (supply 23 >= 22), no WouldRemove (settling) |
| 12 | 4 | 3 | 16 | 0 | – | 2 / 4 | Hold (supply 23 >= 14) |
| 0 | 0 | 0 | 0 | 0 | – | 2 / 3 managed | Grow 2 Full (seed) |
| 0 | 0 | 0 | 0 | 0 | – | not managed | Blocked (Validate), no order |
| 6 | 0 | 0 | 0 | 0 | capacity fail 3 min ago, no proof | 2 / 3 | Hold "probe wait" |
| 6 | 0 | 0 | 0 | 0 | capacity fail 21 min ago | 2 / 3 | Grow 1 Probe |
| 6 | 0 | 0 | 0 | 1 | capacity fail 21 min ago, run started 1 min ago | 2 / 3 | Hold "probe running" |
| 6 | 0 | 0 | 0 | 1 | capacity fail 21 min ago, run started 3 min ago | 2 / 3 | Grow 7 Full (proof) |
| 10 | 4 | 0 | 0 | 6 | 4 capacity fails closed 10:00:20, the 6 runs started 10:00:00 | 2 / 3 | Hold "probe running" (no proof: started before the failure) |
| 6 | 0 | 1 | 0 | 0 | capacity fail 30 min ago, run Completed 5 min ago, started after the fail | 2 / 3 | Grow 7 Full |
| 6 | 0 | 0 | 0 | 0 | capacity fail 30 min ago, only a Completed run that started BEFORE it | 2 / 3 | Grow 1 Probe (not proof) |
| 6 | 0 | 0 | 0 | 0 | 2 non-capacity fails, none since success | 2 / 3 | Grow 8 Full (needs 3 to probe) |
| 6 | 0 | 0 | 0 | 0 | 3 non-capacity fails, none since success | 2 / 3 | Grow 1 Probe |
| 50 | 0 | 0 | 0 | 0 | – | 8 / 15 | Grow 58 Full → Plan one request of 15 |
| 9 | 12 | 0 | 0 | 0 | – | 2 / 3 | Hold; alert assigned > demand |
| 6 | 6 | 3 | 0 | 0 | – | 2 / 3 | Hold (total 9 = ceiling 9 is not above it; boundary test) |
| 6 | 6 | 4 | 0 | 0 | – | 2 / 3 | WouldRemove 2 (total 10 > ceiling 9; surplus = min(10 − 8, claimable 4) = 2) |


## Metrics

The catalogue below is copied from [PLAN.md](docs/PLAN.md); WP6 implements it.
Prefix `herzi_`. Labels: `pool` = InfraEnv name. Slash abbreviations share the
preceding `herzi_pool_` prefix (for example `_assigned` means `herzi_pool_assigned`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `herzi_pool_demand` / `_assigned` / `_claimable` / `_settling` / `_stuck_agents` / `_open_runs` | gauge | pool | the counted terms |
| `herzi_pool_target` / `_ceiling` / `_need` | gauge | pool | the formula outputs |
| `herzi_pool_blocked` | gauge 0/1 | pool, reason | pool blocked this pass |
| `herzi_pool_probe_mode` | gauge 0/1 | pool | hardware looks missing |
| `herzi_pool_failure_class` | gauge 0/1 | pool, class (`capacity`,`other`) | last failure class |
| `herzi_orders_total` | counter | pool, mode (`full`,`probe`), result (`started`,`short`,`ambiguous`,`rejected`,`dry_run`) | install requests |
| `herzi_machines_ordered_total` | counter | pool | machines requested |
| `herzi_would_remove_total` | counter | pool | shrink log lines |
| `herzi_reconcile_duration_seconds` | histogram | — | per reconcile |
| `herzi_sweep_duration_seconds` | histogram | — | per pool sweep |
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
