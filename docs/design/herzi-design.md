# Inventory Controller — v2

**Scope:** permanently add-only. The controller only adds servers; capacity is never reclaimed.
VM creation and install-route idempotency remain backlog items.
**Design source:** [`inventory-controller-original.md`](inventory-controller-original.md), superseded historical input.
**Reviews applied:** two Codex reviews, a logic simulation review and a feasibility review.
Independent round-2 safety fixes and the owner-approved add-only change are incorporated below.

| Part | What it is |
|---|---|
| [Part 1](#part-1--how-it-works-in-words) | The logic in plain words, with worked examples of the cases the controller meets |
| [Part 2](#part-2--pseudocode) | Framework, types, the four pipeline stages, the growth protocol, config, tests |
| [Part 3](#part-3--flaws-found-and-how-they-are-handled) | Every flaw found, how it is fixed, the edge cases, residual risks, decisions, backlog |

---

# Part 1 — How it works, in words

## The idea

One controller runs on each MCE hub. A **pool** is one InfraEnv: the machines of one node type in one zone.
Tenants say how many workers they want by writing NodePools (`replicas`). Machines that exist are
**Agents**. The controller keeps each pool a little bigger than demand, so a tenant that scales up
finds machines already waiting instead of waiting for a new install.

Every pass, for each pool:

```text
buffer = max(minFree, (highPct*demand + 50)/100)  # integer half-up rounding
target = demand + buffer
need   = target - (assigned + claimable + settling + open)
```

With valid observations and an unblocked pool, positive need enters Full/Probe growth.
At or above target, the controller holds (`HoldAtOrAboveTarget = "at or above target"`),
before considering failures or probes. Capacity is never reclaimed: lowering demand or
the buffer only stops additions. Spare capacity can persist indefinitely.

With `highPct: 30` and `minFree: 2`:

| Demand | Rounded percentage | Buffer | Target |
|---:|---:|---:|---:|
| 0 | 0 | 2 | 2 |
| 4 | 1 | 2 | 6 |
| 6 | 2 | 2 | 8 |
| 10 | 3 | 3 | 13 |
| 12 | 4 | 4 | 16 |
| 20 | 6 | 6 | 26 |
| 50 | 15 | 15 | 65 |

The default target buffer intentionally doubles from 15% to 30%: demand 20 previously
had three spare machines (target 23), now six (target 26). Set `highPct: 15` explicitly
to retain the old target behavior. The `minFree` floor still protects small pools.

**What each supply term means**

| Term | Meaning |
|---|---|
| assigned | Agents bound to a tenant cluster |
| claimable | Agents free to be claimed right now: unbound, approved, connected, not deleting |
| settling | Agents that are free but not claimable *yet*: rebooting after a release, just registered, or carrying a leftover label. Counted for up to an hour from their last state change, then they stop counting and raise an alert |
| open runs | Every Running install slot, including brief overlap after its Agent arrives |

## Worked cases

### 1. Steady state
Demand 10, assigned 10, claimable 3. Target 13, supply 13. **Nothing to do. The controller stays silent.**

### 2. A tenant scales up
Demand goes 10 → 16, with 10 assigned and 3 free. New buffer is 5, target 21.

```
10:00  the NodePool claims the 3 spares: assigned 13, claimable 0 → supply 13, need 8
10:00  controller orders 8 machines (one request of count=8)
10:02  8 runs open → supply 21, need 0 → Hold (at or above target)
10:25  runs finish, Agents appear; tenant claims 3 more, leaving claimable 5 → buffer rebuilt
```

The tenant got 3 machines immediately. The other 3 wait for installs; five rebuild the buffer.

### 3. A very large scale-up
An empty pool, demand 0 → 50. Target 65. The controller never has more than 15 runs open per pool
(a load limit) and the API takes at most 20 per request.

```
10:00  need 65 → order 15
10:20  10 runs done: supply 10 Agents + 5 open → order 10 more (headroom 10)
...    about 5 waves; each pass orders only what is still missing
```

It never re-orders what is already in flight: open runs are subtracted every time.

### 4. A tenant scales down, then up again
Demand 20 → 4. 16 Agents are released. They reboot into the discovery image for ~12 minutes and are
wiped before they register as free.

```
10:00  assigned 4, claimable 6, settling 16 (the rebooting ones) → supply 26
10:10  tenant scales to 12 → target 16, supply 26 → need -10 → no order
10:21  the 16 return as claimable
```

The rebooting machines count, so the controller does not order machines nobody needs. Capacity remains available for future demand.

### 5. The inventory has no free machines
The pool is short by 6 and server-scan has nothing for it.

```
10:00  controller orders 6 → all 6 runs fail within seconds with "no server available"
10:00  the pool enters PROBE mode: it will order exactly 1 machine, at most every 20 minutes
10:20  probe → fails; 10:40 probe → fails ...   alert: "pool X short by 6, needs hardware"
```

It never stops trying, but a pool with no hardware costs one failed run every 20 minutes instead of six
every two. 20 minutes because server-scan refreshes its state about every 15 minutes.

### 6. Hardware appears
Same pool, 3 hours later someone racks servers and server-scan sees them.

```
next probe (within 20 min): the run is still going 2 minutes later AND acquire_servers completed → proof a machine was found
controller returns to FULL orders at once: the probe counts as supply → order the remaining need
```

A run older than 2 minutes proves recovery only after its acquire_servers activity completed.
Retrying earlier activities does not prove acquisition; the controller need not wait for the whole install.

### 7. Only part of an order can be satisfied
Order of 10, server-scan has 6 machines.

```
10:00  6 runs get a machine and keep going, 4 fail at once ("no server")
       the 6 started BEFORE the failure closed → they are not proof of new hardware
10:00  pool in probe mode → wait for the scheduled probe
10:20  send one probe despite the older open work (subject to total open-run cap)
10:25  older runs finish; next probe follows probeNotBefore
```

The controller does not send a second burst of 4 doomed runs, which a naive "a success happened"
rule would have caused.

### 8. One broken machine
A candidate never boots. The run waits an hour, rolls it back and tries the next machine.

```
the run stays open the whole time and counts as supply → no extra order
a single failed run for any non-capacity reason → alert only, full orders continue
3 such failures in a row, nothing succeeded → treat like case 5 (probe) and alert "investigate"
```

### 9. The reply to an order is lost
```
10:00  POST count=5, connection drops, controller does not know whether it worked
10:00  pool blocked for 5 minutes (stored on the InfraEnv, so a restart does not forget it)
10:05  the sweep describes every slot id: 5 runs open → need 0, nothing re-ordered
       (if only 3 started, it orders 2)
```

### 10. The controller restarts or loses its lease
The pod exits at once, with no draining. The new leader reads the durable state and pre-POST block
annotations, then asks Temporal about all 100 slots of each pool. It reaches the same answer as the old one.

### 11. A worker is down
The pool is **blocked, not ordered**, when no worker polls the hub's activity queue, the install
workflow queue or the segment queue: such runs never fail and never start, so ordering would only pile up
open runs. An alert fires. (Pollers linger about 5 minutes after a worker dies, so detection is late.)

### 12. A NodePool is deleted or paused
It is not counted as demand. Its Agents unbind and return to the pool as settling and then claimable.
If its Agents stay bound (a HyperShift bug), `assigned` is higher than `demand` for hours: the
controller **alerts**, because it cannot tell which bound Agents are orphaned. This is the one case not fixed automatically.

### 13. A stray Agent or an unattributable NodePool
An Agent with no pool label, or a NodePool with no InfraEnv label, produces a warning and a metric.
It never blocks other pools.

### 14. An unmanaged or paused pool
Only pools listed in the config are managed. A pool can also be paused with an annotation or a config
flag. The controller still reads and reports it, but never orders.

### 15. Dry run
With `dryRunGrow: true` it logs "WOULD INSTALL" with the exact request instead of sending it.

---

# Part 2 — Pseudocode

## Framework: controller-runtime, scaffolded with Kubebuilder, no CRD

The controller owns no resource. It watches InfraEnv, NodePool and Agent, which belong to other projects.

```
kubebuilder init --domain <yours> --repo <module>
kubebuilder create api --group agent-install --version v1beta1 --kind InfraEnv \
    --controller --resource=false \
    --external-api-path=github.com/openshift/assisted-service/api/v1beta1 \
    --external-api-domain=openshift.io
# the same with --kind NodePool (group hypershift.openshift.io) and --kind Agent;
# then merge the generated reconcilers into ONE controller: For(InfraEnv) + Watches(NodePool, Agent)
```

| controller-runtime | Used for |
|---|---|
| Lease leader election | one active reconciler. Losing the lease exits the process with no graceful drain (`gracefulShutdownTimeout = 0`) |
| One reconcile per key | the key is the InfraEnv name; **the mapping functions for NodePool and Agent must return exactly the same namespace/name as `For`**, or the serialisation breaks |
| `MaxConcurrentReconciles: 4` | different pools reconcile in parallel; every external call has a deadline and each reconcile has an overall timeout |
| `GetAPIReader()` | uncached reads of Agents, label-filtered and namespace-scoped |
| `RequeueAfter` | polling Temporal; it cannot be watched |
| rate limiter | only slows *errors*, so event-driven reconciles need our own per-pool throttle (below) |

**Cache.** Restrict the Agent cache to the agent namespace and strip `managedFields` and inventory with a
`DefaultTransform`. Do **not** use a label selector on Agents: it would hide unlabeled Agents and the
"agent not in any pool" warning could never fire. `PartialObjectMetadata` is not enough, since the
controller needs spec and status.

**Namespace.** A NodePool's selector gives only the InfraEnv name. The pool key is the InfraEnv name in
one configured `agentNamespace` (default `inventory`), the same value sent in the install request.

## Layout

```
cmd/main.go                    manager, scheme, leader election, metrics
internal/controller/           thin reconciler: gather -> pipeline -> execute -> requeue
internal/gather/               I/O: k8s reads, Temporal sweep, failure types, poller counts
internal/pipeline/
    attribute.go               PURE  raw objects        -> PoolFacts
    validate.go                PURE  PoolFacts+raw      -> Verdict
    decide.go                  PURE  facts+config+now   -> Intent
    plan.go                    PURE  intent+limits      -> []InstallRequest
internal/ids/                  workflow-id builders; a test pins them to the Python builders
internal/installer/            I/O: POST install-server
internal/config/               Config, validation, last-good on a bad update
```

## Workflow ids

Pool IDs are `install-server-<mce>_<infraEnv>_<slot>` (slots 1–100); named IDs
are `install-server-<mce>_<lowercased-server>`. The first separator is `-`, then `_`
after the MCE and InfraEnv. Exact slot Describes account for pool runs; named runs
are outside pool accounting. The authoritative builders are
`/home/toto/code/workflows/shared/workflow_ids.py`.

Runs started before the workflows PR keep their previous IDs and are not accounted
for by this controller. Start every pool in dry run. Enable real growth
(`dryRunGrow: false`) only after verifying in Temporal that no previous-ID install
runs are still open. There is no automatic multi-family accounting.

## Types

```go
type PoolKey string                                   // InfraEnv name in agentNamespace

type SlotRun struct {
    Slot     int
    Status   string     // Running | Completed | Failed | TimedOut | Terminated | NotFound
    Started  time.Time
    Closed   time.Time  // zero while Running
    FailType string     // Failed/TimedOut close history; unreadable history => unknown
    Acquired bool       // completed acquire_servers activity in this exact execution
}

type RawSnapshot struct {
    InfraEnvs []InfraEnv; NodePools []NodePool; Agents []Agent
    Sweep     map[PoolKey]SweepResult   // Slots []SlotRun; Err error  (any failed Describe)
    Pollers   PollerStatus              // activity, workflow and segment queues; Err
    ReadErrs  []ReadError
    At        time.Time
}

type PoolFacts struct {
    Key        PoolKey
    Managed, Paused bool
    BlockedUntil time.Time        // from the InfraEnv pending marker written BEFORE each POST
    Demand     int
    Assigned   int         // bound to a cluster
    Claimable  int         // unbound, approved, connected, no agentMachineRef, not deleting
    Settling   int         // unbound + not claimable + not deleting, last state change < settlingMax (60m)
    StuckAgents int        // same but older than settlingMax: NOT counted, alert
    State      State       // durable annotation merged with sweep evidence
    Open       []SlotRun   // every Running slot
    Closed     []SlotRun   // closed slots (probe evidence)
    Problems   []Problem   // pool-scoped: block this pool only
    Warnings   []Warning   // hub-scoped: metric + log, never block
}

type Verdict struct{ Key PoolKey; Blocked bool; Reasons []string }

type Mode int                // Full | Probe
type Intent interface{ isIntent() }                       // sealed: Grow, Hold
type Grow        struct{ Key PoolKey; Need int; Mode Mode }
type Hold        struct{ Key PoolKey; Reason string }

type InstallRequest struct{ Key PoolKey; Count int }      // 1..20, namespace always sent
```

## Reconcile

```go
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    cfg := r.cfg.Snapshot()                                // one frozen config for the whole pass
    ctx, cancel := context.WithTimeout(ctx, cfg.ReconcileTimeout); defer cancel()
    key := PoolKey(req.Name)
    pc  := cfg.ForPool(string(key))                        // defaults with this pool's overrides merged in

    raw   := r.gather.Snapshot(ctx, key)                   // sweep reused if younger than 10 s (event bursts)
    facts := pipeline.Attribute(raw, cfg)[key]
    v     := pipeline.Validate(facts, raw, cfg)
    metrics.Observe(facts, v)                              // gauges every pass, even when blocked
    if v.Blocked { log.Warn(key, v.Reasons); return requeue(2*time.Minute), nil }

    switch i := pipeline.Decide(facts, cfg, raw.At).(type) {
    case Hold:        log.V(1).Info("hold", i.Reason)
    case Grow:
        // last check before spending: re-read this pool's Agents/NodePools UNCACHED, re-run all
        // stages on the fresh data. Order only if it still says Grow, and never more than before.
        fresh := r.gather.Recheck(ctx, key, raw.Sweep)
        v2 := pipeline.Validate(fresh, raw, cfg)
        g2, ok := pipeline.Decide(fresh, cfg, raw.At).(Grow)
        if v2.Blocked || !ok { break }
        // Check again AFTER the uncached recheck; elapsed time cannot refresh an old sweep.
        if !raw.Sweep.ObservedAt.After(r.previousAttempt(key)) { break }
        g2.Need = min(g2.Need, i.Need)
        if pc.DryRunGrow { log.Info("WOULD INSTALL", g2); break }
        for _, ir := range pipeline.Plan(g2, pc, fresh) {
            // Persist pending deadline and, for Probe, its schedule BEFORE spending.
            // Independent cancellation-detached 10-second patch; failure means no POST.
            if err := r.prewritePending(ctx, key, g2.Mode); err != nil { return ctrl.Result{}, err }
            res, err := r.installer.Install(ctx, ir)
            r.sweeper.Invalidate(key) // every attempt; next sweep must postdate this attempt
            r.recordOutcome(ctx, key, ir, res, err) // definite: null clear; uncertain: renew from outcome time
        }
    }
    return requeue(2*time.Minute), nil
}
// SetupWithManager: For(InfraEnv); Watches(NodePool/Agent -> same key);
// accepted file-watcher updates requeue all pools;
// leader election ON (LeaderElectionReleaseOnCancel for clean rollouts).
```

## Stage 1 — Attribute (pure)

```go
func Attribute(raw RawSnapshot, cfg Config) map[PoolKey]PoolFacts {
    for each InfraEnv ie (not deleting): facts[ie] = PoolFacts{Managed: cfg.Manages(ie),   // listed under pools:
        Paused: cfg.ForPool(ie).Paused || annotationPaused(ie), BlockedUntil: annotation(ie, "blocked-until")}

    for each NodePool np:
        switch {
        case np.Platform.Type != "Agent":                         skip
        case np.DeletionTimestamp != nil:                         warn(np, "deleting")
        case isPaused(np):                                        warn(np, "paused")        // pausedUntil: "true" or RFC3339
        case no IELabel in selector:                              warn(np, "not tied to an InfraEnv")   // hub-scoped
        case ie not known:                                        warn(np, "unknown InfraEnv")          // hub-scoped
        case len(MatchLabels) != 1 || len(MatchExpressions) > 0:  problem(ie, "selector must be only the InfraEnv label")
        case np.Replicas == nil:                                  problem(ie, "no replicas (autoscaling?)")
        default:                                                  facts[ie].Demand += *np.Replicas
        }

    for each Agent a:
        ie := a.Labels[IELabel]
        if ie == "" || unknown(ie): warn(a, "agent not in any pool"); continue
        switch {
        case a.Spec.ClusterDeploymentName != nil:   facts[ie].Assigned++
        case a.Deleting:                            // ignore
        case isClaimable(a):                        facts[ie].Claimable++
        case sinceLastStateChange(a) < cfg.ForPool(ie).SettlingMax:  facts[ie].Settling++   // includes a stale agentMachineRef label
        default:                                    facts[ie].StuckAgents++
        }

    for each pool: Open = Running slots; Closed = closed slots           // no phase filter, no progress queries
}
// isClaimable = approved, state known-unbound, connected, no clusterDeploymentName, no agentMachineRef label.
```

## Stage 2 — Validate (pure, fail closed, pool-scoped)

```go
func Validate(f PoolFacts, raw RawSnapshot, cfg Config) Verdict {
    // Blocked => no mutating action; metrics and a warning only.
    - !f.Managed || f.Paused
    - f.BlockedUntil is in the future              // unclear POST earlier (stored on the InfraEnv)
    - a read for THIS pool failed
    - the sweep for THIS pool had any error        // never treat "unknown" as zero
    - f.Problems non-empty
    - InfraEnv deleting, or spec.clusterRef set (early-bound)
    - raw.Pollers.Err != nil, or any of {activity queue, install workflow queue, segment queue} has 0 pollers
    - pool config invalid
}
// Hub-scoped findings (Warnings) never block another pool.
```

## Stage 3 — Decide (pure)

```go
func Decide(f PoolFacts, cfg Config, now time.Time) Intent {
    p       := cfg.ForPool(string(f.Key))              // defaults with this pool's overrides merged in
    buffer := max(p.MinFree, (p.HighPct*f.Demand + 50) / 100) // integer half-up
    target := f.Demand + buffer
    need   := target - (f.Assigned + f.Claimable + f.Settling + len(f.Open))
    if need <= 0 { return Hold{f.Key, HoldAtOrAboveTarget} } // "at or above target"

    // ---- probe rule: merge durable state with this sweep ----
    failing, lastFail := hardwareLooksMissing(f, p, now)
    if failing {
        if exists open run started after lastFail         { return Hold{f.Key, "probe running"} }
        if now.Before(f.State.ProbeNotBefore)           { return Hold{f.Key, "probe wait"} }   // 20 min
        return Grow{f.Key, 1, Probe}
    }
    return Grow{f.Key, need, Full}
}

func hardwareLooksMissing(f PoolFacts, p PoolConfig, now time.Time) (bool, time.Time) {
    state := MergeState(f.State, f.Open + f.Closed, now, p)
    // MergeState admits proof only for post-failure Completed runs or acquired mature Running runs.
    lastFail := state.capFailAt if state.capFailAt > state.lastOKAt else zero
    if len(state.otherFails) >= 3: lastFail = max(lastFail, latest other failure close time)
    return lastFail != zero, lastFail
}
// Alert by failure type (metrics, not decisions):
//   capacity class -> "pool X short by N, needs hardware"      other -> "pool X install failing: <type>, investigate"
```

## Stage 4 — Plan (pure)

```go
func Plan(g Grow, p PoolConfig, f PoolFacts) []InstallRequest {
    const apiMax = 20                                              // router.py: count le=20, a hard limit
    headroom := p.MaxOpenRunsPerPool - len(f.Open)                 // per-pool load limit (e.g. 15), not a candidate guarantee
    n := min(g.Need, headroom, apiMax)
    if n <= 0 { return nil }
    return []InstallRequest{{Key: g.Key, Count: n}}                // big deficits go in waves, one request per pass
}
```

## Growth protocol (gather + recordOutcome)

```
Counting open runs and merging durable evidence:
  describe  install-server-<mce>_<infraEnv>_<1..100>   (parallelism ~10, per-call deadline)
  NotFound (errors.As serviceerror.NotFound) = free slot. Any other error = the pool is BLOCKED this pass.
  Describe(workflowId) with no run id returns the LATEST run and is strongly consistent, unlike a List.
  For slots closed Failed within failureMemory: read the failure type from the CLOSE EVENT
    (GetWorkflowHistory, close-event filter), cached by (workflowId, runId).
  Cache: a pool is swept at most once per 10 s absent invalidation. Every POST attempt invalidates it.
  Spending and Recheck require ObservedAt after the pool's previous attempt.
  State retains failures across slot reuse; unknown history is a capacity-equivalent spending guard.
  Closed runs vanish after the Temporal namespace retention: retention must be well above
  max(probeInterval, failureMemory).

Before a POST: persist blocked-until using an independent bounded 10-second context; failure means no POST.
After a POST:
  202: the slots whose status is "started" are ordered. "failed" items are NOT assumed absent
       (a lost Temporal ack looks like a failure): the next sweep decides.
       started < requested  => metric grow_short. Clear blocked-until with a merge-patch null;
       a failed clear is logged and the marker expires.
  timeout / connection error / 5xx: ambiguous => extend the prewritten blocked-until from outcome time
       on the InfraEnv; do not retry the same request.
  422: our bug => log error, metric, same block.

Pollers: DescribeTaskQueue (plain, not Enhanced) for the hub activity queue, the install workflow queue and the
segment-lifecycle queue. Detection lags ~5 min after a worker dies.
```

## Durable pool state and the pre-POST marker

One annotation per InfraEnv, `herzi.redbull.io/state`, holds compact JSON of at most 1 KiB:
`capFailAt` is the latest capacity/unknown failure close time; `otherFails` contains at most three
most recent distinct `{runId, closedAt}` failures; `lastOKAt` records completion/recovery proof;
`probeNotBefore` records the earliest next probe. Timestamps use RFC 3339. Unknown JSON keys are ignored.
Missing, corrupt or oversized state degrades to the sweep alone, without blocking or panicking.

`MergeState` is pure. It retains the latest timestamps, deduplicates failures by run ID, drops expired
failure evidence, removes failures covered by success, and retains only the latest three other failures.
A capacity/unknown failure without later proof or three distinct other failures since success enters probe
mode. The initial wait follows the failure close time; each sent Probe extends `probeNotBefore` to its
attempt time plus `probeInterval`. Older open work still subtracts from need and consumes headroom, but
only post-failure open work suppresses a scheduled probe.

A Running recovery run must start after the failure, reach `drawGrace`, AND have a completed
`acquire_servers` activity. The name is verified in `shared/interfaces/server_lifecycle.py` and the
install workflow. Run-pinned all-event history associates completion with its scheduled activity type;
reads are bounded and lazy for capacity/unknown pools only. Positive acquisition is cached by workflow
and run IDs. Read errors supply no acquisition proof. Completed proof must also start after the failure.

The reconciler patches changed state through its own bounded context. Immediately before an additive
POST it merge-patches `herzi.redbull.io/blocked-until = attempt time + ambiguousWait`; failure prevents
spending. A Probe's new `probeNotBefore` is included in the SAME patch. All annotation patches contain
only `metadata.annotations`, using `context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)`.
A definite parsed 202 outcome clears only the marker with JSON null; a failed clear is logged and expires.
Ambiguous/rejected outcomes keep the marker and extend it from outcome time through a fresh context.
Every attempted POST invalidates the cached sweep. A later attempt requires `ObservedAt` strictly newer
than the previous attempt, on both the initial decision and the uncached Kubernetes recheck.

## Config

Settings are of two kinds. **Hub settings** describe this hub or protect against a problem of this hub, and cannot differ per pool. **Pool settings**
all live under `defaults`, and every one of them can be overridden for a single pool.

```yaml
# ---- hub settings (one value per hub, not overridable per pool) ----
interval: 120s
reconcileTimeout: 60s
hubName: ocp4-<hub>
agentNamespace: inventory
installURL: https://<workflows-api>/workflows/server-lifecycle/install-server
maxSlots: 100                       # must equal the route's slot range
ambiguousWait: 5m                   # block after an unclear POST: protects against an API problem on this hub
stuckAfter: 2h                      # an open run older than this => alert only

# ---- pool settings: defaults, overridable per pool ----
defaults:
  highPct: 30
  minFree: 2
  maxOpenRunsPerPool: 15
  probeInterval: 20m                # server-scan refreshes about every 15 min
  drawGrace: 2m                     # minimum age for acquisition evidence to prove recovery
  failureMemory: 6h
  settlingMax: 60m                  # matches the install workflow's one-hour agent wait
  dryRunGrow: true                  # rollout safeguard: log "WOULD INSTALL" instead of ordering
  paused: false

# ---- pools: OPT-IN, keyed by InfraEnv name; only the fields written here differ ----
pools:
  "32-128-zone-1": { minFree: 5, maxOpenRunsPerPool: 8 }
  "16-64-zone-2":  { paused: true }
  "8-32-zone-1":   { highPct: 25, dryRunGrow: true }
```

**Merge rule.** For a pool, each field takes the pool's value if it is written, otherwise the default.
In Go every pool field is a pointer (`*int`, `*time.Duration`, `*bool`), so "not written" is different from
`0` or `false`: `minFree: 0` and `paused: false` are real overrides. A pool that appears in `pools:` with
no fields is managed with all defaults. A pool that is not listed is not managed.

```go
func (c Config) ForPool(name string) PoolConfig {
    p := c.Defaults                                  // value copy of every pool setting
    if o, ok := c.Pools[name]; ok { p = merge(p, o) } // field by field, written fields win
    return p
}
```

**Validation.** Hub `maxSlots` must equal 100. After merging defaults and overrides:
`highPct >= 0`, `minFree >= 0`, `1 <= maxOpenRunsPerPool <= maxSlots`, durations > 0,
`drawGrace < probeInterval`, `failureMemory >= 2*probeInterval`,
`settlingMax >= probeInterval`, and hub `interval <= probeInterval`.
Timing rules apply to defaults and every merged pool. Bad YAML, unknown keys, invalid durations,
hub settings or defaults reject the document and retain last good on reload. Invalid merged
pools remain published with `PoolErrors`; `reason="config_invalid"` blocks only those pools.

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
`highPct: 15` explicitly for the former target buffer; the new default remains 30.

`minFree` is the operator's to size: the largest scale request to absorb without a wait, plus the surge of
any pool still using `upgradeType: Replace`. A pool can also be paused with an annotation on the InfraEnv,
with no ConfigMap edit.

The decision uses signed need; `herzi_pool_need` reports `max(0, need)`.

## Invariants (write these tests first)

| Invariant | Protects against |
|---|---|
| `running` is always subtracted and measured by Describe on fixed ids, never by a List | duplicate orders from visibility lag |
| A Describe error blocks the pool; it is never counted as zero | acting on unknown data |
| One request never exceeds 20 | permanent 422 |
| A lost-ack or `failed` item is resolved by the next sweep, not assumed absent | double order |
| After an unclear POST, no order for `ambiguousWait`, and the block survives a restart | blind retry |
| Probe: Grow is exactly 1, at most every `probeInterval`, while capacity failures have no proof after them | storms, and never stopping |
| Proof must have STARTED after the last capacity failure | doomed bursts after a partial order |
| Free machines (claimable, settling) are never dropped from supply while rebooting | over-order after a scale-down |
| An Agent with a stale `agentMachineRef` is settling, not invisible | runaway ordering |
| At/above target always Holds, regardless of supply composition or failure evidence | no unnecessary additions or probes |
| Zero pollers on any of the three queues, or a poller error, blocks the pool | runs that never start |
| Hub-scoped findings never block another pool | one stray object stalling the hub |
| Unmanaged, paused, early-bound or deleting pool never grows | opt-in only |
| `ids` builder output equals the Python builder's for a table of cases | contract with the route |
| One frozen config and one clock per reconcile | mixed inputs |
| Adding one claimable Agent never increases the ordered count (property test) | monotonicity |

## Test table for Decide

Each custom-buffer row uses `highPct: 0, minFree: <buffer>`; the demand-50 row
uses defaults (`highPct: 30, minFree: 2`). Probe rows use the default timings.
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
| 50 | 0 | 0 | 0 | 0 | – | 15 (default) | Grow 65 Full → Plan one request of 15 |
| 9 | 12 | 0 | 0 | 0 | – | 2 | Hold; sustained assigned > demand alerts |
| 6 | 6 | 1 | 0 | 0 | – | 2 | Grow 1 Full (target − 1) |
| 6 | 6 | 2 | 0 | 0 | – | 2 | Hold (target equality) |
| 6 | 6 | 3 | 0 | 0 | capacity/unknown/three-other failures | 2 | Hold (target + 1); no probe |
| 6 | 6 | 4 | 0 | 0 | – | 2 | Hold (at or above target) |

Additional coverage: zero buffer (`highPct: 0, minFree: 0`); exact-half rounding
(demand 5, `highPct: 10, minFree: 0` gives target 6); and at/above target Holds
for every supply composition, including failure evidence. Under defaults,
demand 20 / assigned 18 / claimable 3 orders 5 toward target 26; even assigned 21
with no other supply needs 5. More supply never increases the order count.

---

# Part 3 — Flaws found and how they are handled

## A. Flaws from the reviews

| # | Flaw | Example | How it is handled |
|---|---|---|---|
| 1 | Duplicate orders (additive POST, visibility lag, lost reply) | order 12, reply lost, next pass orders 12 again → 34 for a target of 22 | The sweep Describes all 100 fixed slots, subtracts open runs, and cannot be reused after an attempt. A pre-POST marker survives cancellation/restart; uncertain outcomes extend it from outcome time. Rare remote-request residual accepted |
| 2 | No leader election | two replicas each order 12 | Lease election; one replica. No fencing: accepted |
| 3 | Stale or partial reads | cached Agents empty → orders again | Errors block the pool; `Recheck` re-reads uncached and re-validates before ordering |
| 4 | `count` > 20 gives 422 forever | need 65 → 422 every pass | Plan caps each request at 20, waves for big deficits |
| 5 | Slots shared across hubs | other hub's runs counted as ours | MCE in the id (workflows PR #12) |
| 6 | Spare may not match the NodePool selector | red/blue example | Not real here: selectors are only the InfraEnv label; any other selector blocks that pool |
| 7 | Unlabeled Agents vanish | 4 of 12 uncounted → over-order | Warning and metric; never blocks another pool |
| 8 | Failed or hung runs | fast fails re-ordered every pass | Probe mode: 1 machine per 20 min, failure-class alerts, poller check, startup-configured stuck alert |
| 9 | Candidate window at batch 20 | 20 unusable candidates | Open-run cap is load tuning only. Real fix is server-side |
| 10 | NodePool nil replicas, deleting, paused | nil pointer; phantom demand | Handled in Attribute; `pausedUntil` parsed |
| 11 | Zero-demand pools auto-seeded | 12 InfraEnvs × minFree 2 → 24 machines | Opt-in `pools` list |
| 12 | Buffer floor meaning | small pools need a fixed spare floor | One buffer: max(minFree, rounded highPct percentage) |
| 13 | 202 does not mean started | all slots busy → started 0 | Only `started` slots counted; the sweep decides the rest |
| 14 | Early-bound InfraEnv | hosts bound at once | Validate blocks it |
| 15 | Hand installs invisible | operator installs 3 by name → 3 extra | Accepted; a per-pool pause switch |

### Independent round 2 implementation

| Finding | Resolution |
|---|---|
| F1 stale sweep spending | Observation timestamp checked after recheck; invalidate every attempted POST |
| F2 lost ambiguity marker | Durable prewrite, detached bounded patch contexts, null clear, outcome-time extension |
| F3 hidden slots | maxSlots must equal the route's 100 slots |
| F4 reused-slot evidence loss | Bounded durable state, run-ID deduplication, persisted probe schedule |
| F5 unreadable failure history | unknown class prevents Full spending |
| F6 runtime mistaken for acquisition | Require completed acquire_servers activity or post-failure workflow completion |
| F7 invalid timing combinations | Validate all four timing relationships for defaults and each merged pool |
| F8 unrelated malformed objects | Identity-first pool-scoped errors; unattributable warnings |
| F9 old hung work | Only post-failure open runs suppress a new probe; total cap still applies |
| F10 whole-document pool rejection | Publish valid changes; config_invalid blocks the affected pool |
| F11 ineffective stuckAfter | herzi_stuck_after_seconds is initialized from startup config (process constant); alert uses `> on() group_left` matching; restart to change |

## B. Flaws from the logic simulation

| # | Flaw | Example | Fix |
|---|---|---|---|
| L1 | Wrong recovery evidence for probe mode | success from before the failure flips to Full → burst of doomed runs; slot reuse hides evidence; recovery waits a whole install | Proof must have started after the last capacity failure and (completed or still running after 2 min with a completed acquire_servers activity). Durable state survives slot reuse; a probe with acquisition proof returns the pool to Full |
| L2 | Released machines vanish while rebooting | released Agents disappear from supply during reboot | `settling` = any free, unbound, non-deleting, not-yet-claimable Agent, measured from its last state change, up to 60 min, then an alert |
| L3 | Orphan-bound Agents count as supply | 4 stale bindings can hide a shortage | Alert when assigned exceeds demand for three hours; investigate ownership. No automatic binding correction |
| L4 | Failure classes | batch shortage shows as `ServerReservedError`; one dead host throttles the pool | `ServerNotAvailableError` and `ServerReservedError` are capacity; unknown history also prevents Full. Three distinct other failed run IDs since last success enter probe mode |
| L5 | Stale `agentMachineRef` Agent in no bucket | runaway ordering | Counted as settling, then an alert |
| L6 | `Recheck` did not re-validate | worker dies just before the POST | Recheck re-runs Validate and Decide, orders at most the first amount |
| L7 | The 5-minute block could miss a canceled outcome | restart forgets it | Prewritten on the InfraEnv with an independent 10-second patch context; clear uses annotation null |
| L8 | Poller check missed the segment queue | runs hang at `resolving-vlan` | All three queues checked |
| L9 | Truncating percentage rounding | demand 5 at 10% must round 0.5 to 1 | Integer half-up `(highPct*demand+50)/100`; test exact-half boundaries |
| L10 | Double count while the Agent exists but the run is open | one pass of 1 too few | Accepted (seconds). Phase queries dropped: they removed almost nothing |

## C. Findings from the feasibility review

| Finding | Change |
|---|---|
| The Kubebuilder command needs `--external-api-path` / `--external-api-domain`, and the three generated reconcilers must be merged | Scaffold section updated |
| Lost lease = immediate exit, an in-flight POST may still land | Block stored on the InfraEnv; sweep on every start |
| Event-driven reconciles are not rate limited | Cache for 10 s per pool; invalidate after every POST attempt; both decisions require observation after the previous attempt |
| A NodePool does not carry the Agent namespace | One configured `agentNamespace`; the key is the InfraEnv name |
| Agent cache can be large | Namespace-scoped cache plus a transform that drops `managedFields`; no label selector |
| Describe does not return the failure | Close event read, cached by (id, runId) |
| `QueryWorkflow` needs a worker and can hang | No queries in v2 |
| `DescribeTaskQueue` pollers linger ~5 min | Documented, accepted |
| Closed runs disappear after retention | Retention must exceed `probeInterval` and `failureMemory` |
| `pausedUntil` is a string, not a bool | Parsed |
| Snippet issues (sealed Intent, zero-value log, shadowed `req`, clock) | Fixed in the pseudocode |

## D. Edge cases and what the controller does

| Edge case | What happens |
|---|---|
| Cold start of a new managed pool, demand 0 | seeds `minFree` machines (target 2) |
| Pool not listed in config / paused / early-bound / deleting | read and reported, never orders |
| Config changes | the file watcher re-queues pools; document/hub/default errors retain last good, merged pool errors block only that pool |
| Inventory runs dry in a batch | the runs that got a machine continue; the failed ones start probe mode |
| Hardware racked | next probe ≤ 20 min, then full orders at once. New hardware reaches server-scan on its vendor collection (~6 h) |
| All candidates dead for a pool | the run fails after ~hours with `AgentNeverAppearedError`; after 3 such failures the pool probes and alerts "investigate" |
| Machine installed but not yet approved | all Agents on this hub are auto-approved; settling covers the short gap |
| Closed runs expired from retention | durable annotation retains evidence until failureMemory; absent/corrupt state degrades to the visible sweep |
| Many Agents flapping | per-pool sweep throttle; reconciles coalesce |
| One slow pool | each reconcile has a deadline; pools reconcile in parallel |
| Both leader and a stuck API request | old request may start runs after a sweep: accepted residual |
| Server-scan outage | installs cannot start; runs that did start stay open and are counted; no extra orders |

## E. Residual risks accepted in v1
- A request still stuck inside the API can start runs after a later sweep (over-order by one batch).
- Leader election has no fencing.
- Orphan-bound Agents are detected (alert) but not corrected.
- `candidate_count` is sized per hub, so two hubs filling the same InfraEnv name at once see more `reserved` skips.
- Nothing in `workflows` creates VMs yet: VM pools run dry when server-scan has none free.
- Pollers are detected ~5 min after a worker dies.

## F. To verify on a real hub
- The exact Agent label key and the Agent state strings.
- That the controller's Temporal credentials can call `DescribeTaskQueue`; the not-found error on your server version.
- The Temporal namespace retention and rate limit.
- Whether any NodePool still uses `Replace`, and its surge.
- Segment worker and install workflow queue names.

## G. Decision log

| Topic | Decision |
|---|---|
| Framework | Kubebuilder scaffold, controller-runtime, no CRD |
| Demand attribution | InfraEnv label in the selector; the selector must be only that label |
| Route | `POST /workflows/server-lifecycle/install-server` with `count` ≤ 20 (no `/bulk`) |
| Scrub | InfraEnv ignition override wipes disks before an Agent registers; assumed perfect |
| Slot discovery | Describe sweep over fixed ids |
| Unclear POSTs | block 5 min (annotation), then sweep; rare race accepted |
| Double counting | all Running runs count; free machines count while settling (≤ 60 min) |
| Upgrade surge | operator sizes `minFree`; most upgrades are InPlace |
| Named / previous-ID installs | outside slot accounting; verify previous-ID runs closed before enabling growth |
| Workflow ids | `install-server-<mce>_<infraEnv>_<slot>` and `install-server-<mce>_<lowercased-server>` |
| Empty pool | probe of 1 machine every 20 minutes (server-scan state refreshes every ~15 min) |
| Failure classes | capacity class = `ServerNotAvailableError`, `ServerReservedError`; other failures probe after 3 in a row |
| No worker | zero pollers on any of the three queues => do not order |
| Stuck run | compare open age to unlabeled herzi_stuck_after_seconds from startup stuckAfter (default 2h); restart to change |
| Capacity policy | add-only; at or above target Holds; lowering demand or buffer only stops additions |

## H. Backlog
- **Idempotency key (or "ensure N open") on the install route:** removes the residual race.
- **Orphan-bound Agent detection** by NodePool, so the controller can exclude them from supply.
- **Verify the discovery-image wipe override:** ordered before registration, fails closed, every disk, present on VM and bare-metal InfraEnvs.
- **VM creation path** and which KubeVirt cluster a new VM is built on.
- **Bare-metal pools in scope?**
- **`InventoryPolicy` CRD** for per-pool config with status.
- **Fencing for leader election**, if the residual risk proves real.
