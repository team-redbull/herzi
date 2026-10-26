# Inventory Controller — v2

**Scope of v1:** grow for real; shrink is log-only. Cooldown, real shrink and hardware growth are in the backlog.
**Design source:** [`inventory-controller.md`](inventory-controller.md), procedures 01 and 02.
**Reviews applied:** two Codex reviews, a logic simulation review and a feasibility review.
Codex's review of this exact version is still pending; its findings will be added to Part 3.

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

```
demand  = sum of NodePool replicas that name this InfraEnv
supply  = assigned + claimable + settling + open runs
target  = demand + low          (a buffer so the next scale-up is instant)
ceiling = demand + high         (above this the pool is too big)
need    = target - supply
```

If `need > 0` it asks the install API for `need` more machines. If the pool is above the ceiling it
only writes a "would remove" log line (shrink is not built yet). Otherwise it does nothing.

**What each supply term means**

| Term | Meaning |
|---|---|
| assigned | Agents bound to a tenant cluster |
| claimable | Agents free to be claimed right now: unbound, approved, connected, not deleting |
| settling | Agents that are free but not claimable *yet*: rebooting after a release, just registered, or carrying a leftover label. Counted for up to an hour from their last state change, then they stop counting and raise an alert |
| open runs | Install runs still going whose machine has not arrived yet |

**The buffer.** With the default settings (low 15%, high 30%, `minFree` 2, minimum band 1):

| demand | low | high | target | ceiling |
|---|---|---|---|---|
| 0 | 2 | 3 | 2 | 3 |
| 4 | 2 | 3 | 6 | 7 |
| 6 | 2 | 3 | 8 | 9 |
| 10 | 2 | 3 | 12 | 13 |
| 12 | 2 | 4 | 14 | 16 |
| 20 | 3 | 6 | 23 | 26 |
| 50 | 8 | 15 | 58 | 65 |

The percentage protects big pools, the floor protects small ones, and the minimum band keeps the
ceiling at least one above the target.

## Worked cases

### 1. Steady state
Demand 10, assigned 10, claimable 2. Target 12, supply 12. **Nothing to do. The controller stays silent.**

### 2. A tenant scales up
Demand goes 10 → 16, with 10 assigned and 2 free. New target is 18.

```
10:00  the NodePool claims the 2 spares: assigned 12, claimable 0 → supply 12, need 6
10:00  controller orders 6 machines (one request of count=6)
10:02  6 runs open → supply 18, need 0 → silent
10:25  runs finish, Agents appear, claimable 2 again → buffer rebuilt
```

The tenant got 2 machines immediately. The other 4 wait for installs, and the buffer is rebuilt behind them.

### 3. A very large scale-up
An empty pool, demand 0 → 50. Target 58. The controller never has more than 15 runs open per pool
(a load limit) and the API takes at most 20 per request.

```
10:00  need 58 → order 15
10:20  10 runs done: supply 25 + 5 open → order 10 more (headroom 10)
...    about 4 waves; each pass orders only what is still missing
```

It never re-orders what is already in flight: open runs are subtracted every time.

### 4. A tenant scales down, then up again
Demand 20 → 4. 16 Agents are released. They reboot into the discovery image for ~12 minutes and are
wiped before they register as free.

```
10:00  assigned 4, claimable 3, settling 16 (the rebooting ones) → supply 23
10:10  tenant scales to 12 → target 14, supply 23 → need 0 → no order
10:21  the 16 return as claimable
```

The rebooting machines count, so the controller does not order 7 machines nobody needs.
Shrink would have shown "would remove" only if nothing was still settling.

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
next probe (within 20 min): the run is still going 2 minutes later → proof a machine was found
controller returns to FULL orders at once: need 6 → orders the rest
```

A run that is still going 2 minutes after it started has passed the "no server" check (that fails in
seconds). The controller does not wait for the probe's whole install to finish.

### 7. Only part of an order can be satisfied
Order of 10, server-scan has 6 machines.

```
10:00  6 runs get a machine and keep going, 4 fail at once ("no server")
       the 6 started BEFORE the failure closed → they are not proof of new hardware
10:00  pool in probe mode, open runs > 0 → hold
10:25  6 finished. Probe mode: next probe after 20 minutes
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
The pod exits at once, with no draining. The new leader keeps nothing in memory: it reads the block
annotation, then asks Temporal about all 100 slots of each pool. It reaches the same answer as the old one.

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

## Workflow ids (workflows PR #12)

```
pool run : add-server-to-mce_<mce>_<infraenv>_<slot>     slot 1..maxSlots (100; must equal the route)
named run: add-server-to-mce_<mce>_<server>              not counted by the controller
```

`_` joins every part (it cannot occur in a Kubernetes name). Old `install-server-*` ids are ignored.

## Types

```go
type PoolKey string                                   // InfraEnv name in agentNamespace

type SlotRun struct {
    Slot     int
    Status   string     // Running | Completed | Failed | TimedOut | Terminated | NotFound
    Started  time.Time
    Closed   time.Time  // zero while Running
    FailType string     // only for Failed; read from the close event, cached per (id, runId)
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
    BlockedUntil time.Time        // from the InfraEnv annotation set after an unclear POST
    Demand     int
    Assigned   int         // bound to a cluster
    Claimable  int         // unbound, approved, connected, no agentMachineRef, not deleting
    Settling   int         // unbound + not claimable + not deleting, last state change < settlingMax (60m)
    StuckAgents int        // same but older than settlingMax: NOT counted, alert
    Open       []SlotRun   // every Running slot
    Closed     []SlotRun   // closed slots (probe evidence)
    Problems   []Problem   // pool-scoped: block this pool only
    Warnings   []Warning   // hub-scoped: metric + log, never block
}

type Verdict struct{ Key PoolKey; Blocked bool; Reasons []string }

type Mode int                // Full | Probe
type Intent interface{ isIntent() }                       // sealed: Grow, WouldRemove, Hold
type Grow        struct{ Key PoolKey; Need int; Mode Mode }
type WouldRemove struct{ Key PoolKey; Surplus, Total, Target, Ceiling int }
type Hold        struct{ Key PoolKey; Reason string }

type InstallRequest struct{ Key PoolKey; Count int }      // 1..20, namespace always sent
```

## Reconcile

```go
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    ctx, cancel := context.WithTimeout(ctx, cfgSnap.ReconcileTimeout); defer cancel()
    cfg := r.cfg.Snapshot()                                // one frozen config for the whole pass
    key := PoolKey(req.Name)
    pc  := cfg.ForPool(string(key))                        // defaults with this pool's overrides merged in

    raw   := r.gather.Snapshot(ctx, key)                   // sweep reused if younger than 10 s (event bursts)
    facts := pipeline.Attribute(raw, cfg)[key]
    v     := pipeline.Validate(facts, raw, cfg)
    metrics.Observe(facts, v)                              // gauges every pass, even when blocked
    if v.Blocked { log.Warn(key, v.Reasons); return requeue(2*time.Minute), nil }

    switch i := pipeline.Decide(facts, cfg, raw.At).(type) {
    case WouldRemove: log.Info("WOULD REMOVE", i)
    case Hold:        log.V(1).Info("hold", i.Reason)
    case Grow:
        // last check before spending: re-read this pool's Agents/NodePools UNCACHED, re-run all
        // stages on the fresh data. Order only if it still says Grow, and never more than before.
        fresh := r.gather.Recheck(ctx, key, raw.Sweep)
        v2 := pipeline.Validate(fresh, raw, cfg)
        g2, ok := pipeline.Decide(fresh, cfg, raw.At).(Grow)
        if v2.Blocked || !ok { break }
        g2.Need = min(g2.Need, i.Need)
        if pc.DryRunGrow { log.Info("WOULD INSTALL", g2); break }
        for _, ir := range pipeline.Plan(g2, pc, fresh) {
            res, err := r.installer.Install(ctx, ir)
            r.recordOutcome(ctx, key, ir, res, err)
        }
    }
    return requeue(2*time.Minute), nil
}
// SetupWithManager: For(InfraEnv); Watches(NodePool/Agent -> same key); Watches(ConfigMap -> all pools);
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
    low     := max(p.MinFree, (p.LowPct*f.Demand + 50) / 100)            // half-up in integers
    high    := max(low + p.MinBand, (p.HighPct*f.Demand + 50) / 100)      // band at least MinBand wide
    target  := f.Demand + low
    ceiling := f.Demand + high

    total   := f.Assigned + f.Claimable + f.Settling
    running := len(f.Open)
    need    := target - (total + running)                                 // running ALWAYS subtracted

    if need <= 0 {
        if total > ceiling && running == 0 && f.Settling == 0 {
            surplus := min(total - target, f.Claimable)                   // only free machines can ever be removed
            return WouldRemove{f.Key, surplus, total, target, ceiling}
        }
        return Hold{f.Key, "inside band"}
    }

    // ---- probe rule: derived from the sweep, no memory ----
    failing, lastFail := hardwareLooksMissing(f, p, now)
    if failing {
        if running > 0                                   { return Hold{f.Key, "probe running"} }
        if now.Sub(lastFail) < p.ProbeInterval           { return Hold{f.Key, "probe wait"} }   // 20 min
        return Grow{f.Key, 1, Probe}
    }
    return Grow{f.Key, need, Full}
}

func hardwareLooksMissing(f PoolFacts, p PoolConfig, now time.Time) (bool, time.Time) {
    capFails := closed runs where Status in {Failed, TimedOut} and FailType in {ServerNotAvailableError, ServerReservedError}
    lastFail := latest Closed among capFails
    if none or now - lastFail > p.FailureMemory:   return false
    // PROOF that hardware exists again = a run that STARTED AFTER the last capacity failure closed and
    //   - Completed, or
    //   - is still Running 'drawGrace' (2 min) after it started (a "no server" failure comes in seconds)
    proof := exists run r in f.Open ∪ f.Closed with r.Started > lastFail and
             (r.Status == Completed || (r.Status == Running && now - r.Started >= p.DrawGrace))
    if proof:  return false
    // other failure types: only when repeated and nothing worked since
    ...otherFails := Failed runs with other types after the last Completed; if count >= 3 -> failing too
    return true, lastFail
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
Counting open runs, no memory needed:
  describe  add-server-to-mce_<mce>_<infraenv>_<1..maxSlots>   (parallelism ~10, per-call deadline)
  NotFound (errors.As serviceerror.NotFound) = free slot. Any other error = the pool is BLOCKED this pass.
  Describe(workflowId) with no run id returns the LATEST run and is strongly consistent, unlike a List.
  For slots closed Failed within failureMemory: read the failure type from the CLOSE EVENT
    (GetWorkflowHistory, close-event filter), cached by (workflowId, runId).
  Throttle: a pool is swept at most once per 10 s; Recheck reuses a sweep younger than 30 s.
  Closed runs vanish after the Temporal namespace retention: retention must be well above
  max(probeInterval, failureMemory).

After a POST:
  202: the slots whose status is "started" are ordered. "failed" items are NOT assumed absent
       (a lost Temporal ack looks like a failure): the next sweep decides.
       started < requested  => metric grow_short.
  timeout / connection error / 5xx: ambiguous => write annotation  blocked-until = now + ambiguousWait (5 min)
       on the InfraEnv; do not retry the same request.
  422: our bug => log error, metric, same block.

Pollers: DescribeTaskQueue (plain, not Enhanced) for the hub activity queue, the install workflow queue and the
segment-lifecycle queue. Detection lags ~5 min after a worker dies.
```

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
  lowPct: 15
  highPct: 30
  minFree: 2
  minBand: 1
  maxOpenRunsPerPool: 15
  probeInterval: 20m                # server-scan refreshes about every 15 min
  drawGrace: 2m                     # a run still going this long after it started has found a machine
  failureMemory: 6h
  settlingMax: 60m                  # matches the install workflow's one-hour agent wait
  dryRunGrow: false                 # log "WOULD INSTALL" instead of ordering
  paused: false

# ---- pools: OPT-IN, keyed by InfraEnv name; only the fields written here differ ----
pools:
  "32-128-zone-1": { minFree: 5, maxOpenRunsPerPool: 8 }
  "16-64-zone-2":  { paused: true }
  "8-32-zone-1":   { lowPct: 25, dryRunGrow: true }
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

**Validation, after the merge, per pool.** `lowPct`/`highPct` >= 0 and `lowPct <= highPct`; `minFree` >= 0;
`minBand` >= 1; `maxOpenRunsPerPool` between 1 and `maxSlots`; durations > 0; `drawGrace` < `probeInterval`.
A pool that fails validation is **blocked with the reason named** (other pools are unaffected). A bad
ConfigMap update as a whole (unparseable, unknown keys) is rejected and the last good one kept.
An unknown key under a pool is an error, so a typo such as `minfree` is caught instead of ignored.

`minFree` is the operator's to size: the largest scale request to absorb without a wait, plus the surge of
any pool still using `upgradeType: Replace`. A pool can also be paused with an annotation on the InfraEnv,
with no ConfigMap edit.

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
| `Surplus = min(total - target, claimable)`; no WouldRemove while any Agent is settling or a run is open | misleading shrink log |
| Zero pollers on any of the three queues, or a poller error, blocks the pool | runs that never start |
| Hub-scoped findings never block another pool | one stray object stalling the hub |
| Unmanaged, paused, early-bound or deleting pool never grows | opt-in only |
| `ids` builder output equals the Python builder's for a table of cases | contract with the route |
| One frozen config and one clock per reconcile | mixed inputs |
| Adding one claimable Agent never increases the ordered count (property test) | monotonicity |

## Test table for Decide

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

---

# Part 3 — Flaws found and how they are handled

## A. Flaws from the reviews

| # | Flaw | Example | How it is handled |
|---|---|---|---|
| 1 | Duplicate orders (additive POST, visibility lag, lost reply) | order 12, reply lost, next pass orders 12 again → 34 for a target of 22 | The sweep Describes the fixed slot ids (consistent), subtracts open runs, blocks the pool 5 min after an unclear POST (stored on the InfraEnv). Rare residual race accepted |
| 2 | No leader election | two replicas each order 12 | Lease election; one replica. No fencing: accepted |
| 3 | Stale or partial reads | cached Agents empty → orders again | Errors block the pool; `Recheck` re-reads uncached and re-validates before ordering |
| 4 | `count` > 20 gives 422 forever | need 58 → 422 every pass | Plan caps each request at 20, waves for big deficits |
| 5 | Slots shared across hubs | other hub's runs counted as ours | MCE in the id (workflows PR #12) |
| 6 | Spare may not match the NodePool selector | red/blue example | Not real here: selectors are only the InfraEnv label; any other selector blocks that pool |
| 7 | Unlabeled Agents vanish | 4 of 12 uncounted → over-order | Warning and metric; never blocks another pool |
| 8 | Failed or hung runs | fast fails re-ordered every pass | Probe mode: 1 machine per 20 min, failure-class alerts, poller check, 2 h stuck alert |
| 9 | Candidate window at batch 20 | 20 unusable candidates | Open-run cap is load tuning only. Real fix is server-side |
| 10 | NodePool nil replicas, deleting, paused | nil pointer; phantom demand | Handled in Attribute; `pausedUntil` parsed |
| 11 | Zero-demand pools auto-seeded | 12 InfraEnvs × minFree 2 → 24 machines | Opt-in `pools` list |
| 12 | `maxFree` meaning | doc says ceiling, formula floors | Removed; `minFree` floors low, band width floors high |
| 13 | 202 does not mean started | all slots busy → started 0 | Only `started` slots counted; the sweep decides the rest |
| 14 | Early-bound InfraEnv | hosts bound at once | Validate blocks it |
| 15 | Hand installs invisible | operator installs 3 by name → 3 extra | Accepted; a per-pool pause switch |

## B. Flaws from the logic simulation

| # | Flaw | Example | Fix |
|---|---|---|---|
| L1 | Wrong recovery evidence for probe mode | success from before the failure flips to Full → burst of doomed runs; slot reuse hides evidence; recovery waits a whole install | Proof must have started after the last capacity failure and (completed or still running after 2 min). A probe that found a machine returns the pool to Full immediately |
| L2 | Released machines vanish while rebooting | scale 20→4→12 orders 7 extra | `settling` = any free, unbound, non-deleting, not-yet-claimable Agent, measured from its last state change, up to 60 min, then an alert |
| L3 | Orphan-bound Agents count as supply; shrink log overstates | 4 orphans hide a stall; "would remove 17" when 3 are free | `Surplus = min(total - target, claimable)`; no WouldRemove while any Agent settles; alert when assigned exceeds demand for hours. **Not fixed automatically** |
| L4 | Failure classes | batch shortage shows as `ServerReservedError`; one dead host throttles the pool | `ServerNotAvailableError` and `ServerReservedError` are the capacity class and the only ones that probe; other failures probe only after 3 in a row |
| L5 | Stale `agentMachineRef` Agent in no bucket | runaway ordering | Counted as settling, then an alert |
| L6 | `Recheck` did not re-validate | worker dies just before the POST | Recheck re-runs Validate and Decide, orders at most the first amount |
| L7 | The 5-minute block lived in memory | restart forgets it | Stored on the InfraEnv as an annotation |
| L8 | Poller check missed the segment queue | runs hang at `resolving-vlan` | All three queues checked |
| L9 | Zero-width band, truncating rounding | demand 6: target 8 = ceiling 8 | `high >= low + minBand`; integer half-up `(pct×d+50)/100` |
| L10 | Double count while the Agent exists but the run is open | one pass of 1 too few | Accepted (seconds). Phase queries dropped: they removed almost nothing |

## C. Findings from the feasibility review

| Finding | Change |
|---|---|
| The Kubebuilder command needs `--external-api-path` / `--external-api-domain`, and the three generated reconcilers must be merged | Scaffold section updated |
| Lost lease = immediate exit, an in-flight POST may still land | Block stored on the InfraEnv; sweep on every start |
| Event-driven reconciles are not rate limited | Sweep at most once per 10 s per pool; Recheck reuses a fresh sweep |
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
| Config changes | the ConfigMap watch re-queues all pools; a bad update is rejected |
| Inventory runs dry in a batch | the runs that got a machine continue; the failed ones start probe mode |
| Hardware racked | next probe ≤ 20 min, then full orders at once. New hardware reaches server-scan on its vendor collection (~6 h) |
| All candidates dead for a pool | the run fails after ~hours with `AgentNeverAppearedError`; after 3 such failures the pool probes and alerts "investigate" |
| Machine installed but not yet approved | all Agents on this hub are auto-approved; settling covers the short gap |
| Closed runs expired from retention | probe evidence is lost; the pool returns to Full orders for one pass, then re-learns from the next failure |
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
| Cooldown | deferred; v1 only logs WouldRemove statelessly |
| Slot discovery | Describe sweep over fixed ids |
| Unclear POSTs | block 5 min (annotation), then sweep; rare race accepted |
| Double counting | all Running runs count; free machines count while settling (≤ 60 min) |
| Upgrade surge | operator sizes `minFree`; most upgrades are InPlace |
| Hand installs / legacy ids | not counted / ignored; per-pool pause switch |
| Workflow ids | `add-server-to-mce_<mce>_<infraenv>_<slot>` and `add-server-to-mce_<mce>_<server>` (PR #12) |
| Empty pool | probe of 1 machine every 20 minutes (server-scan state refreshes every ~15 min) |
| Failure classes | capacity class = `ServerNotAvailableError`, `ServerReservedError`; other failures probe after 3 in a row |
| No worker | zero pollers on any of the three queues => do not order |
| Stuck run | open longer than 2 h => alert only |

## H. Backlog
- **Cooldown before shrink (above-ceiling timer):** shrink only after a pool stays above its ceiling for 30 min; start in memory, move to an InfraEnv annotation when shrink really deletes.
- **Real shrink:** call remove-server for `Surplus` claimable victims, longest idle, re-read right before.
- **Idempotency key (or "ensure N open") on the install route:** removes the residual race.
- **Orphan-bound Agent detection** by NodePool, so the controller can exclude them from supply.
- **Verify the discovery-image wipe override:** ordered before registration, fails closed, every disk, present on VM and bare-metal InfraEnvs.
- **VM creation path** and which KubeVirt cluster a new VM is built on.
- **Bare-metal pools in scope?**
- **`InventoryPolicy` CRD** for per-pool config with status.
- **Fencing for leader election**, if the residual risk proves real.
