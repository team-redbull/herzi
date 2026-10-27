> **Current configuration supersedes this record:** there are now nine pool fields.
> See [operating.md](operating.md#config-model) for current configuration and migration,
> and the [runbook](runbook.md) / [alert rules](../../charts/herzi/files/alerts.rules.yaml)
> for startup-configured stuck-run alerting. Past validation below is preserved as recorded.

> **Historical record, including former kustomize packaging, superseded by `charts/herzi`.** This is the report of the packaging work package as it was delivered, before the
> controller was wired. Statements about what "WP6 must still do" are out of date: the manager wiring, the
> generated RBAC and the envtest suite now exist. Where this file disagrees with `README.md`,
> `operating.md` or `rbac.md`, those win.

# WP5 packaging report

## Files delivered

- `config/default/{kustomization.yaml,metrics_service.yaml}`: default installation.
- `config/manager/{kustomization.yaml,manager.yaml,namespace.yaml}`: one hardened
  Deployment named herzi, restricted namespace, config/Secret volumes and probes.
- `config/samples/{kustomization.yaml,herzi-config.yaml}`: stable generated
  herzi-config ConfigMap with all 13 hub fields, all 11 pool fields and three pools.
- `config/rbac/kustomization.yaml`: keeps leader election/metrics RBAC, excludes
  the unmodified generated Pod-reader placeholder and its binding pending WP6.
- `config/prometheus/{kustomization.yaml,monitor.yaml,herzi-alerts.yaml}`: verified
  HTTPS scrape configuration and nine catalogue-only alerts.
- `config/network-policy/{kustomization.yaml,allow-metrics-traffic.yaml,egress.yaml}`:
  optional metrics ingress and endpoint-specific egress example.
- `Dockerfile`, `.dockerignore`, `Makefile`, `.golangci.yml` and
  `.github/workflows/ci.yml`: container, tooling and CI configuration.
- `test/crds/{infraenvs.yaml,agents.yaml,nodepools.yaml}`: permissive envtest CRDs.
- `test/e2e/README.md`, `README.md`, and
  `docs/ops/{operating.md,runbook.md,rbac.md,packaging.md}`: operations and status.

Removed files: `config/default/manager_metrics_patch.yaml`,
`config/default/cert_metrics_manager_patch.yaml`,
`config/prometheus/monitor_tls_patch.yaml`, `.custom-gcl.yml`,
`.github/workflows/{lint.yml,test.yml,test-e2e.yml}` and
`test/e2e/{e2e_test.go,e2e_suite_test.go}`. Scaffold markers from the deleted
files were retained in the replacement default kustomization/e2e README.
There were no actual config/crd, config/webhook or config/certmanager directories
in the inspected scaffold; their inactive references were removed.

No files under cmd/, internal/, go.mod/go.sum, test/utils/ or PROJECT were edited.
No git commands or Go module modification commands were used.

## Executed validation

- `kubectl kustomize config/default` rendered successfully.
- `make lint-manifests` rendered default, manager, rbac, samples, prometheus and
  network-policy. Kubeconform was unavailable; schema validation was not executed.
- Python/PyYAML parsed all 27 configuration/workflow/fixture/lint YAML files.
- `make -n` parsed every declared target, including all new targets and tool targets.
  These were dry runs; no tool downloads, deployment or Go generation occurred.
- Python assertions checked rendered Deployment name/namespace, one replica,
  RollingUpdate parameters, UID, read-only root, dropped capabilities, termination
  grace, ConfigMap presence, no CRD/PDB, exclusion of the scaffold Pod-reader role,
  complete shared config field coverage, duration strings and three dry-run pools.
- Python assertions checked all nine alert expressions use catalogue names, all
  required annotations/runbook anchors exist, action references have 40-character
  SHAs, and all three fixtures have permissive spec/status plus status subresources.

Not executed: Docker build/push, Kind/e2e, deployment, real cluster/Temporal or
HTTP behavior, hosted GitHub Actions CI, envtest integration, Go build/unit tests,
lint/vet/govulncheck, coverage enforcement, promtool validation or kubeconform.
Docker and a cluster are absent. WP5 owns no Go packages; the plan forbids running
other work packages while they may be mid-edit. The deliverable config cannot run
with the inspected scaffold entrypoint until WP6 supplies the documented wiring.

## Action pinning

No unpinned actions. Checkout v6.0.2 and setup-go v6.3.0 retain the full commit
SHAs already supplied by the scaffold; no remote SHA lookup was necessary.
Go caching is setup-go's built-in cache. There is no image push in CI.

## Assumptions and integration dependencies

- The HERZI_* secret environment names are proposed interfaces for WP6, not
  implemented flags/config fields. See operating.md for exact names and mounts.
- TLS Secrets, endpoints, task queues, Prometheus selectors, credential rotation
  and least-privilege bindings must be provisioned for the actual hub. The sample
  Temporal namespace/segment queue and TEST-NET network destinations are examples.
- Resource requests/limits are starting values; measure inventory/cache size and
  latency on the real hub before accepting them as production sizing.
- The builder is pinned to golang:1.26.0, matching go.mod; image tags are version
  pins, not immutable digests. The requested distroless nonroot final tag is kept.
- main.version/main.commit and all controller/config/secret/metrics wiring,
  release-on-cancel and immediate lease-loss shutdown belong to WP6. The read-only
  envtest suite also needs test/crds and ErrorIfCRDPathMissing enabled.
- Generated controller RBAC is intentionally not invented by WP5. WP6 must
  generate/review it and enable role.yaml plus role_binding.yaml in the RBAC tree.
- Fixed alert durations are operational policy (5-minute transient filter for most
  failures, 10-minute reconcile-age threshold, 3-hour assigned surplus). Hardware
  shortage uses 30 minutes, blocked pools 15 minutes and stuck runs 2 hours as
  requested. Adapt alerts when changing interval/stuckAfter.
- RollingUpdate maxSurge 0/maxUnavailable 1 prevents intentional surge but cannot
  guarantee absence of a terminating Pod while its replacement starts. The lease
  and immediate process exit still provide the active-controller guarantee; lack
  of fencing remains the design's accepted residual risk.

## Deliberate departures from the scaffold

1. No owned CRD, webhook or cert-manager install resources/references, and no
   install/uninstall targets: herzi observes external resources. Manifests generates
   RBAC only; generate remains available for future internal DeepCopy objects.
2. Default deployment has no name prefix, so Deployment and ConfigMap names remain
   exactly herzi and herzi-config. Namespace transformation preserves RBAC subjects.
   No PodDisruptionBudget is added because one replica intentionally tolerates a
   maintenance gap. Security, resources, graceful termination, config and TLS
   mounts replace the empty scaffold settings. UID 65532 needs hub admission review.
3. The redundant metrics argument patch is folded into Deployment args; verified
   metrics TLS uses provisioned Secrets, removing self-signed skip-verification and
   cert-manager patches. ServiceMonitor/PrometheusRule remain opt-in so a default
   install does not require Prometheus Operator CRDs.
4. NetworkPolicy remains opt-in because API/Temporal/workflows IPs and DNS/CNI
   behavior are site-specific. It adds default-deny egress with only required
   destinations, retaining scoped metrics ingress.
5. The generated scaffold Pod role is excluded rather than hand-rewritten. The
   exact controller permission contract is documented for WP6 marker review.
   Original leader-election and metrics-auth files remain untouched as requested;
   the scaffold election Role is broader than the minimum contract.
6. Docker now uses an exact Go version, static trimpath/stripped build, version
   and commit args/OCI labels, explicit source copies and a shell-free distroless
   nonroot runtime. Dockerignore limits context to module and production sources.
7. Unit tests no longer download envtest or mutate manifests/source as side effects.
   They use race detection/atomic coverage; integration has its own pinned envtest
   target. Build/run no longer regenerate resources or format other work packages.
   Added vuln, ci and manifest lint targets; retained image build/push, installer
   rendering, multi-platform build, deploy/undeploy and lint helpers. Buildx uses
   the Dockerfile directly instead of writing a temporary Dockerfile.cross.
8. The custom linter/plugin config is removed: the scaffold fetched logtools at
   latest and could silently swallow its build failure. Standard pinned
   golangci-lint retains the built-in checks; Kubernetes logging remains a review
   requirement. No custom plugin is claimed to run.
9. Three overlapping scaffold workflows are replaced by ci.yml with separate unit,
   integration, manifests and image jobs. No go mod tidy, latest Kind download or
   e2e job remains. An explicit integration gate exposes the missing fixture path;
   CI must not report a completed integration suite against absent CRDs.
10. The scaffold e2e suite installed unrelated cert-manager/owned CRDs, so it is
    replaced by the requested future-coverage README. External permissive fixtures
    live exclusively under test/crds. test/utils remains untouched and unused.
11. README and operations docs replace scaffold instructions with the design's
    decision table, complete config/metric reference, nine alert procedures,
    dry-run rollout, upgrade/rollback, RBAC contract and real-hub checklist.
