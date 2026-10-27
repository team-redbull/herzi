# herzi Helm chart

The chart, its commented `values.yaml`, and `values.schema.json` define the deployment contract. herzi adds machine capacity permanently and owns no CRDs. Kubernetes 1.27 or newer is required; external InfraEnv, Agent and NodePool CRDs must already exist. No hooks, Namespaces, Secrets or CRDs are installed by this chart.

Install into a deliberately chosen cluster with site-specific values and an immutable image:

```sh
helm install herzi charts/herzi --namespace herzi-system --create-namespace \
  -f site-values.yaml --set-string image.repository=registry.example.com/herzi \
  --set-string image.tag=0.1.0
# Equivalent repository target:
make deploy VALUES=site-values.yaml IMG_REPO=registry.example.com/herzi IMG_TAG=0.1.0
```

`site-values.yaml` must set `config.hubName`, `config.installURL` (absolute HTTP(S) install-server endpoint, using its Service address on the same cluster or Route otherwise), and `config.temporalAddress` (host:port). `image.repository` is also required. Missing values fail schema validation before installation. `image.tag` defaults to Chart.appVersion. Do not deploy the illustrative endpoints in `ci/example-values.yaml` unchanged. All three example pools have `dryRunGrow: true`; keep every pool in dry run until the hub, worker queues, Temporal history and install route have been verified. The default chart manages no pools.

The release namespace is supplied by Helm, rather than created by a chart template. Apply any site namespace security labels separately. Resource names use the release name and chart name (release `herzi` produces `herzi`, `herzi-config`, `herzi-manager`, etc.). `nameOverride` changes the chart name portion; `fullnameOverride` selects the whole base name. Cluster-scoped RBAC names must be unique across releases, including releases in different namespaces.

## Values reference

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

## Controller configuration

`config` contains only controller settings. Durations use Go duration strings such as `120s`, `20m`, `1h30m`; days and weeks are unsupported. Unknown controller, default or pool keys fail validation. `lowPct` and `minBand` were removed and must be deleted, even when zero or null. Use `highPct` for the single target buffer percentage. Explicit zero and false pool overrides are preserved.

| `config` key | Default | Meaning |
| --- | --- | --- |
| `interval` | `120s` | Normal requeue period. |
| `reconcileTimeout` | `60s` | Overall reconcile deadline. |
| `hubName` | required | MCE identity; controller canonicalizes the `ocp4-` prefix. |
| `agentNamespace` | `inventory` | InfraEnv and Agent namespace. |
| `installURL` | required | Full HTTP(S) workflows install-server endpoint (Service address or Route). |
| `maxSlots` | `100` | Fixed API slot range; any other value is rejected. |
| `ambiguousWait` | `5m` | Persisted wait after an unclear POST. |
| `stuckAfter` | `2h` | Startup open-run alert threshold exported as a metric. |
| `temporalAddress` | required | Temporal host:port. |
| `temporalNamespace` | `default` | Temporal namespace. |
| `activityQueue` | empty | Derives `server-lifecycle-activity-<canonical hubName>`. |
| `workflowQueue` | `install-server-workflow` | Workflow worker queue. |
| `segmentQueue` | `segment-lifecycle-activity` | Segment worker queue. |
| `defaults` | below | Defaults inherited by managed pools. |
| `pools` | `{}` | InfraEnv name to pool overrides; presence opts in. |

Every pool key can be set in `config.defaults` and overridden in `config.pools.<name>`:

| Pool key | Default | Meaning |
| --- | --- | --- |
| `highPct` | 10 | Spare buffer percentage (0–1000), integer half-up rounding. |
| `minFree` | 2 | Minimum spare buffer (0–100000). |
| `maxOpenRunsPerPool` | 15 | Concurrent pool run cap (1–100). |
| `probeInterval` | `20m` | Hardware shortage probe interval. |
| `drawGrace` | `2m` | Acquisition evidence grace period; strictly less than probeInterval. |
| `failureMemory` | `6h` | Failure retention; at least twice probeInterval. |
| `settlingMax` | `60m` | Unbound Agent settling allowance; at least probeInterval. |
| `dryRunGrow` | false | Record planned additions without issuing orders. |
| `paused` | false | Suspend pool growth. |

`interval` must be no greater than any managed pool's probeInterval. The controller validates duration positivity, range overflow and timing relationships; JSON Schema validates syntax, types and numeric bounds. Defaults or hub errors reject the document; invalid merged pool relationships block only that pool.

The ConfigMap is mounted read-only as a directory at `/etc/herzi` without subPath, so Kubernetes can update the file and the watcher can reload pool and live timing changes. Only the JSON hash of startup-bound config enters `checksum/startup-config`: hubName, agentNamespace, installURL, maxSlots, **stuckAfter**, temporalAddress, temporalNamespace and the three queues. `stuckAfter` is included because `cmd/main.go` captures it at startup. Live timing and pool edits do not change the pod template. Image, Secret references, scheduling and other workload values naturally change the pod template when edited. Secret content rotation alone does not trigger a rollout; restart explicitly when clients require it.

The Deployment uses RollingUpdate with maxSurge 0/maxUnavailable 1 and accepts downtime during a single-replica rollout. Increasing replicas allows multiple standby pods; leader election still selects one active controller. Pod security uses runAsNonRoot and RuntimeDefault seccomp; OpenShift restricted-v2 assigns the namespace UID, so no runAsUser/runAsGroup is fixed. The manager has a read-only root filesystem, no privilege escalation and drops ALL capabilities. Shutdown grace is 30 seconds. No PDB is installed.

## External Secrets and monitoring

The chart never creates Secrets or accepts secret values. All referenced Secrets must exist in the release namespace. Secret files are mounted read-only with mode 0444 so an OpenShift-assigned UID can read them; only the manager receives these mounts.

| Reference | Default keys | Behavior |
| --- | --- | --- |
| `temporal.tls.existingSecret` | `caKey: ca.crt` | Mount `/etc/herzi-temporal`; sets HERZI_TEMPORAL_TLS_CA_FILE. Empty uses plaintext gRPC. |
| `temporal.mtls.enabled` | `certKey: tls.crt`, `keyKey: tls.key` | Requires the TLS Secret; adds HERZI_TEMPORAL_TLS_CERT_FILE and HERZI_TEMPORAL_TLS_KEY_FILE. |
| `temporal.apiKey.existingSecret` | `key: api-key` | HERZI_TEMPORAL_API_KEY via secretKeyRef. |
| `installApi.bearerToken.existingSecret` | `key: token` | HERZI_API_BEARER_TOKEN via secretKeyRef. |
| `metrics.tls.existingSecret` | `tls.crt`, `tls.key` | Mount `/etc/herzi-metrics`; adds --metrics-cert-path. Empty uses the controller's self-signed certificate. |

`ci/tls-values.yaml` demonstrates CA verification and a metrics certificate; `ci/mtls-values.yaml` adds a client certificate. `ci/full-values.yaml` additionally enables monitoring, network policy, API credentials and a metrics reader subject. These are complete illustrative values files, suitable for strict linting.

Metrics are authenticated HTTPS on port 8443. `metrics.enabled` gates the Service; set the ServiceMonitor separately. Install Prometheus Operator CRDs before enabling `serviceMonitor.enabled` or `prometheusRule.enabled`. Rendering is deterministic and never depends on API discovery. Supply `serviceMonitor.tlsConfig.serverName` matching the certificate and `tlsConfig.ca.secret` with `name`/`key`; verification is enabled by default. Set `bearerTokenSecret` to a rotating Prometheus token reference and add its ServiceAccount to `rbac.metricsReader.subjects`. ServiceMonitor labels must match your Prometheus selection. On OpenShift, enable user-workload monitoring for this namespace before using the ServiceMonitor.

Manager RBAC is generated from controller markers into `files/rbac-rules.yaml`; never hand-edit it. The manager reads Agents, InfraEnvs and NodePools and patches InfraEnvs; it cannot create, update or delete those objects. A namespaced role manages leader-election leases and emits events, without ConfigMap access. Metrics auth can create TokenReviews and SubjectAccessReviews. The metrics-reader ClusterRole allows only GET `/metrics`, with no binding unless subjects are supplied. With `rbac.create: false`, provision all required roles and bindings externally.

`networkPolicy.enabled` defaults false. Before enabling, populate each of `apiServer`, `temporal`, and `installApi` with destination `cidrs` and `ports` lists. Empty CIDR lists allow no traffic for that destination. DNS selects `kubernetes` (kube-system CoreDNS TCP/UDP 53) or `openshift` (openshift-dns TCP/UDP 5353). Metrics ingress uses `metricsNamespaceSelector`, defaulting to namespaces labeled `metrics: enabled`; all other ingress is denied. Account for CNI pre/post-DNAT and node-local DNS before enabling.

## Development and verification

```sh
make manifests          # regenerate the shipped RBAC rules
make manifests-drift    # regeneration and committed-file comparison
make helm-lint helm-test test-alerts
make helm-package       # bin/herzi-0.1.0.tgz
./hack/dev-config.sh > /tmp/herzi-config.yaml
make run                # extracts example config; uses current kubeconfig
helm uninstall herzi -n herzi-system
```

`hack/test-chart.sh` lints all four examples and asserts RBAC, security, image defaults, config contents, startup checksum behavior, TLS/mTLS, optional resources, literal Prometheus templates, labels, namespaces and rejection of invalid values. `hack/test-alerts.sh` uses promtool directly on the shipped native `files/alerts.rules.yaml` and every case in `tests/alerts.test.yaml`, including two controller targets. Go tests load the example config through the strict production loader and compare every chart config default with the code.

Uninstall removes release-managed resources, including its ConfigMap and RBAC. The namespace and externally provisioned Secrets remain. The chart leaves no release-managed resources or owned CRDs behind. Back up site values; `helm rollback` restores the old release values and rendered ConfigMap together with the workload, so an old image receives its matching config; capacity previously added is never reclaimed.
