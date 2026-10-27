# Operating herzi

This implements design Part 2, Config: “Hub settings ... cannot differ per pool”
and Part 1, cases 9–15 (ambiguity, lease loss, pause and dry run). The design is
the behavior contract.

## Install on OpenShift

Run from the repository root against the intended MCE hub. These steps also apply
to plain Kubernetes: use `kubectl` instead of `oc`, an externally pushed image,
and `networkPolicy.dns: kubernetes`. The chart requires Kubernetes 1.27 or newer.

### 1. Check hub prerequisites

```bash
oc get crd agents.agent-install.openshift.io infraenvs.agent-install.openshift.io nodepools.hypershift.openshift.io
oc auth can-i create clusterroles.rbac.authorization.k8s.io
```

Expect all three operator-owned CRDs and `yes`. Install the agent-install and
HyperShift operators first if they are missing. Use a cluster-admin identity for
installing the ClusterRole and its binding, or have the administrator provision
all RBAC and select `rbac.create: false` as described in [rbac.md](rbac.md).

The controller pod must reach Kubernetes, Temporal's configured host:port and the
workflows API. Set `config.installURL` to **the same full URL used today to call
install-server**: use the Service address if the workflows brain is on this
cluster, otherwise its Route, retaining `/workflows/server-lifecycle/install-server`.
Do not invent a new endpoint. `config.hubName` is the canonical MCE name used in
install requests, normally `ocp4-...`; match the hub worker queue and Temporal
namespace. Confirm these network paths from the hub before enabling growth.

### 2. Build and publish an immutable image

For a registry you can push to (replace the example repository and tag):

```bash
make docker-build IMG=registry.example.com/herzi:0.1.0 VERSION=0.1.0 COMMIT=unknown
make docker-push IMG=registry.example.com/herzi:0.1.0
# Podman alternative:
make docker-build docker-push CONTAINER_TOOL=podman IMG=registry.example.com/herzi:0.1.0 VERSION=0.1.0 COMMIT=unknown
```

Expect a successful build and push. Set `VERSION` to your release and `COMMIT` to
the source revision recorded by your build system; `unknown` is a literal fallback.
The Dockerfile builds a static binary, then copies it into a distroless image.

Alternatively, use an OpenShift binary Docker build (create the project once):

```bash
oc new-project herzi-system
oc new-build --binary --strategy=docker --name=herzi -n herzi-system
oc start-build herzi --from-dir=. --follow -n herzi-system
```

Expect the build to complete and push an ImageStream image to
`image-registry.openshift-image-registry.svc:5000/herzi-system/herzi:latest`.
For this path use repository
`image-registry.openshift-image-registry.svc:5000/herzi-system/herzi` and tag
`latest`; use `image.pullPolicy: Always` when reusing that tag. Prefer assigning
an immutable release tag for upgrades. If the project already exists, skip
`oc new-project`. The binary build uses the Dockerfile's default version metadata
unless build arguments are configured separately.

Air-gapped clusters must mirror both Dockerfile base images: `golang:1.27.3` and
`gcr.io/distroless/static:nonroot`. Configure the build's registry mirror access
before starting it; the builder also needs a reachable Go module source/cache.

### 3. Write hub values

```bash
cp charts/herzi/ci/example-values.yaml my-hub-values.yaml
```

Expect a local values file with three illustrative pools; edit it for this hub.
Required values are `image.repository`, `config.hubName`, `config.installURL` and
`config.temporalAddress`. Replace the example endpoints and `config.activityQueue`
(or leave the queue empty to derive it from hubName). List only the InfraEnv names
to manage under `config.pools`. Set `config.defaults.dryRunGrow: true` and keep
**every pool** at `dryRunGrow: true`; a per-pool false overrides the default.
Provision any opted-in Secrets separately in `herzi-system` before installation.
Missing required values fail Helm validation early; the default manages no pools.

### 4. Render and install

```bash
export PATH=/home/linuxbrew/.linuxbrew/bin:$PATH
helm lint --strict charts/herzi -f my-hub-values.yaml
helm template herzi charts/herzi -n herzi-system -f my-hub-values.yaml --set image.tag=0.1.0
helm upgrade --install herzi charts/herzi -n herzi-system --create-namespace -f my-hub-values.yaml --set image.tag=0.1.0
# Equivalent repository target:
make deploy VALUES=my-hub-values.yaml IMG_REPO=registry.example.com/herzi IMG_TAG=0.1.0 NAMESPACE=herzi-system
```

Expect lint success, rendered YAML for review, then a deployed Helm release.
Use your chosen image tag (including `latest` for the binary build) consistently.
`make deploy` requires `VALUES`, `IMG_REPO` and `IMG_TAG`; `NAMESPACE` defaults to
`herzi-system`. The ConfigMap is rendered from `config` in values. The chart has
no Namespace template: Helm creates the namespace with `--create-namespace`.
The pod sets no fixed UID or group; OpenShift restricted-v2 assigns one from the
namespace range. The root filesystem is read-only and all capabilities are dropped.

### 5. Verify before permitting orders

```bash
oc -n herzi-system get pods
oc -n herzi-system rollout status deployment/herzi --timeout=120s
oc -n herzi-system logs deployment/herzi -c manager --since=10m
```

Expect a Ready manager, successful rollout and observations/`Would install` logs
for pools needing capacity, with no real install requests. Check the exact
permission commands in [rbac.md](rbac.md#confirm-on-the-real-hub). Readiness alone
does not prove Temporal or install API connectivity. Complete [To verify on a real
hub](#to-verify-on-a-real-hub) and the [dry-run rollout](#dry-run-rollout) before
changing one small pool to real growth.

### 6. Upgrade, roll back and uninstall

Use the [upgrade and rollback](#upgrade-and-rollback) commands below with matching
image and values versions. Helm rollback restores the old values too, including
the chart-rendered ConfigMap. Removed-key rejection means an old image needs the
old values. Pool edits can safely use `helm upgrade`: they reload live without
changing the startup checksum or restarting the pod.

```bash
helm uninstall herzi -n herzi-system
# Equivalent repository target:
make undeploy NAMESPACE=herzi-system
```

Expect the release resources, including ConfigMap, RBAC and optional monitoring
and NetworkPolicy, to be removed. No chart-owned CRDs or Namespace resources are
left because the chart installs neither. The Helm-created namespace and externally
provisioned Secrets remain, as do MCE/HyperShift resources, OpenShift build objects,
existing workflows and capacity already added. Uninstall does not reclaim machines.

## Chart values reference

The [chart guide](../../charts/herzi/README.md) and
[values.yaml](../../charts/herzi/values.yaml) define the deployment contract.
Each top-level value is listed here; controller defaults and pool settings are
also documented in the chart guide.

| Key | Purpose |
| --- | --- |
| `image` | Required repository, optional tag, pull policy (`IfNotPresent`). |
| `replicaCount` | Deployment replica count, default 1; leader election selects one active process. |
| `nameOverride` | Override the chart name portion of generated names. |
| `fullnameOverride` | Override the whole generated base name. |
| `goMemLimit` | GOMEMLIMIT, default `400MiB`. Adjust with the container memory limit. |
| `resources` | Requests `100m` CPU/`128Mi`, limits `1` CPU/`512Mi`. |
| `serviceAccount` | `create`, `name`, `annotations`; when not created, empty name selects `default`. |
| `rbac` | `create` gates all roles/bindings; `metricsReader.subjects` optionally grants `/metrics` access. |
| `config` | Exact controller config, described below; rendered verbatim as YAML in the ConfigMap. |
| `temporal` | Existing CA/mTLS Secret and optional API-key Secret references. |
| `installApi` | Optional existing bearer-token Secret reference. |
| `metrics` | HTTPS metrics Service enabled by default, port 8443, optional existing TLS Secret. |
| `serviceMonitor` | Opt-in monitor: interval, scrapeTimeout, labels, tlsConfig, bearerTokenSecret. |
| `prometheusRule` | Opt-in shipped alerts and additional labels. |
| `networkPolicy` | Opt-in egress restrictions and metrics ingress; platform DNS and endpoint CIDRs/ports. |
| `nodeSelector` | Pod node label selection. |
| `tolerations` | Pod tolerations. |
| `affinity` | Pod affinity and anti-affinity. |
| `topologySpreadConstraints` | Pod topology spread constraints. |
| `priorityClassName` | Existing PriorityClass name. |
| `podAnnotations` | Extra pod annotations; startup checksum takes precedence. |
| `podLabels` | Extra pod labels; chart labels take precedence. |
| `imagePullSecrets` | Existing registry Secret references, e.g. `[{name: registry}]`. |
| `extraArgs` | Additional manager flags; avoid duplicating chart-owned arguments. |
| `extraEnv` | Additional Kubernetes EnvVar entries; avoid duplicating chart-owned names. |

## Integration status and release gates

The controller, metrics, manager wiring (`--config`, leader election with a stable id and release on
cancel, secure metrics, HTTP/2 off, a lazy Temporal client, credentials from the environment) and the
generated RBAC are implemented and covered by unit tests plus an envtest suite that runs against a real
kube-apiserver. `golangci-lint` and `govulncheck` are clean. These things have **not** been exercised and are
the release gates, to be closed on a real hub before the first real order:

1. **A container build and a deployment** (no Docker or cluster was available during development).
2. **Temporal and the install route, live:** `DescribeTaskQueue` permission and the task-queue names on your
   server, the `NotFound` error and the namespace retention (retention must be well above
   `failureMemory`), and that the controller's network path to the workflows API works.
3. **The assumptions in the design's "To verify on a real hub" list** (Agent labels and states, NodePool
   `pausedUntil`, wipe-before-register ordering).
4. **The hosted CI workflow** (it is written and every step maps to a Makefile target that runs locally).

Roll out in this order: install with every pool at `dryRunGrow: true`; read the `Would install` lines and the
`herzi_pool_*` gauges for a few days; enable one small pool; then the rest. `make helm-test` checks the rendered manager permissions and binding,
and `make manifests-drift` guards the generated rules; both run in CI.

## Config model

The mounted file is `/etc/herzi/config.yaml`, from the stable ConfigMap
`herzi-config` in `herzi-system` for the default release name. Edit [example values](../../charts/herzi/ci/example-values.yaml)
for the hub before rendering. Helm values put hub fields under `config`, pool defaults
under `config.defaults` and opted-in InfraEnv names under `config.pools`. In the
rendered controller file, hub fields are top-level with `defaults` and `pools`. Every pool field
can be overridden. Absent fields inherit; explicit `0` and `false` override.
An empty pool mapping opts in with all defaults. Durations are strings such as
`"120s"` and `"20m"`, never integer nanoseconds. All fields appear in the
[README reference](../../README.md#configuration).

Validation is strict: bad YAML, unknown keys, invalid duration syntax, hub settings or defaults
reject the document (startup fails; reload keeps the last good snapshot). `maxSlots` must
be exactly 100; tune load through `1 <= maxOpenRunsPerPool <= 100`.
Defaults and merged pools require `0 <= highPct <= 1000`, `0 <= minFree <= 100000`, positive durations,
`failureMemory >= 2*probeInterval`, `interval <= probeInterval`,
`settlingMax >= probeInterval`, and `drawGrace < probeInterval`.
Invalid merged pool settings remain published with `PoolErrors` and block only that
pool with `reason="config_invalid"`; healthy changes, including pauses, still apply.
A frozen snapshot serves each pass. There are exactly nine pool settings: `highPct`,
`minFree`, `maxOpenRunsPerPool`, `probeInterval`, `drawGrace`, `failureMemory`,
`settlingMax`, `dryRunGrow`, and `paused`.

herzi polls the mounted file every ten seconds and requeues all pools when an accepted change
appears; the directory mount avoids subPath so Kubernetes can refresh its contents (the kubelet
needs up to about a minute). Check both the accepted-config metric and the logs after updating.
Pool/default edits and live timing (`interval`, `reconcileTimeout`, `ambiguousWait`)
reload without a rollout. Among controller settings, only startup-bound values
(`hubName`, `agentNamespace`, `installURL`, `maxSlots`, `stuckAfter`,
`temporalAddress`, `temporalNamespace`, `activityQueue`, `workflowQueue`, `segmentQueue`)
change `checksum/startup-config` and restart pods. Thus `helm upgrade` is safe for
pool edits. Changes to image, Secret references or pod scheduling also change the
pod template and cause a rollout.

```bash
helm upgrade herzi charts/herzi -n herzi-system -f my-hub-values.yaml --set image.tag=0.1.0
kubectl -n herzi-system logs deployment/herzi -c manager --since=10m
```

To pause only one InfraEnv without changing config:

```bash
kubectl -n inventory annotate infraenvs.agent-install.openshift.io 32-128-zone-1 herzi.redbull.io/paused="true" --overwrite
# Resume only when the pool is safe to manage:
kubectl -n inventory annotate infraenvs.agent-install.openshift.io 32-128-zone-1 herzi.redbull.io/paused-
```

Config `paused: true` also pauses a pool; removing the annotation does not override
that setting. The controller still observes paused/unmanaged pools. NodePool
`pausedUntil` and deletion exclude its demand. The InfraEnv annotation
`herzi.redbull.io/blocked-until` is a controller-written RFC3339 UTC deadline,
written BEFORE each POST as attempt time + ambiguousWait (default 5m). Failure to
persist prevents the POST. The Probe schedule is persisted in the same prewrite.
All annotation patches use independent cancellation-detached, ten-second contexts.
A definite parsed 202 outcome clears the pending marker with JSON null; a failed
clear is logged and left to expire. Ambiguous/rejected outcomes retain it and extend
the deadline from outcome time; failed renewal still leaves the original marker.
A restart preserves the marker. Do not clear it to accelerate recovery: let it expire
and inspect every Temporal slot. Failed response items are resolved by the next sweep.
Every attempted POST invalidates the sweep; another order needs an observation strictly
newer than the previous attempt, including after the uncached Kubernetes recheck.

`herzi.redbull.io/state` stores compact bounded JSON (<= 1 KiB): `capFailAt`, up to
three distinct `otherFails` entries (`runId`, `closedAt`), `lastOKAt`, and
`probeNotBefore`. State merges with fresh uncached annotation data before writes,
retains evidence across slot reuse/restart, and expires failures after failureMemory.
Missing, corrupt or oversized state degrades to sweep-only evidence; unknown JSON
keys are ignored. A Running run proves recovery only if it started after the failure,
reached drawGrace and has a completed `acquire_servers` activity in run-pinned history.
A Completed workflow must also have started after the failure. Runtime alone supplies
no proof. Failed history reads produce `class="unknown"`, preventing Full spending.
TimedOut runs have no failure type; the sweeper assigns `pipeline.FailUnknown`
(`"unknown"`) without reading failure history. MergeState and Decide retain that
unknown failure evidence and prevent Full spending, allowing only scheduled probes.
Only post-failure open work suppresses the next scheduled probe; all open work still
counts against need and the open-run cap.

## Capacity policy

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

Open-run age is compared with the unlabeled `herzi_stuck_after_seconds` gauge,
initialized before reconciliation from startup `stuckAfter` (default 2h). It is a
process constant; changing the effective threshold requires restart. Prometheus adds
scrape target labels (including job and instance); the rule matches those labels
and ignores only pool, keeping each controller's threshold separate. The rule uses
`herzi_oldest_open_run_seconds > ignoring(pool) group_left herzi_stuck_after_seconds`.

## Secrets, TLS and secure monitoring

The chart never creates Secrets. Create references in the release namespace using
your secret manager; never put credentials in values or the ConfigMap. With no
Secret references, Temporal uses plaintext gRPC and metrics use a self-signed
certificate. Examples below are separate additions to your hub values.

Temporal TLS verifies the server with the existing Secret's `ca.crt`:

```yaml
temporal:
  tls:
    existingSecret: herzi-temporal-tls
    caKey: ca.crt
```

Temporal mTLS additionally requires `tls.crt` and `tls.key` in that same Secret:

```yaml
temporal:
  tls:
    existingSecret: herzi-temporal-tls
  mtls:
    enabled: true
    certKey: tls.crt
    keyKey: tls.key
```

Temporal API key (sets `HERZI_TEMPORAL_API_KEY` through secretKeyRef):

```yaml
temporal:
  apiKey:
    existingSecret: herzi-temporal-api
    key: api-key
```

Workflows bearer token (sets `HERZI_API_BEARER_TOKEN` through secretKeyRef):

```yaml
installApi:
  bearerToken:
    existingSecret: herzi-api
    key: token
```

Metrics TLS uses `tls.crt` and `tls.key` in an existing Secret. The certificate
must cover `herzi-metrics.herzi-system.svc` for these monitoring examples:

```yaml
metrics:
  tls:
    existingSecret: herzi-metrics-tls
```

The ServiceMonitor requires Prometheus Operator CRDs and matching Prometheus
selectors. OpenShift must have user-workload monitoring enabled for the namespace.
Provision a rotating token and CA Secret readable by the monitoring operator;
verify TLS rather than disabling verification. Use the metrics-reader subject
example below for that token's ServiceAccount.

```yaml
serviceMonitor:
  enabled: true
  interval: 30s
  scrapeTimeout: 10s
  labels:
    release: prometheus
  tlsConfig:
    insecureSkipVerify: false
    serverName: herzi-metrics.herzi-system.svc
    ca:
      secret: {name: herzi-metrics-tls, key: ca.crt}
  bearerTokenSecret: {name: herzi-prometheus-token, key: token}
```

Enable alert rules after installing Prometheus Operator CRDs; adapt labels to the
Prometheus rule selector:

```yaml
prometheusRule:
  enabled: true
  labels:
    release: prometheus
```

NetworkPolicy is opt-in. Replace **all** TEST-NET ranges below with actual endpoint
IPs and ports before enabling it. Empty CIDR lists allow no traffic for that
destination. OpenShift DNS uses `dns: openshift` (openshift-dns TCP/UDP 5353);
plain Kubernetes uses `dns: kubernetes` (kube-system CoreDNS TCP/UDP 53).

```yaml
networkPolicy:
  enabled: true
  dns: openshift
  metricsNamespaceSelector:
    matchLabels: {metrics: enabled}
  apiServer: {cidrs: [192.0.2.10/32], ports: [443, 6443]}
  temporal: {cidrs: [192.0.2.20/32], ports: [7233]}
  installApi: {cidrs: [192.0.2.30/32], ports: [443]}
```

The policy denies other ingress and egress. Check service DNAT, node-local DNS,
kubelet probes and the actual CNI behavior. DNS changes do not update ipBlocks.
Metrics ingress permits only the selected namespaces; label your monitoring
namespace accordingly or change the selector.

Grant authenticated metrics access to the scraping ServiceAccount:

```yaml
rbac:
  metricsReader:
    subjects:
      - kind: ServiceAccount
        name: prometheus
        namespace: monitoring
```

The chart mounts certificates read-only at mode 0444 so the OpenShift-assigned UID
can read them. Temporal files are under `/etc/herzi-temporal`, metrics files under
`/etc/herzi-metrics`. Credentials are read at startup: Secret content rotation
alone does not roll out pods. After rotating credentials, explicitly restart:

```bash
kubectl -n herzi-system rollout restart deployment/herzi
```

Complete examples are in `charts/herzi/ci/tls-values.yaml`, `mtls-values.yaml` and
`full-values.yaml`. Missing scrape targets need platform alerts in addition to
`HerziControllerDown`; a scrape gap otherwise silences pool alerts.

## Alert rule tests

The shipped alert rules have promtool unit tests in `charts/herzi/tests/alerts.test.yaml`. Run them with:

```bash
make test-alerts      # needs promtool on PATH; CI installs a pinned, checksum-verified one
```

Rules live in `charts/herzi/files/alerts.rules.yaml`; enable their chart resource with
`prometheusRule.enabled: true`. `hack/test-alerts.sh` runs promtool directly on the native rule
groups and the test file, without extracting a Kubernetes wrapper. The tests cover configured stuck
thresholds across several pools **and across two controller targets with different thresholds** (the series are
matched by controller target, not by an empty key), bound capacity in excess of demand for three hours versus
ordinary free spares, the controller's series going absent for ten minutes, and paused or pending pools being excluded from
the blocked-pool alert. The test file is not a Kubernetes resource and is not rendered by the chart.

## Dry-run rollout

1. Close the release gates above. Verify the real-hub checklist below,
   external CRDs, TLS trust, namespace/queues and the route's slot limit.
2. Set `dryRunGrow: true` explicitly on **every managed pool first** (the shipped
   three examples do this); also set `defaults.dryRunGrow: true` for new pools.
   Confirm there are no per-pool false overrides. Keep new pools paused until
   inventory attribution is confirmed. Even zero demand seeds minFree machines.
3. Build/test and deploy a fixed image tag. Review generated RBAC, inspect
   `helm template herzi charts/herzi -n herzi-system -f my-hub-values.yaml`, and
   provision referenced Secrets before the pod starts. Use the install commands above.
4. Compare `Would install` logs to NodePool replicas, assigned/claimable/settling
   gauges and open slot histories. Simulate an empty-capacity failure, missing
   pollers, pause and invalid config; confirm blocks and last-good config behavior.
5. Verify in Temporal that no previous-ID install runs are still open, as described below.
   Enable real growth by setting `dryRunGrow: false` for one pool. Watch orders,
   ambiguity blocks, Agents and need until stable, then enable remaining pools.
   To stop new orders restore dry run or pause; neither cancels existing workflows.

## Upgrade and rollback

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
as well as the old image; Helm rollback restores old release values and its rendered ConfigMap. Choose
`highPct: 15` explicitly for the former target buffer; the new default is 10.

Back up site values and keep Secrets in your secret manager. Compare config,
metrics, RBAC and workflow-ID compatibility across versions. First enable dry run,
inspect pending installs, then upgrade to a new immutable image and matching values.
The single replica uses RollingUpdate with maxSurge 0/maxUnavailable 1, allowing
an availability gap. A terminating pod can overlap its replacement; the lease and
immediate exit remain necessary to prevent overlapping active reconcilers. No PDB
is installed.

```bash
helm upgrade herzi charts/herzi -n herzi-system -f my-hub-values.yaml --set image.tag=0.1.1
kubectl -n herzi-system rollout status deployment/herzi --timeout=120s
helm history herzi -n herzi-system
# Choose a compatible revision from history (example: 1):
helm rollback herzi 1 -n herzi-system
kubectl -n herzi-system rollout status deployment/herzi --timeout=120s
```

Rollback restores the previous revision's chart, workload **and values**, including
its rendered ConfigMap and RBAC. Removed-key rejection means an old image must
receive its old values. Secrets are external and are not restored by rollback;
keep their credentials compatible. Inspect logs and metrics before resuming growth.
Uninstall and its exact retention behavior are documented in the installation steps.

## Lease loss and restart

Behavior: lease loss exits immediately with no graceful draining
(`GracefulShutdownTimeout = 0`); enable `LeaderElectionReleaseOnCancel` for clean
rollouts and exit as soon as the manager stops. The Pod's 30-second termination
grace is an upper bound for Kubernetes, not permission to keep ordering after
lease loss. The new leader reads the persisted pending marker and durable state annotation, then
describes all 100 fixed workflow IDs. Restart does not reset the probe schedule.

Leader election has no fencing. An old API request can still land after a new
leader's sweep; over-ordering by one batch remains an accepted v1 risk. Never
remove the block or blindly replay a POST after an uncertain response. Temporal
retention must comfortably exceed both probeInterval and failureMemory.

## To verify on a real hub

The design Part 3, F requires all of the following before enabling growth:

- Exact Agent InfraEnv label key and Agent state strings.
- Temporal credentials can call DescribeTaskQueue; verify the server version's
  not-found response, and distinguish NotFound from all other Describe errors.
- Temporal namespace retention and rate limits (a sweep describes maxSlots per
  pool, with bounded parallelism; retain history longer than failureMemory).
- Whether any NodePool still uses Replace upgrades, and its surge; size minFree
  for the largest desired instant scale-up plus that surge.
- Segment worker and install workflow queue names, and the hub activity queue.

Also verify the discovery-image disk wipe runs before Agent registration, fails
closed and covers every disk for both VM and bare-metal pools. Confirm all Agents
are auto-approved by the hub, since herzi's RBAC does not approve them. Verify
TLS identity, Secret rotation, restricted Pod admission (the Deployment sets no
fixed UID, so OpenShift's restricted-v2 SCC assigns one from the namespace range), RBAC, probes, lease loss, restart ambiguity blocks,
network policy and real worker failures. None of these cluster checks were run
in the packaging workspace.
