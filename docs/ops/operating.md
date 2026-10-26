# Operating herzi

This implements design Part 2, Config: “Hub settings ... cannot differ per pool”
and Part 1, cases 9–15 (ambiguity, lease loss, pause and dry run). The design is
the behavior contract.

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

Roll out in this order: install with every pool at `dryRunGrow: true`; read the `WOULD INSTALL` lines and the
`herzi_pool_*` gauges for a few days; enable one small pool; then the rest. `make check-rbac` fails when the
default install does not grant the controller what it reads (it caught a missing role once; it also runs in CI).

## Config model

The mounted file is `/etc/herzi/config.yaml`, from the stable ConfigMap
`herzi-config` in `herzi-system`. Edit [the sample](../../config/samples/herzi-config.yaml)
for the hub before rendering. Hub fields are top-level; pool defaults live in
`defaults`; only InfraEnv names listed in `pools` are managed. Every pool field
can be overridden. Absent fields inherit; explicit `0` and `false` override.
An empty pool mapping opts in with all defaults. Durations are strings such as
`"120s"` and `"20m"`, never integer nanoseconds. All fields appear in the
[README reference](../../README.md#configuration).

Validation is strict: unknown keys and unparsable files reject an update and
keep the last good snapshot. Merged per-pool constraints include nonnegative
percentages with lowPct <= highPct, minFree >= 0, minBand >= 1, open-run cap
between 1 and maxSlots, positive durations and drawGrace < probeInterval.
An invalid pool is blocked with its reason. A frozen snapshot serves each pass.
herzi polls the mounted file every ten seconds and requeues all pools when an accepted change
appears; the directory mount avoids subPath so Kubernetes can refresh its contents (the kubelet
needs up to about a minute). Check both the accepted-config metric and the logs after updating.

```bash
kubectl kustomize config/samples | kubectl -n herzi-system apply -f -
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
`herzi.redbull.io/blocked-until` is a controller-written RFC3339 timestamp persisted
after an ambiguous POST (timeout/connection failure/5xx or rejected 422). It
prevents new orders for `ambiguousWait` (5 minutes); a restart preserves it.
Do not clear it to accelerate recovery: first let it expire and inspect every
Temporal slot. A failed response item is also resolved by a subsequent sweep.

## Secrets, TLS and secure monitoring

No Secret values are shipped, and **none is required for the default install**: herzi speaks plain gRPC to
Temporal (as every client inside the hub does) and serves metrics with the self-signed certificate that
controller-runtime generates. TLS is opt-in through kustomize components, so a hub that does not use it
never waits for a Secret that does not exist:

| Component | Adds | Needs the Secret | Keys |
|---|---|---|---|
| `config/components/temporal-tls` | `HERZI_TEMPORAL_TLS_CA_FILE` / `_CERT_FILE` / `_KEY_FILE`, a read-only mount at `/etc/herzi-temporal` | `herzi-temporal-tls` | `ca.crt`; `tls.crt` + `tls.key` for mTLS |
| `config/components/metrics-tls` | `--metrics-cert-path`, a read-only mount at `/etc/herzi-metrics` | `herzi-metrics-tls` | `tls.crt`, `tls.key` (+ `ca.crt` for the ServiceMonitor) |

`config/overlays/tls` shows both together; copy it per hub and keep only the components whose Secrets exist.
Other secrets:

| Secret | Keys | Consumer |
|---|---|---|
| herzi-api (optional) | token | `HERZI_API_BEARER_TOKEN` environment variable (absent when the Secret is absent) |
| (any Secret you wire in) | api key | `HERZI_TEMPORAL_API_KEY` environment variable: a Temporal Cloud/API-key credential. Not set by default; add it to the Deployment yourself with a `secretKeyRef`. Never log or place it in the ConfigMap. |
| herzi-prometheus-token | token | ServiceMonitor authorization credential |

**Monitoring needs more than herzi does.** The shipped ServiceMonitor verifies the metrics certificate against the
`ca.crt` in `herzi-metrics-tls` and presents the token in `herzi-prometheus-token`, and the Prometheus
ServiceAccount must be bound to `metrics-reader`. So scraping requires the `metrics-tls` component, those Secrets
and that binding; the default install (self-signed metrics certificate) is scrapeable only with TLS verification
relaxed in your own ServiceMonitor. Because a scrape gap would also silence the alerts, the
`HerziControllerDown` alert fires on missing series.

Never embed TLS keys or bearer tokens in the ConfigMap. The mounted certificate files are world-readable
inside the container (0444), which lets UID 65532 read Secrets owned by root; no writable root is needed.
The bearer token and the TLS files are read at process start, so restart herzi after rotating them.
Verification is never switched off.

Metrics are HTTPS on 8443 with token authentication and authorization. Provision
a certificate valid for `herzi-metrics.herzi-system.svc`; no cert-manager dependency
is required. Secure monitoring is opt-in because it requires Prometheus Operator
CRDs. Configure the Prometheus instance's ServiceMonitor/PrometheusRule selectors
to discover `app.kubernetes.io/name: herzi` in `herzi-system`, allow its namespace
through the metrics ingress policy (`metrics: enabled`), and bind its ServiceAccount
to the scaffold `metrics-reader` ClusterRole. Provision/rotate the corresponding
token in `herzi-prometheus-token` for the monitor; prefer a platform-managed
short-lived credential refreshed before expiry over a permanent token. Prometheus
Operator must have permission to read the referenced credential and CA Secrets.
Do not use `insecureSkipVerify` or share the server's private key with Prometheus.

```bash
kubectl apply -k config/prometheus
```

The optional `config/network-policy` tree denies all controller egress except
API server TCP 443/6443, Temporal TCP 7233, workflows TCP 443 and DNS UDP/TCP 53.
Replace its TEST-NET IPs with actual endpoint ranges, adapt ports and DNS selectors,
and verify your CNI's service DNAT/node-local DNS behavior before applying it.
There are no allow-all egress exceptions. Its ingress rule permits HTTPS scrapes
only from namespaces labelled `metrics: enabled`; kubelet probe handling is CNI
specific and must be tested. DNS resolving a new endpoint IP does not update
NetworkPolicy ipBlocks automatically.

## Dry-run rollout

1. Close the release gates above. Verify the real-hub checklist below,
   external CRDs, TLS trust, namespace/queues and the route's slot limit.
2. Set `dryRunGrow: true` explicitly on **every managed pool first** (the shipped
   three examples do this); also set `defaults.dryRunGrow: true` for new pools.
   Confirm there are no per-pool false overrides. Keep new pools paused until
   inventory attribution is confirmed. Even zero demand seeds minFree machines.
3. Build/test and deploy a fixed image tag/digest. Generate/review RBAC, render
   `kubectl kustomize config/default`, and provision Secrets before the Pod starts.
   `make deploy IMG=<registry>/herzi:<version>` selects the image in the manager
   kustomization and applies the bundle to the current kubeconfig context.
4. Compare `WOULD INSTALL` logs to NodePool replicas, assigned/claimable/settling
   gauges and open slot histories. Simulate an empty-capacity failure, missing
   pollers, pause and invalid config; confirm blocks and last-good config behavior.
5. Enable real growth by setting `dryRunGrow: false` for one pool. Watch orders,
   ambiguity blocks, Agents and need until stable, then enable remaining pools.
   To stop new orders restore dry run or pause; neither cancels existing workflows.

## Upgrade and rollback

Record the current image digest and export the ConfigMap to a protected backup;
back up Secrets in your secret manager. Compare config fields/metrics/RBAC and
Temporal workflow-id compatibility across versions. First enable dry run, inspect
pending installs, then deploy the new immutable image/config. The single replica
uses RollingUpdate with maxSurge 0 and maxUnavailable 1, intentionally allowing
an availability gap. Kubernetes can retain a terminating Pod while creating its
replacement; the lease and immediate exit behavior remain necessary to prevent
overlapping active reconcilers. There is no PDB: minAvailable 1 would block drains
of the only replica, while maxUnavailable 1 adds no protection.

```bash
kubectl -n herzi-system rollout status deployment/herzi --timeout=120s
kubectl -n herzi-system rollout history deployment/herzi
# Restore the separately backed-up ConfigMap if its format changed, then:
kubectl -n herzi-system rollout undo deployment/herzi
kubectl -n herzi-system rollout status deployment/herzi --timeout=120s
```

Rollout undo restores only the Pod template, not ConfigMaps/Secrets/RBAC. Keep the
image in your site kustomization consistent with rollback so the next apply does
not undo it. Inspect logs and metrics before resuming growth. Undeploy deletes
the packaged Namespace as well as controller objects; it leaves external CRDs
untouched, but deletes namespaced config/Secrets/monitoring in that namespace.
Back those up and assess other namespace users before `make undeploy`.

## Lease loss and restart

Behavior: lease loss exits immediately with no graceful draining
(`GracefulShutdownTimeout = 0`); enable `LeaderElectionReleaseOnCancel` for clean
rollouts and exit as soon as the manager stops. The Pod's 30-second termination
grace is an upper bound for Kubernetes, not permission to keep ordering after
lease loss. The new leader reads the persisted block annotation and describes
all maxSlots fixed workflow IDs. No in-memory decision state is required.

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
TLS identity, Secret rotation, restricted Pod admission (including OpenShift UID
policy for fixed UID 65532), RBAC, probes, lease loss, restart ambiguity blocks,
network policy and real worker failures. None of these cluster checks were run
in the packaging workspace.
