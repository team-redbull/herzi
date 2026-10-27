> **Superseded historical input.** This original design records the former low/high buffers, removal and cooldown proposals. None are current instructions or future commitments. The [current add-only design](herzi-design.md) is authoritative; this file is retained as design history.

# Inventory Controller

**What:** Keeps the pool of ready worker nodes matched to what tenant clusters are asking for.
**Where:** One instance on each Customer MCE hub.

---

## Terms

| Term | Meaning |
|---|---|
| **NodePool** | HyperShift resource saying how many workers a tenant cluster wants in one zone |
| **Agent** | A VM that has booted, registered with MCE, and is ready to become a worker. Either spare or assigned. |
| **InfraEnv** | What agents register into. One per node type per zone, shared by all tenants. |
| **Pool** | One InfraEnv's worth of inventory, keyed by hub, node type and zone. The unit this controller works in. |

---

## What it does

NodePools say how many nodes tenants want. Agents are the nodes that exist. Nothing keeps the
two matched today, so inventory is built by hand.

The controller watches demand on its own hub, compares it to a target, and asks Temporal to
build or remove VMs to close the gap.

It does not know or care *why* demand changed. A new tenant cluster, a scale-up, a node
replaced during an upgrade, and a cluster deletion all look identical to it: the pool is the
wrong size. One code path handles all of them.

---

## The loop

Every pass, default two minutes, for each pool on the hub:

1. **Read demand.** Sum the desired replicas of every NodePool drawing on this pool.
2. **Read supply.** Count agents, both spare and assigned.
3. **Read what is already building.** Ask Temporal which runs for this pool are still open.
4. **Compute the target**, demand plus a buffer, then subtract everything in steps 2 and 3
   to get what is still needed.
5. **Nothing needed? Stay silent.** Otherwise ask for that many, and move on.

The controller never blocks and never remembers what it asked for. Every pass starts from
scratch, which is why it can be restarted at any moment and why a missed pass costs one
cycle rather than leaving anything inconsistent.

---

## The formula

Read from the cluster, per pool:

| Name | Meaning |
|---|---|
| `demand` | Sum of desired replicas across the NodePools in this pool |
| `assigned` | Agents assigned to a NodePool |
| `spare` | Agents registered and unassigned. A VM mid-scrub must not appear here. |
| `running` | Open runs for this pool, from Temporal. Machines being built are not Agents yet. |

Then:

```
low     = max(minFree, round(lowPct  × demand))
high    = max(maxFree, round(highPct × demand))

target  = demand + low
ceiling = demand + high

total   = assigned + spare
needed  = target - (total + running)

grow    if  needed > 0                          ->  ask for `needed`, immediately
hold    if  needed <= 0 and total <= ceiling    ->  say nothing at all
shrink  if  total > ceiling, held for cooldown  ->  ask for the surplus to go
```

Everything is in the same unit: whole agents in the pool.

**Subtracting `running` is not optional.** The route that starts work is **additive**: it
starts the number of new runs you ask for. Ask for two every two minutes through a
twenty-minute build and you get twenty machines for a shortfall of two. The controller is
the only thing positioned to prevent that, because it is the one deciding whether to speak
at all.

**In-flight work is discoverable, which is why there is no parent workflow.** Runs for a pool
are named by slot, `install-server-<infraenv>_1` upward, so "how many are building" is a
question anyone can ask Temporal. An earlier design had a parent workflow holding that count
in memory, where only it could see it and where a crash lost it. Naming beats bookkeeping.

**Only the ask is a delta. The thinking is absolute.** The target is computed from demand and
configuration, converted to a count at the last moment, immediately after a live read. That
matters because the target never touches the agent count, so a stale reading cannot corrupt
it: it can cause an unnecessary ask or a missed one, but not a wrong number. A delta computed
minutes earlier and buffered would carry the error straight into machines built.

**Attributing demand to a pool is the part that needs care.** A NodePool claims agents
through its label selector, so the only honest test of whether its replicas are demand on
this pool is to match that selector against the labels this InfraEnv stamps. A selector that
resolves to more than one InfraEnv is a configuration error to report, not something to
apportion: guessing a split produces a plausible wrong number, which is worse than refusing.
Today's committed manifests select on zone alone, so this does not work yet.

### Worked example

One zone, node type 32/128, three tenant clusters wanting twenty nodes between them.
Watermarks of ten and twenty percent.

```
demand = 20
low    = 2      ->  target  = 22
high   = 4      ->  ceiling = 24

total = 21   below target          ->  order 1
total = 23   inside the band       ->  do nothing
total = 26   above ceiling, held   ->  remove 4, back down to 22
```

### Three things that are easy to get wrong

- **Size the pool off demand, not off how many agents are currently assigned.** The two agree
  only while every NodePool is full. The moment a NodePool is short, the assigned count hides
  the shortfall and you under-order. Demand always shows it.
- **A VM mid-scrub must not show up as spare.** On scale-down the VM returns to the pool rather
  than being destroyed, which is the point of the band. The controller does not track it while
  its disk is being reset. It simply reappears as a spare agent on a later pass and is counted
  then. That only works if a released VM stops being a registered agent until the reset
  completes. If it lingers as an unassigned agent mid-scrub, the controller will hand tenant
  data to the next cluster that claims it.
- **`minFree` is not optional.** A brand-new node type has `demand = 0`, and any percentage of
  zero is zero, so the pool is never seeded and the first tenant to want that type waits out a
  full VM build. Small pools have the same problem: ten percent of three rounds to nothing.
  Size the floor off the largest single scale request you want to absorb without a wait. The
  percentage protects large pools, the floor protects small ones.

---

## Talking to Temporal

No parent workflow. One run per machine, in numbered slots.

| | |
|---|---|
| Read in-flight | Temporal: open runs matching `install-server-<infraenv>_*` |
| Ask for more | `POST /workflows/server-lifecycle/install-server/bulk` |
| Body | `{ infra_env, mce_cluster, count: needed }` |
| Reply | 202 with a per-slot report: started, already running, or failed |

Four rules:

- **`count` is additive.** It starts that many **new** runs. It is not a target and the route
  will not reconcile to one. Subtracting `running` before asking is the controller's job, and
  nothing downstream will catch the mistake.
- **Slots deduplicate, ids do not need to.** The route walks slots from `_1` upward and steps
  over any whose run is still going, because Temporal allows one running workflow per id. A
  finished slot is free again.
- **Nothing owns convergence, deliberately.** A failed slot leaves the gap open and the next
  pass sees it. The evaluation cycle **is** the retry, which is why there is no pool-level
  attempt counter anywhere.
- **Both directions still belong together.** Growing and shrinking one pool must not run at
  once, or a build races a teardown over the same machines.

Two runs cannot take one machine. Each takes the inventory's reservation lock before writing
anything, granted by compare-and-set and refused to any other run id, and a run that loses
skips that candidate. That lock is what made serialising a pool unnecessary in the first
place.

A Temporal task queue routes work to workers. It does not deduplicate, and it holds no logic.

---

## Config

A ConfigMap on the hub, read at startup and re-read on change.

```yaml
defaults:
  lowPct:   15      # buffer, as a percentage of demand
  highPct:  30      # above this the pool is too big
  minFree:   2      # buffer floor, in whole agents
  maxFree:  10      # buffer ceiling, in whole agents
  cooldown: 30m     # how long a surplus must persist before shrinking
pools:
  "32-128/zone-1":
    minFree: 5
```

The values above are placeholders. See the last section.

---

## Why it is built this way

| Decision | Reason |
|---|---|
| One controller per MCE hub | Blast radius is one hub. In-cluster credentials only. No central service to scale. |
| Works per pool, not per tenant | The InfraEnv is already shared per node type and zone. Bulk requests fall out for free. |
| Ignores what caused the demand | Modelled on Kubernetes. One code path instead of one per cause. |
| Reads state, never events | Restart recovery is a single pass. Nothing to replay, nothing to miss. |
| Thinks in targets, asks in counts | The target comes from demand and configuration, so a stale agent reading cannot corrupt it. It becomes a count only at the last moment, immediately after a live read, because the route that starts work is additive. |
| One run per machine, in numbered slots | Deduplication is one running workflow per slot id. In-flight is then a question anyone can ask Temporal, rather than a number a parent holds in memory and loses on a crash. |
| Target is total pool size, measured off demand | Sizing off assigned agents hides the shortfall whenever a NodePool is not full. |
| Percentage with an absolute floor | The percentage scales with the pool. The floor keeps small and empty pools usable. |
| Grow now, shrink slowly | Shrinking late wastes a little capacity. Growing late stalls a tenant. |
| Grow and shrink never overlap on one pool | A build racing a teardown over the same machines has no good outcome. |
| Never in the tenant's request path | A controller outage slows replenishment. It does not fail tenant requests. |

---

## Still to decide

- **Threshold values.** Cannot be set until we measure how long one VM takes to build, because
  `minFree` has to cover the demand that arrives during a single build.
- **Release behaviour.** Does a released VM stop being a registered agent while its disk is
  reset? The arithmetic above is only safe if it does.
- **Bare-metal pools.** In scope, or VM pools only?
- **Who grows the fleet.** When a hub or KV cluster fills up, something has to provision a new
  one. Not this controller. It only sees its own hub.
