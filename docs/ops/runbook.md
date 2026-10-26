# herzi alert runbook

Implements design Part 2, “Alert by failure type (metrics, not decisions)”,
and Part 3, orphan-bound Agents and stuck runs. These procedures apply once WP6
implements the metric catalogue and controller wiring. Replace pool names with the
alert's `pool` label; the namespace below assumes the shipped example.

## Common confirmation commands

```bash
export HERZI_NAMESPACE=herzi-system
export AGENT_NAMESPACE=inventory
export POOL=32-128-zone-1
kubectl -n "$HERZI_NAMESPACE" get deployment herzi
kubectl -n "$HERZI_NAMESPACE" get pods -l app.kubernetes.io/name=herzi -o wide
kubectl -n "$HERZI_NAMESPACE" logs deployment/herzi -c manager --since=1h
kubectl -n "$HERZI_NAMESPACE" get events --sort-by=.lastTimestamp
kubectl -n "$HERZI_NAMESPACE" get leases.coordination.k8s.io -o yaml
kubectl -n "$AGENT_NAMESPACE" get infraenvs.agent-install.openshift.io "$POOL" -o yaml
kubectl -n "$AGENT_NAMESPACE" get agents.agent-install.openshift.io -o yaml
kubectl get nodepools.hypershift.openshift.io -A -o yaml
```

Run each PromQL expression below in the hub Prometheus expression browser (retain
`pool`, `reason` and `queue` labels). To inspect raw metrics with a credential whose
ServiceAccount has the `metrics-reader` ClusterRole:

```bash
kubectl -n herzi-system port-forward service/herzi-metrics 8443:8443
# In another terminal; METRICS_TOKEN is supplied securely, not printed to logs.
kubectl -n herzi-system get secret herzi-metrics-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/herzi-metrics-ca.crt
curl --fail --cacert /tmp/herzi-metrics-ca.crt \
  --resolve herzi-metrics.herzi-system.svc:8443:127.0.0.1 \
  -H "Authorization: Bearer $METRICS_TOKEN" \
  https://herzi-metrics.herzi-system.svc:8443/metrics
```

The target TLS certificate must cover `herzi-metrics.herzi-system.svc`. The
Prometheus ServiceAccount needs a binding to `metrics-reader`. Use platform alerts
for missing scrape targets: catalogue gauges alone cannot detect every missing
series, especially before the first reconcile.

<a id="needs-hardware"></a>

## HerziPoolNeedsHardware

Capacity errors (ServerNotAvailableError or ServerReservedError) have kept the pool probing for over 30 minutes.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_pool_probe_mode == 1 and on(pool) herzi_pool_failure_class{class="capacity"} == 1
herzi_pool_need{pool="32-128-zone-1"}
```

Inspect server-scan availability/reservations for this pool; rack or release matching hardware and allow the next 20-minute probe. A run started after the failure and alive for drawGrace restores full orders. Do not raise the open-run cap to cure missing hardware.

<a id="install-failing"></a>

## HerziInstallFailing

The last failure is outside the capacity class. One failure alerts; three consecutive failures without success can cause probing.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_pool_failure_class{class="other"} == 1
herzi_pool_failure_class{pool="32-128-zone-1",class="other"}
```

Inspect the latest failed slot close event in Temporal and the install worker logs. Repair boot, discovery, network or ignition failures for the reported error type. Capacity remediation alone will not fix AgentNeverAppearedError or a broken install path.

<a id="pool-blocked"></a>

## HerziPoolBlocked

Validation has prevented growth continuously for 15 minutes. The reason label identifies the blocker; intentional pause/unmanaged states may be expected.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_pool_blocked == 1
herzi_pool_blocked{pool="32-128-zone-1"}
```

```bash
kubectl -n "$AGENT_NAMESPACE" get infraenvs.agent-install.openshift.io "$POOL" -o jsonpath='{.metadata.annotations.herzi\.redbull\.io/blocked-until}{"\n"}'
```

For read/sweep errors restore API or Temporal access and RBAC. For zero pollers repair workers. For selector/config errors correct only the affected pool. For paused/unmanaged states confirm operator intent and silence as appropriate. For an ambiguity block let it expire and require a full sweep before ordering; never erase it to force a retry.

<a id="stuck-agents"></a>

## HerziStuckAgents

One or more unbound Agents have remained unclaimable past settlingMax (60 minutes by default).

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_pool_stuck_agents > 0
herzi_pool_stuck_agents{pool="32-128-zone-1"}
```

Inspect status transitions, connectivity, approval and stale agentMachineRef labels. Repair the release/reboot/approval flow. Remove a stale label only after confirming that HyperShift has released the machine. Verify disk wipe before registration; do not force an Agent into claimable state.

<a id="stuck-open-runs"></a>

## HerziStuckOpenRuns

An install slot has been open for more than the fixed two-hour alert threshold. This alert alone does not change the decision.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_oldest_open_run_seconds > 7200
herzi_pool_open_runs{pool="32-128-zone-1"}
```

Inspect pending workflow activities and all three worker queues. Repair the failing activity or worker first. Cancel/terminate a workflow only after establishing machine ownership and accounting for cleanup; the controller does not automatically retry a still-open slot.

<a id="no-pollers"></a>

## HerziQueueHasNoPollers

One of the activity, workflow or segment queues has zero pollers. A queue error also blocks growth, but may surface through the blocked-pool alert instead.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_pollers == 0
herzi_pollers
```

Check Temporal DescribeTaskQueue credentials, actual task queue names and the corresponding worker deployment. Restore workers and verify positive poller counts before expecting growth. Poller records linger roughly five minutes after worker death.

<a id="not-reconciling"></a>

## HerziNotReconciling

A reported pool has had no successful reconcile for ten minutes, sustained for another five minutes. Missing series and a dead scrape target need separate platform monitoring.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
time() - herzi_last_successful_reconcile_timestamp_seconds > 600
herzi_last_successful_reconcile_timestamp_seconds{pool="32-128-zone-1"}
```

Inspect Pod readiness/restarts, lease ownership, errors and external request deadlines. Restore Kubernetes/Temporal connectivity, fix RBAC or recover the controller. Check that the pool is still configured. Do not add replicas to bypass a stuck leader.

<a id="config-invalid"></a>

## HerziConfigInvalid

The latest config update was rejected; this does not imply the invalid values are in use.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_config_valid == 0
herzi_config_valid
```

```bash
kubectl -n herzi-system get configmap herzi-config -o go-template='{{index .data "config.yaml"}}'
kubectl -n herzi-system logs deployment/herzi -c manager --since=30m
```

Compare the mounted ConfigMap with the intended config; fix unknown keys, duration strings and merged constraints. Reapply the ConfigMap and confirm herzi_config_valid returns to 1. A per-pool validation failure can also appear as a pool block.

<a id="assigned-exceeds-demand"></a>

## HerziAssignedExceedsDemand

Assigned Agents have exceeded active NodePool demand for three hours, suggesting orphaned bindings.

Confirm with the common kubectl/log commands and these exact PromQL queries:

```promql
herzi_pool_assigned > on(pool) herzi_pool_demand
herzi_pool_assigned{pool="32-128-zone-1"} - on(pool) herzi_pool_demand{pool="32-128-zone-1"}
```

Compare bound clusterDeploymentName references with active NodePools and hosted clusters. Investigate HyperShift release behavior, deleted/paused NodePools and upgrade surge. Repair bindings through the owning operator after confirming ownership. herzi cannot identify or remove orphan-bound Agents automatically.

