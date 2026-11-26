> **Change specification / historical planning record.** Written on 2026-11-19 against herzi `0ef4d2a` and workflows
> `5856f96`; the owner approved the decisions in section 1. Implemented on branch `feat/retire-used-servers`; the
> [design](../design/herzi-design.md) and [operating guide](../ops/operating.md) now describe the contract. The
> implementation differs from this plan in small ways: `herzi_retiring_agents` also has `paused` and `dry_run`
> states, the slow alert is `HerziRetirementSlow` on `herzi_oldest_retiring_seconds`, and the retirer has no
> config-reload watch (retiring Agents requeue every `interval`).

# Plan: retire used servers

**Goal:** a server that was ever installed on a tenant cluster never returns to the spare pool. Once it is released,
it is taken off the MCE with the workflows `remove-server-from-mce` route.

**What herzi does:**

1. It labels every Agent bound to a tenant cluster with `redbull.io/ever-installed: "true"`.
2. For every Agent that carries the label and is no longer bound, it calls
   `POST /workflows/server-lifecycle/remove-server-from-mce`.
3. It keeps the spare-stock logic (target, supply, Full/Probe growth), but labelled Agents never count as claimable or
   settling.

**What the NodePools do:** every NodePool will carry this selector expression, so CAPI never re-claims a labelled
Agent:

```yaml
spec:
  platform:
    agent:
      agentLabelSelector:
        matchLabels:
          infraenvs.agent-install.openshift.io: <infraEnv>
        matchExpressions:
          - key: redbull.io/ever-installed
            operator: DoesNotExist
```

**For the implementer:** read sections 1–4 before writing code. Section 9 gives the order and the checks to run.
File references are relative to the repo root at `0ef4d2a`.

---

## 1. Owner decisions

| # | Decision |
|---|---|
| D1 | The add-only hard rule is lifted for this one purpose. herzi still never removes capacity because of a surplus: target, supply and Hold stay add-only. What is new is **retiring used hardware**, an action driven by the label and not by the numbers. |
| D2 | Scope is **off this MCE**. The remove workflow does not mark the server as never to be reused, so server-scan may hand it to a later add-server-to-mce as a fresh, unlabelled Agent. That is accepted. |
| D3 | Agents already bound when this is deployed are labelled on the first pass, the same as new binds. There is no opt-out. |
| D4 | Labelling and removal cover **every Agent in `agentNamespace`**: managed, ignored and excluded InfraEnvs, and Agents with no pool label. `ignoreLabel` and `pools.<name>.exclude` now mean only "not counted for supply and growth". |
| D5 | Agents in other namespaces are out of scope. The Agent cache and RBAC stay namespace-scoped. |
| D6 | The label is written **on bind** (`spec.clusterDeploymentName` set), not when the install completes. A bind that aborts before installing still retires the server. This is conservative and matches the goal. |
| D7 | Removal starts in dry run (`dryRunRemove: true` by default) and goes live one pool at a time, like `dryRunGrow`. |

## 2. Step 0: rewrite the contract first

These texts state the old rule. Change them in the first commit of the implementation, so the repo never contradicts
itself.

**[CLAUDE.md](../../CLAUDE.md), "Hard rules":** replace the add-only bullet with:

> **herzi never removes capacity because of a surplus.** Target, supply and Hold stay add-only: no ceiling, shrink or
> cooldown. It **retires used servers**: an Agent that was ever bound to a tenant cluster is labelled
> `redbull.io/ever-installed` and, once released, removed from the MCE through the workflows `remove-server-from-mce`
> route. herzi holds no Kubernetes delete verb; the workflow worker deletes.

**[CLAUDE.md](../../CLAUDE.md), "Behaviour that must not change":** change the ignored-InfraEnv bullet to say that an
ignored or excluded InfraEnv is left out of supply, growth and pool metrics, but its Agents are still labelled and
retired. Add a bullet: labelled unbound Agents are `retiring` and never count as supply.

**Other places to update:**

| File | What to change |
|---|---|
| [README.md](../../README.md) | Line 25, and the other "capacity is never reclaimed" sentences |
| [herzi-design.md](../design/herzi-design.md) | Line 3 (scope), the supply-term table, worked case 4 (section 3.4 below), case 14, Part 2 (a new Retirer section), the invariants table, decision log G "Capacity policy" |
| `docs/PLAN.md` | Line 10 |
| [docs/ops/rbac.md](../ops/rbac.md) | The agents row, and the "No Agent ... permission exists" sentence |
| [docs/ops/operating.md](../ops/operating.md) | The lines that state "capacity is never reclaimed" |

## 3. Verified facts the design rests on

### 3.1 The remove route (workflows `5856f96`)

Links point to [team-redbull/workflows at 5856f96](https://github.com/team-redbull/workflows/tree/5856f9686d65025b90a4bcdcf5c240fd0a6eee97).

**Request and response:**
- **Route:** `POST /workflows/server-lifecycle/remove-server-from-mce`. The path is defined at
  `workflow_domains/server_lifecycle/router.py:80` and handled at `router.py:602-630`.
- **Body:** `{"server_name": str, "mce_cluster": str, "namespace": str}` (`RemoveServerFromMceRequest`,
  `router.py:579`).
  - One server per call.
  - `namespace` defaults to `inventory`.
  - `ocp4-` is added to `mce_cluster` if missing.
  - Unknown fields are rejected with 422.
  - The field is singular `server_name`; add-server takes `server_names`.
- **No auth.** Send the same bearer token herzi already sends; it is ignored today.
- **Responses:**

  | Status | Meaning |
  |---|---|
  | 202 | `{workflow_id, run_id}` |
  | 409 | A run is already in flight for this server (`router.py:625-629`) |
  | 422 | Bad body |
  | 500 | Any other Temporal start error |

**The workflow id** is `remove-server-from-mce-<lower(server)>` (`shared/workflow_ids.py:137-146`):
- It contains no MCE.
- The same id is also started by add-server-to-mce rollback children, and by uninstall-server-from-hc runs recorded
  before 2026-11-19. A Running run under this id is therefore not necessarily herzi's.
- Temporal's default reuse policy applies: a POST while a run is open gets 409; a POST after it closes starts a new
  run.

**The route is idempotent:**
- A re-run after a completed run finds nothing and completes with `found: false`.
- A re-run after a failure continues from whatever is left.

**The workflow refuses a bound Agent, but only by `spec.clusterDeploymentName`:**
- `remove_server_from_mce.py:289-298` raises `AgentBoundToClusterError` and changes nothing if any matched Agent has
  `spec.clusterDeploymentName` set.
- It does **not** check the `agentMachineRef` label, AgentMachines or Nodes, so herzi must check `agentMachineRef`
  itself.
- `set_host_online` and `delete_agents` re-check the binding just before acting.

**Steps,** in this order:
1. Inspect.
2. Power off (polls up to 10 min).
3. Delete the BMH and NMStateConfig.
4. Delete the Agent.
5. Clear the PXE entries (best effort).
6. Verify (up to 5 min).

A healthy run takes about 1–3 minutes. No execution timeout is set, so with no worker a run stays Running
indefinitely. The workflow never releases server-scan's install lock and never marks the server out of service.

**Queues:**

| Queue | Kind | herzi polls it today? |
|---|---|---|
| `remove-server-from-mce-workflow` (`shared/consts.py:41`) | workflow | no |
| `server-lifecycle-activity-<mce>` | activity | yes |
| `segment-lifecycle-activity` | activity | yes |

**Server identity: use the Agent label `agent-install.openshift.io/bmh`, which holds the lowercased server name.**
- The workflow matches Agents by this label or by MAC address.
- Never fall back to `spec.hostname`, `status.inventory.hostname` or `metadata.name`. A wrong name either completes
  silently with `found: false` or matches a different server.
- No bmh label means no removal: the Agent waits and an alert fires.

**Precedent to tell the workflows owner about:**
- From 2026-11-12 to 2026-11-17, uninstall-server-from-hc wrote `redbull.io/uninstalling: "true"` for this same
  `DoesNotExist` selector idea, then retired it ("the chart side was never built").
- Since 2026-11-19 it ends with `kept_in_inventory: true`.
- The workflows owner should know that herzi now retires released Agents.

### 3.2 The CAPI agent provider never re-checks the selector after binding

Source: [`openshift/cluster-api-provider-agent` `controllers/agentmachine_controller.go`](https://github.com/openshift/cluster-api-provider-agent/blob/master/controllers/agentmachine_controller.go)
(upstream master; verify against the version on the hub).

| Function | What it does |
|---|---|
| `findAgent` | Runs only while `AgentMachine.Status.AgentRef` is empty. Lists Agents with `agentLabelSelector` plus `agentMachineRef DoesNotExist`, then keeps those that are approved, connected and not bound. |
| `updateFoundAgent` | Sets the `agentMachineRef` label and `spec.clusterDeploymentName` in **one** Update. |
| Later reconciles | Fetch the Agent through `AgentRef`, or through the `agentMachineRef` label after an interrupted pick. Neither uses the selector. |
| `handleDeletionHook` | The only code that unbinds an Agent: clears `clusterDeploymentName` and the `agentMachineRef` label when the AgentMachine is deleted. |

**Consequence:** herzi labels only after it sees `clusterDeploymentName`. By then CAPI has picked the Agent, so the
label cannot disturb the install. Once released, the label keeps the Agent out of every NodePool that carries the
expression.

### 3.3 herzi today

| Where | Fact | Consequence |
|---|---|---|
| [kube/types.go:41-44](../../internal/kube/types.go#L41-L44) | `Selector` keeps `MatchLabels` and only `HasExpressions bool` (set in `parse.go` `ParseNodePool`) | Must keep the parsed expressions |
| [kube/types.go:57-68](../../internal/kube/types.go#L57-L68) | The Agent view has `Labels` and `Bound` (from `spec.clusterDeploymentName.name`) | Enough to decide labelling. The bmh label is in `Labels` |
| [pipeline/attribute.go:48-49](../../internal/pipeline/attribute.go#L48-L49) | Any `matchExpressions` makes the pool a Problem | **As written, the NodePool change blocks every pool.** Must allow the one expression |
| [pipeline/attribute.go:66-87](../../internal/pipeline/attribute.go#L66-L87) | Agent buckets: Bound → Assigned; Deleting → skip; claimable; settling; stuck | New `Retiring` bucket goes here |
| [pipeline/decide.go:78-87](../../internal/pipeline/decide.go#L78-L87), `reconciler.go` `observe`, `logging.go` `logStatus` | Supply = assigned + claimable + settling + open, summed in three places | Unchanged in shape. Retiring is simply not one of the terms |
| [controller/reconciler.go:135-152](../../internal/controller/reconciler.go#L135-L152) | Ignored and excluded InfraEnvs return early | The pool reconciler can't carry D4 |
| [controller/watches.go:36-41](../../internal/controller/watches.go#L36-L41) | An Agent with no pool label maps to no request | Same: needs a per-Agent controller |
| [controller/watches.go:23-29](../../internal/controller/watches.go#L23-L29) | Agent cache scoped to `agentNamespace`, no label selector, `PartialTransform` keeps `metadata.labels` | Reusable as is |
| [controller/reconciler.go:109-111](../../internal/controller/reconciler.go#L109-L111) | RBAC: agents `get;list;watch` | Add `patch`. `hack/test-chart.sh` forbids delete/create/update, not patch |
| [controller/reconciler.go:365-379](../../internal/controller/reconciler.go#L365-L379) | `patchAnnotations`: merge patch through `context.WithTimeout(context.WithoutCancel(ctx), 10s)` | Same pattern for the Agent label |
| `controller/reconciler.go` `cachedPollers` | Poller check cached 30 s (5 s after an error) and shared in flight | Extract so both controllers share it |
| `internal/installer/installer.go` | Tied to the install body and slot report; transport has `DisableKeepAlives`, no redirects, CA file, `redact`/`safeURL`/`transportError` | Extract the shared parts and add a Remover |
| `internal/temporalx/sweeper.go` `describe` | Describe on any id; `*serviceerror.NotFound` means free; per-key TTL cache with shared in-flight sweeps; `Invalidate` | Same pattern for remove ids |

### 3.4 Worked case 4 changes (scale down, then up)

| | Demand 20 → 4, then 12 | Result |
|---|---|---|
| Today | 16 released Agents count as settling (supply 22), so a scale-up to 12 orders nothing and reuses them | |
| After | The 16 are labelled, so they are `retiring` and not supply. They are removed from the MCE | Supply is 4 + 2 claimable = 6. A scale-up to 12 (target 14) orders 8 new machines |

This is intended. State it in the design doc, and replace the invariant "free machines are never dropped from supply
while rebooting" with "...unless they carry `redbull.io/ever-installed`".

## 4. Design

### 4.1 Two controllers in one manager

| | Pool reconciler (exists) | **Retirer** (new) |
|---|---|---|
| Key | InfraEnv `agentNamespace/<name>` | Agent `agentNamespace/<name>` |
| Watches | InfraEnv, NodePool, Agent, config reload | `For(Agent)` in `agentNamespace`. NodePool changes, InfraEnv changes (the pause annotation) and config reload enqueue every labelled, unbound Agent |
| Covers | managed InfraEnvs | every Agent in `agentNamespace`, including ignored, excluded and stray ones |
| Writes | InfraEnv annotations, install POST | Agent label, remove POST |
| Concurrency | 4 | 4 |

Both need leader election and share the manager's Agent cache. The Retirer's label patch changes the parsed Agent
view, so the pool reconciler requeues that pool and recounts its supply. That is desired.

### 4.2 Pool reconciler: supply only

**Attribute.** Add a bucket after `Bound` and `Deleting`, before the claimable case:

```go
_, retired := a.Labels[kube.LabelEverInstalled]   // key presence, the same test as DoesNotExist
switch {
case a.Bound:    f.Assigned++
case a.Deleting: continue
case retired:    f.Retiring++   // new: never supply, never stuck
case <claimable>: ...
```

**Selector rule** (replaces `attribute.go:48-49`). Allowed: exactly the InfraEnv matchLabel, plus either no
expressions or exactly `{key: redbull.io/ever-installed, operator: DoesNotExist}` with no values. Anything else stays
the existing Problem.

**Metric:** `herzi_pool_retiring{pool}`. Ignored and excluded pools stay invisible to the pool reconciler, as today.

Validate, Decide, Plan, the growth protocol, the probe rules and the durable state do not change.

### 4.3 Retirer: one decision per Agent

The decision is a pure function in `internal/pipeline/retire.go`. It does no I/O and reads no clock:

```go
type RetireInput struct {
    Agent        kube.Agent
    NodePools    []kube.NodePool // every NodePool on the hub (cache)
    NodePoolsErr bool            // list or parse failure
    Paused       bool            // the Agent's InfraEnv: config paused || herzi.redbull.io/paused == "true"
    DryRun       bool            // cfg.ForPool(<InfraEnv label>).DryRunRemove; Agents with no pool use defaults
    Pollers      PollerStatus    // now includes Remove
    Run          RemoveRun       // Describe of remove-server-from-mce-<server>; zero when NotFound
    RunErr       error
    OpenRemovals int             // Running remove runs among all retiring Agents
    MaxOpen      int
    RetryAfter   time.Duration
    Now          time.Time
}
// DecideRetire returns Label, Remove{Server}, Wait{Reason} or None.
func DecideRetire(in RetireInput) RetireAction
```

**Rules, in order** (fail closed: when unsure, Wait):

1. **Bound, not deleting, no label → `Label`.** This ignores pause, ignore, exclude, dry run and every other gate,
   because a missed label can never be repaired.
2. **Not labelled, bound or deleting → `None`.**
3. **`agentMachineRef` label present → `Wait("held by an AgentMachine")`.** The Agent is mid-unbind.
4. **Empty `agent-install.openshift.io/bmh` label → `Wait("no bmh label")`.** Never guess the server name.
5. **`NodePoolsErr` → `Wait("nodepools unknown")`.**
6. **Not guarded → `Wait("unguarded nodepool <ns/name>")`.**
   - For every NodePool with `platform.type == Agent`, including paused and deleting ones, build its selector without
     the ever-installed requirement (`metav1.LabelSelectorAsSelector`).
   - If that selector matches the Agent's labels, the NodePool must carry the `DoesNotExist` requirement.
   - A NodePool whose selector can't be built means `NodePoolsErr`.
7. **`Paused` → `Wait("pool paused")`.** Pause is the per-pool emergency brake for removal; labelling continues.
8. **No pollers on the remove workflow queue, the activity queue or the segment queue, or `Pollers.Err` →
   `Wait("no workers")`.**
9. **`RunErr` → `Wait("describe failed")`.**
10. **`Run` is Running → `Wait("removal running")`.** Ours or not.
11. **`Run` closed less than `RetryAfter` ago → `Wait("retry backoff")`.** A closed run whose Agent still exists means
    it failed, or completed with `found: false`; either way retry after the backoff.
12. **`OpenRemovals >= MaxOpen` → `Wait("removal cap")`.**
13. **Otherwise → `Remove{Server: bmh label}`.** The controller logs `Would remove` instead when `DryRun` is set.

**The controller executes `Remove`** as follows:
- Take a hub-wide mutex covering the cap check and the POST, so 4 workers can't overshoot `maxOpenRemovals`.
- Refresh `OpenRemovals` from the remove sweep cache.
- Re-read the Agent **uncached** (`Reader.Get`) and re-run rules 2–4 on it.
- Then POST, and invalidate the remove-id cache for that server.

No pre-POST block marker is needed:
- the fixed per-server id makes a duplicate a 409;
- a re-run after completion is a no-op;
- an ambiguous outcome is resolved by the next Describe.

**Outcome mapping:**

| Outcome | Result label | Action |
|---|---|---|
| 202 | `started` | requeue after `interval` |
| 409 | `already_running` | requeue after `interval` |
| other 4xx | `rejected` | error log (sanitised, see `logging.go`), requeue after `interval` |
| 5xx, timeout, transport error | `ambiguous` | requeue after `interval`; the next Describe decides |

**Requeue:** a `Wait` or `Remove` Agent requeues every `interval` (60 s) until the Agent is gone. `Label` and `None`
rely on watch events.

**Open-removal count:** Running runs among the remove ids of all labelled, unbound Agents in the cache. Use a per-id
Describe cache with a 10 s TTL, sharing in-flight calls like the pool sweeper, invalidated per server after each POST.
Runs whose Agent has already been deleted (the last phases) are not counted. That is accepted, because the cap limits
load and is not a guarantee.

### 4.4 Why there is no 1- or 5-minute timer

The Retirer reconciles an Agent on every change to its parsed view. A bind changes `spec.clusterDeploymentName`, so
the label is written within seconds. A timer would only widen the window in which a bind followed by a release goes
unlabelled, and that miss is permanent. The `interval` requeue of retiring Agents is the only periodic work.

## 5. Configuration

| Key | Level | Default | Rules | Live or startup |
|---|---|---|---|---|
| `removeURL` | hub | `installURL` with its last path segment replaced by `remove-server-from-mce` | absolute http(s), no userinfo or fragment (same checks as `installURL`) | startup |
| `removeWorkflowQueue` | hub | `remove-server-from-mce-workflow` | non-empty | startup |
| `maxOpenRemovals` | hub | `10` | 1..100 | live (`snapshot()`) |
| `removeRetryAfter` | hub | `"30m"` | > 0, ≥ `interval` | live (`snapshot()`) |
| `dryRunRemove` | pool (`defaults` and `pools.<name>`) | `true` | bool; allowed next to `exclude: true` | live |

**For the hub keys,** follow the checklist in [CLAUDE.md](../../CLAUDE.md) ("A hub setting is live only if
`snapshot()`..."):
- `types.go`, `load.go` (input field and duration parsing), `DefaultHub` and `validateDocument`.
- For startup keys: `startupBoundChanged` in `cmd/main.go`, the chart checksum in `_helpers.tpl`, and
  `docs/ops/operating.md`.
- For live keys: the `snapshot()` copy.

**For the pool key:**
- `PoolConfig`, `PoolOverride`, `poolInput` and `convert`; `merge`; `clonePools`; `Defaults()`; `validatePool`.
- Allow it in an `exclude` entry.
- `values.yaml`, `values.schema.json` (`additionalProperties: false`), `TestChartDefaultsMatchCode`, and the "exactly
  nine pool settings" text in `operating.md`, which becomes ten.

## 6. Metrics and alerts

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `herzi_pool_retiring` | gauge | `pool` | labelled unbound Agents of a managed pool (not supply) |
| `herzi_retiring_agents` | gauge | `state` = `waiting`, `removing`, `blocked` | hub-wide: `blocked` = rules 3–8, `removing` = Running run, `waiting` = backoff or cap |
| `herzi_agents_labelled_total` | counter | — | labels written |
| `herzi_label_errors_total` | counter | — | failed label patches |
| `herzi_removals_total` | counter | `result` = `started`, `already_running`, `ambiguous`, `rejected`, `dry_run` | remove POSTs |

Keep label sets bounded. Add each metric to the catalogue in `internal/metrics/metrics_test.go`, to `ForgetPool` for
the per-pool one, and to the README metric table.

**Alerts** (`charts/herzi/files/alerts.rules.yaml`, with promtool cases in `hack/chart/alerts.test.yaml`):

| Alert | Expression | Runbook entry |
|---|---|---|
| `HerziRetireBlocked` | `herzi_retiring_agents{state="blocked"} > 0` for 30m | find the reason in the logs: no bmh label, an unguarded NodePool, no workers, a paused pool |
| `HerziRetiringAgentsStuck` | `herzi_retiring_agents > 0` for 2h (any state) | removal failing repeatedly; check the remove runs in Temporal |

Retiring Agents are never `StuckAgents`, so `HerziStuckAgents` is unaffected.

## 7. File-by-file changes

| Area | Change |
|---|---|
| `internal/kube` | Constants `LabelEverInstalled = "redbull.io/ever-installed"`, `EverInstalledValue = "true"`, `LabelBMH = "agent-install.openshift.io/bmh"`. Replace `Selector.HasExpressions` with parsed `Expressions []Requirement{Key, Operator string; Values []string}` and a `LabelSelector()` builder. Add fixtures to `testdata`. Keep `TestTransform` green. |
| `internal/pipeline` | `attribute.go`: Retiring bucket and selector rule. `types.go`: `PoolFacts.Retiring`, `RetireInput`, the `RetireAction` sealed type (`Label`, `Remove`, `Wait`, `None`), `RemoveRequest{Agent, Server}`, `RemoveRun{Status, Started, Closed}`, `PollerStatus.Remove`. New `retire.go`: `DecideRetire`, `Guarded`. `ports.go`: `Remover.Remove(ctx, RemoveRequest) (RemoveResult, error)` and `RemoveSweeper.Describe(ctx, server)` plus `Invalidate(server)`. |
| `internal/ids` | `RemoveServer(server) = "remove-server-from-mce-" + strings.ToLower(server)`. Pin it in `ids_test.go` to `shared/workflow_ids.py:137-146` with literal cases. |
| `internal/temporalx` | A `RemoveSweeper` that reuses `describe`, `IsNotFound`, the per-call timeout and the TTL and in-flight cache. `queues.go`: poll `removeWorkflowQueue` (workflow type) into `PollerStatus.Remove`. Pool `Validate` does **not** read `Remove`, so growth never blocks on the remove worker. |
| `internal/installer` | Move the shared transport, `failure`, `redact`, `snippet`, `safeURL` and `transportError` into a common file. Add a `Remover` with body `{server_name, mce_cluster: hub.MCEName, namespace: hub.AgentNamespace}` and the outcome mapping in 4.3. Keep `DisableKeepAlives`, no redirects and no retries. |
| `internal/controller` | New `retirer.go` (reconciler, gather, execute) and `retirer_watches.go` (`SetupWithManager`, mappers). Extract `cachedPollers` into a shared type both reconcilers use. Add the RBAC marker `// +kubebuilder:rbac:groups=agent-install.openshift.io,resources=agents,verbs=patch`, then run `make manifests`. Add `retiring` to `logStatus` and `observe`. |
| `internal/config` | The keys in section 5. |
| `internal/metrics` | The metrics in section 6. |
| `cmd/main.go` | Build the Remover (reuse `HERZI_API_BEARER_TOKEN` and `HERZI_INSTALL_CA_FILE`), the RemoveSweeper and the shared poller cache. Register the Retirer. |
| `charts/herzi` | Values and schema keys; regenerated `files/rbac-rules.yaml` (never edited by hand); the two alerts. Still exactly 21 packaged files. |
| Docs | Section 2. Plus `docs/ops/runbook.md` (the alerts above, and how to remove a no-bmh-label Agent by hand through the workflows UI), `docs/design/notes/controller.md` and `docs/design/notes/pipeline.md` for the decisions moved out of code. |

## 8. Tests

**Pure tables** (`internal/pipeline`):
- **Attribute:**
  - a labelled bound Agent is Assigned;
  - a labelled unbound Agent is Retiring and never claimable, settling or stuck;
  - unlabelled Agents behave as before.
- **Selector rule:**
  - no expressions;
  - the exact expression;
  - wrong operator, extra values, a second expression, or a different key → Problem.
- **`Guarded`:**
  - a matching NodePool with the requirement, and without it;
  - a non-matching NodePool;
  - a paused NodePool, and a deleting one;
  - a NodePool with no InfraEnv label (it matches everything);
  - an unparseable NodePool.
- **`DecideRetire`:** one row per rule in 4.3, plus:
  - ignored, excluded and stray Agents still get `Label` and `Remove`;
  - pause blocks `Remove` but not `Label`;
  - a Completed run whose Agent is still present retries after the backoff.
- **Decide design table:** add rows showing retiring Agents are not supply. Keep the property "more supply never
  increases the order", and add "a retiring Agent never lowers the order".

**Installer:** `httptest` cases for 202, 409, 422, 500, a timeout and an oversized body. The request body must contain
exactly three fields.

**ids:** the literal Python-pinned cases.

**temporalx:** the fake client covers NotFound, Running, each closed status, a Describe error, the cache TTL and
`Invalidate`.

**Envtest:** extend the `world` fake so `Remove` registers a Running run under the remove id, then closes it and
deletes the Agent on demand. A fake that never reports the run would make the Retirer correctly POST again; this is the
CLAUDE.md pitfall. Scenarios:
- bind → label written;
- release → exactly one POST;
- while Running, no second POST;
- Agent deleted → the Retirer forgets it;
- unguarded NodePool → no POST and `blocked`;
- ignored InfraEnv → labelled and removed, but absent from the pool metrics;
- stray Agent (no pool label) → labelled and removed;
- 20 simultaneous releases with `maxOpenRemovals: 10` → never more than 10 Running;
- dry run → log and metric only;
- pause → label yes, remove no.

## 9. Implementation order and checks

1. Step 0 docs (section 2).
2. `kube` and `ids`.
3. `pipeline`: the Attribute bucket, the selector rule, `DecideRetire`, `Guarded`, with their tables.
4. `config`.
5. `installer` Remover; `temporalx` RemoveSweeper and queue.
6. `controller`: the Retirer, the shared poller cache, RBAC, `make manifests`.
7. `metrics`.
8. Chart values, schema and alerts.
9. `cmd` wiring.
10. Envtest scenarios.

**Run the full gate from [CLAUDE.md](../../CLAUDE.md) after each layer.** Run the envtest specs yourself; the fake
client passing is not proof.

```bash
gofmt -l cmd internal; go build ./... && go vet ./...
KUBEBUILDER_ASSETS=$PWD/bin/k8s/1.37.0-linux-amd64 go test -race -count=1 -cover ./...
./bin/golangci-lint run ./cmd/... ./internal/...
make vuln helm-lint helm-test test-alerts manifests-drift
```

**Rules from CLAUDE.md that apply here:**
- Read Agents and NodePools as unstructured through `internal/kube`; do not import the typed APIs.
- Keep comments to the necessary ones, and move rationale to `docs/design/notes/`.
- Pass external error text through `sanitize` and `summarize`.
- Use a Conventional Commit subject, `feat!:` for the behaviour change.

## 10. Rollout

1. **Deploy.**
   - Labelling is live; removal is in dry run everywhere.
   - Retiring Agents leave supply, so pools may order replacements, still gated by `dryRunGrow`.
   - Expect every Agent bound today to be labelled on the first pass (D3).
2. **Add the `DoesNotExist` expression to the NodePools.**
   - **Try one non-production NodePool first.** HyperShift builds the AgentMachineTemplate from `agentLabelSelector`,
     so changing it may roll the NodePool. On `upgradeType: Replace` that replaces every node, and with removal live
     each released server would then be retired.
   - Then check that `herzi_retiring_agents{state="blocked"}` falls to 0.
3. **Read the `Would remove` logs.** Check that each server name is the bmh label and that no bound Agent appears.
4. **Set `dryRunRemove: false` one pool at a time.** Agents with no pool, and pools without an override, follow
   `defaults.dryRunRemove`.

## 11. Residual risks and checks on a real hub

**Accepted residual risks:**
- A bind and release that both happen while herzi is down are never labelled, and that server returns to the pool.
- Agents released before the first deploy can't be known to be used, so they stay in the pool.
- `upgradeType: Replace` NodePools retire their whole fleet on every upgrade. Each upgrade then needs that many new
  installs.
- The remove workflow id has no MCE in it. Two hubs removing the same server name at once would get a 409; this can't
  happen in practice, because a server is on one MCE.
- The open-removal cap undercounts runs in their last phases, after the Agent is deleted.
- server-scan can hand a removed server out again (D2).

**To verify on a hub before going live:**
- The CAPI agent provider version behaves as in 3.2.
- Every Agent carries `agent-install.openshift.io/bmh` (count the ones that don't).
- The assisted-service controllers keep a foreign label on Agents (they use read-modify-write).
- herzi's Temporal credentials can Describe `remove-server-from-mce-*` ids and the `remove-server-from-mce-workflow`
  queue.
- What changing `agentLabelSelector` does to an existing NodePool, as in rollout step 2.

## 12. Out of scope

- Marking a used server permanently out of service in server-scan (D2).
- Agents outside `agentNamespace` (D5).
- Correcting orphan-bound Agents. They stay bound, so they are not retired, and the existing alert still covers them.
