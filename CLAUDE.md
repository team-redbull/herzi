# CLAUDE.md

herzi is a Kubernetes controller (Go, controller-runtime, Kubebuilder scaffold, no CRD of its own). On one OpenShift
MCE hub it keeps each InfraEnv's pool of spare machines matched to HyperShift NodePool demand by calling the workflows
`install-server` API. Read `docs/design/herzi-design.md` first: it is the source of truth for behaviour.
`AGENTS.md` holds the generic Kubebuilder rules.

## Hard rules

- **The controller only ADDS servers.** There is no removal, ceiling, shrink, cooldown, delete call or delete RBAC verb, and
  there must never be one. `lowPct` and `minBand` were removed and are rejected by the config loader.
- **Fail closed.** Unknown, errored or ambiguous observations block the pool; they are never counted as zero. An unknown
  failure never permits a full order.
- **Never push, open a PR or change a remote** unless the user asks. Commit locally only when asked.
- **Commit and PR text carries only the real description**: no `Co-Authored-By` and no "Generated with" lines.
- **Deploy with the Helm chart only** (`charts/herzi`). No kustomize, no `config/` tree.
- **Read Agent, InfraEnv and NodePool as unstructured** through the views in `internal/kube`. Do not import the typed
  assisted-service or HyperShift API modules (100+ modules and an ORM for a dozen fields).
- **Comments: necessary ones only.** Keep `+kubebuilder` markers, `//nolint` with a reason, one package line, one-line
  contracts, and a short "why" at a safety-critical line. Rationale and decisions belong in `docs/`; moved-out decisions live
  in `docs/design/notes/<package>.md`. No design-section, date, PR or finding-id references in code.
- **Generated files are not edited by hand**: `charts/herzi/files/rbac-rules.yaml` comes from the `+kubebuilder:rbac` markers
  in `internal/controller` via `make manifests`.

## Setup and commands

```bash
export PATH=/home/linuxbrew/.linuxbrew/bin:$HOME/go/bin:$PATH     # go, helm, promtool, kubebuilder
make setup-envtest                                                 # once; then:
export KUBEBUILDER_ASSETS=$PWD/bin/k8s/1.37.0-linux-amd64
```

| Command | What it does |
|---|---|
| `make test` / `make test-integration` | unit tests with the race detector / the envtest suite (real kube-apiserver) |
| `make lint` `make vuln` | golangci-lint, govulncheck (both must report nothing) |
| `make manifests` `make manifests-drift` | regenerate / verify the chart's RBAC rules |
| `make helm-lint` `make helm-test` `make helm-package` | strict lint, render assertions, package into `bin/` |
| `make test-alerts` | promtool tests of `charts/herzi/files/alerts.rules.yaml` (cases in `hack/chart/alerts.test.yaml`) |
| `make ci` | lint, vet, test, vuln, chart and alert checks, drift |
| `make deploy VALUES= IMG_REPO= IMG_TAG=` / `make undeploy` | `helm upgrade --install` / `helm uninstall` |

### The gate (run all of it after any change; agent reports are not proof)

```bash
gofmt -l cmd internal; go build ./... && go vet ./...
KUBEBUILDER_ASSETS=$PWD/bin/k8s/1.37.0-linux-amd64 go test -race -count=1 -cover ./...
./bin/golangci-lint run ./cmd/... ./internal/...
make vuln helm-lint helm-test test-alerts manifests-drift
```

Unit tests with the fake client passing does not mean envtest passes: run the envtest specs yourself.

## Layout

- `internal/pipeline`: PURE stages `Attribute -> Validate -> Decide -> Plan` plus durable-state merge; no I/O, no clock, no globals.
- `internal/controller`: the reconciler, the three watches (all map to the InfraEnv key), the pre-POST block marker, metrics wiring.
- `internal/temporalx`: slot sweep via `DescribeWorkflowExecution` on fixed ids, failure types from the close event, queue pollers.
- `internal/installer`: the single POST to the install route; unknown outcomes wrap `ErrAmbiguous`, 4xx wrap `ErrRejected`; never retries.
- `internal/config`: strict loader (durations are strings), per-pool merge and validation, last-good store, file watcher.
- `internal/ids`: workflow ids, pinned to the workflows repo (`shared/workflow_ids.py`): `install-server-<mce>_<infraEnv>_<slot>`.
- `internal/metrics`, `internal/kube`, `cmd/main.go`: Prometheus metrics, unstructured views, manager wiring.
- `charts/herzi`: the Helm chart (exactly 19 packaged files; a test asserts the list). Fixtures are in `hack/chart/`.
- `docs/design` (behaviour), `docs/ops` (install, operate, RBAC, runbook), `docs/review` (historical records, do not rewrite).

## Behaviour that must not change without a design decision

- target = demand + max(`minFree`, (`highPct` x demand + 50) / 100), integer half-up; defaults `highPct` 10 (cap 1000), `minFree` 2
  (cap 100000). Supply = assigned + claimable + settling + open runs. `need <= 0` holds before any probe logic.
- Order only against a sweep observed after the previous attempt; the sweeper cache is invalidated after every POST.
- The block marker (`herzi.redbull.io/blocked-until`) is written BEFORE the POST with its own bounded context and cleared after a
  definite outcome; durable pool state lives in `herzi.redbull.io/state`; `herzi.redbull.io/paused` pauses a pool.
- Empty inventory: probe mode orders exactly one machine per `probeInterval` and never stops asking. Recovery proof needs a run
  that started after the last capacity failure and completed an `acquire_servers` activity.
- Pool-scoped problems block only their pool; hub-scoped findings are warnings. `maxSlots` must be 100. Timing relations are
  validated (`failureMemory >= 2*probeInterval`, `interval <= probeInterval`, `settlingMax >= probeInterval`, `drawGrace < probeInterval`).
- `dryRunGrow` (config, per pool or in `defaults`) logs `Would install` and orders nothing. The code and chart DEFAULT is `true`, so every pool
  starts in dry run. Going live is an explicit `dryRunGrow: false` per pool, one pool at a time.

## Working with Codex agents

Plan and review with Codex, implement with Codex on disjoint files, then verify yourself.

- Start implementers with `--fresh --write`; resuming an earlier read-only thread stays read-only. Pass
  `--model gpt-6.1-sol --effort medium` when the owner asked for it.
- The Codex sandbox cannot bind sockets (no envtest) and may report the Go cache read-only. Tell it:
  `export GOCACHE=/tmp/herzi-go-build GOTMPDIR=/tmp GOFLAGS=-buildvcs=false`, and to stop only if it cannot write under the repo.
- After a partial run, read `git diff HEAD` before relaunching. A forwarder that "failed" may already have started the job
  (`node ~/.claude/plugins/cache/openai-codex/codex/1.0.6/scripts/codex-companion.mjs status`).
- For comment-only refactors prove the code is unchanged: compare each Go file's syntax tree with comments stripped before and after.

## Pitfalls we hit

- A test fake must model reality: a sweeper that never reports the runs an order started makes the controller correctly order
  again. The envtest `world` fake registers open runs on `Install`.
- Test configs must satisfy the validation rules (a requeue interval above `probeInterval` makes the pool `config_invalid`).
- Tool state (`.claude-flow/`, `agentdb.rvf*`, `ruvector.db`) can appear anywhere, including inside the chart; it is git-ignored and
  `.helmignore` keeps it out of a packaged chart. Never commit it.
- Never run `sort -u` on `.gitignore` or other structured files.
