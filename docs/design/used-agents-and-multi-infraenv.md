# Used Agents, and NodePools that draw from several InfraEnvs

**Status: Proposed, 2026-11-19. Not implemented.** Nothing here changes herzi until the owner accepts it.

| Part | What | Reopens |
|---|---|---|
| [A](#part-a--a-collector-for-used-agents) | herzi labels every Agent that is installed into a cluster, and takes a labelled Agent out of the MCE once it is free | The hard rule "the controller only ADDS servers" (and AD26): herzi would remove **used** servers, through the workflows. It still never removes a fresh one, and never shrinks a pool to its target |
| [B](#part-b--demand-of-a-nodepool-with-several-infraenvs) | herzi counts the demand of a NodePool that draws from several InfraEnvs, and orders into the cheapest one that can still grow | "A NodePool selects ONLY by the InfraEnv label" ([PLAN.md](../PLAN.md), design Part 3 G) |

**Order:** B.1 (accepting the new selectors) ships first. Today a `DoesNotExist` expression blocks the pool, and the
`In` selector over InfraEnvs loses its demand. Part A puts a `DoesNotExist` on every NodePool.

---

# Part A — A collector for used Agents

## A.1 Why

A server that has served a cluster is never handed to another one as it is (AD23 in the platform architecture). Its
disks hold the last tenant's data. `uninstall-server-from-hc` already ends in `remove-server-from-mce`. A scale-down
(update-nodepool) leaves the freed server in the MCE as a free Agent. So do a Machine replaced by a repair, a deleted
NodePool or hosted cluster, and a `Replace` rollout. Any NodePool may then take it again. herzi sees every Agent
already, so it can find those servers and have them removed.

## A.2 The label

herzi uses **`redbull.io/claimed: "true"`**, the label the architecture's naming draft defines for this (section 5).
herzi has no earlier decision of its own.

- **herzi labels every bound Agent** (`spec.clusterDeploymentName` set) that lacks the label, on the first pass that
  sees it bound. The write is a JSON merge patch of that one key, with no `resourceVersion`. A failed write is
  retried on the next pass.
- **This is safe for the running node.** The agent provider never re-checks a bound Agent: a Ready AgentMachine
  returns early (cluster-api-provider-agent b86a062).
- **Every NodePool excludes the label.** The operator adds `redbull.io/claimed` to Wingman's
  `DAY1_EXCLUDED_AGENT_LABELS`. Wingman writes it on every nodepool of each cluster file, one merge request per
  cluster, and the chart renders it. So a freed used Agent is never bound again before herzi removes it.
- **The label stays on the Agent** while the Agent unbinds, reboots into discovery and settles. A re-installed server
  registers a new Agent, which has no label.

## A.3 The collector

On every pass, for each Agent of a managed pool:

| Agent | herzi |
|---|---|
| bound | labels it if needed; it counts as `assigned` |
| free, no label | keeps it: a fresh Agent, counted as claimable or settling as today |
| free, labelled, an override present (A.6) | keeps it, not counted as supply |
| free, labelled, still letting go (`agentMachineRef` label, unbinding or reclaiming, last state change younger than `collectAfter`) | waits; not supply |
| free, labelled, settled | **collects it**: `POST /workflows/server-lifecycle/remove-server-from-mce` with `{server_name, mce_cluster, namespace}` |

A labelled free Agent is never supply: no NodePool can take it. It becomes a new term, `used`, reported and never
subtracted from need (naming draft rule 8). The pool's demand has already fallen, or the NodePool has taken a fresh
Agent in its place, so the ordering formula is unchanged.

**Why the workflow, not a delete.** The host is still running the discovery image. If only the Agent is deleted, the
host registers again within a minute, as a fresh Agent with no label: the used server comes back as clean inventory.
remove-server-from-mce powers the host off through its BareMetalHost first. It then deletes the NMStateConfig, the
BMH and the Agent last, and clears the host's PXE entry, so nothing can bring the Agent back. It also refuses a bound
Agent (`AgentBoundToClusterError`) and checks again before each delete. herzi therefore needs **no delete verb**.

**Server name:** the Agent's `agent-install.openshift.io/bmh` label, else its hostname, as update-nodepool reads it.
If neither is present, or two Agents give the same name, herzi skips the Agent and warns.

## A.4 Never fight a run

The workflow ids are deterministic. Before each collection herzi describes, for that server:

| Run | If Running |
|---|---|
| `uninstall-server-from-hc-<server>` | skip: the uninstall removes the server itself (its child is `remove-server-from-mce-<server>`) |
| `remove-server-from-mce-<server>` | skip: already being removed |
| `add-server-to-mce-<mce>_<server>` | skip: a named install of this server is in flight |

A Describe error skips the Agent for this pass (fail closed). The POST is safe to repeat: a second start of the same
id is a 409, not a second removal. An unclear outcome is therefore settled by the next Describe, with no block
marker. Locks are not herzi's business: remove-server-from-mce takes none and releases none, and a lock left by an
earlier holder runs to its TTL. A failed removal (`HostPowerOffTimeoutError`, `ServerStillPresentError`) is tried
again after `probeInterval` and alerts after three failures.

## A.5 Missed labels

| Case | How herzi recognises it |
|---|---|
| Bound while herzi runs | labelled on first sight. A bound period lasts hours or days |
| Bound and freed while herzi was down | caught while it unbinds. The `Bound` condition's reason is `Unbinding` or `UnbindingPendingUserAction`, `status.debugInfo.state` is `unbinding*` or `reclaiming*`, or `status.deprovision_info` is set (assisted-service records the previous cluster there at unbind). herzi labels it, then collects it |
| Freed before the label existed, or the whole unbind missed | **not recognised, so kept.** No assisted-service field remembers a binding once the Agent is `known-unbound`: `deprovision_info` is cleared after the spoke cleanup |

herzi never removes a server without evidence. At rollout, herzi's first pass labels every bound Agent before any
NodePool excludes the label (naming draft rule 9). An operator who knows that a free Agent was used labels it by
hand. A claim-time admission policy (the naming draft's Kyverno rule) would close the last gap (open question 2).

## A.6 Safety and operations

- **Dry run first:** `dryRunCollect: true` (pool setting; the code and chart default). herzi labels Agents and logs
  `Would collect <server>`, but sends nothing. A pool goes live with an explicit `dryRunCollect: false`, one pool at
  a time.
- **Bounded:** at most `maxOpenRemovalsPerPool` (default 3) removals open per pool, and a settled Agent waits
  `collectAfter` (default 10m) before collection.
- **Don't collect:** an Agent that carries the `ignoreLabel` key (default `herzi.redbull.io/ignore`) is never
  collected. A paused pool is labelled but never collected; an ignored pool is not read at all.
- **Fail closed:** a pool whose NodePools do not all exclude the label still collects, since a fast removal limits
  reuse. herzi raises a warning (`nodepool_without_claimed_exclusion`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `herzi_pool_used` | gauge | pool | free labelled Agents waiting for removal |
| `herzi_pool_oldest_used_seconds` | gauge | pool | age of the oldest; alert `HerziUsedAgentNotCollected` above 2h |
| `herzi_pool_removals_open` | gauge | pool | open remove-server-from-mce runs started for this pool |
| `herzi_agents_labelled_total` | counter | pool | `redbull.io/claimed` written |
| `herzi_removals_total` | counter | pool, result (`started`,`conflict`,`skipped_run`,`failed`,`dry_run`,`rejected`) | collection attempts |

Logs: `Labelled used Agent`, `Would collect`, `Collecting`, each with the Agent and the server. Events: one on the
Agent per label and per collection. These need create/patch on events in `agentNamespace` from a namespaced Role;
the manager ClusterRole keeps no create verb (`make helm-test`). RBAC: `patch` on Agents (labels); no delete.
Calls: the remove route, plus Describe on the three ids above (builders pinned to `shared/workflow_ids.py`).

## A.7 After removal

The server is off and out of the MCE: no BMH, no NMStateConfig, no Agent, no PXE entry. It does not register again.
server-scan sees it leave the MCE and offers it again. A later add-server-to-mce (herzi's pool order or a person)
installs it from scratch as a fresh Agent. Bare metal is reinstalled; a reinstall overwrites the install disk only (an
open item of the architecture). A VM is taken out of the MCE but not destroyed (open question 4).

## A.8 Worked example

```yaml
# 1. Bound: herzi adds the label on first sight
kind: Agent
metadata:
  namespace: inventory
  labels:
    infraenvs.agent-install.openshift.io: c200-metal-zone-1
    agent-install.openshift.io/bmh: ocp-c200-metal-abc123
    agentMachineRef: <agentmachine>
    redbull.io/claimed: "true"
spec:
  clusterDeploymentName: {name: <hosted cluster>, namespace: <its control plane namespace>}
---
# 2. Scale-down: the Machine is deleted, the Agent unbinds, reboots, settles. The label stays.
metadata:
  labels:
    infraenvs.agent-install.openshift.io: c200-metal-zone-1
    agent-install.openshift.io/bmh: ocp-c200-metal-abc123
    redbull.io/claimed: "true"
spec: {}                                   # no clusterDeploymentName
status:
  debugInfo: {state: known-unbound}
---
# Every NodePool, as the chart renders it: no Machine can take the Agent in 2
agentLabelSelector:
  matchLabels: {infraenvs.agent-install.openshift.io: c200-metal-zone-1}
  matchExpressions:
  - {key: redbull.io/claimed, operator: DoesNotExist}
```

```text
3. collectAfter later, no run open for the server:
   POST remove-server-from-mce {"server_name": "ocp-c200-metal-abc123", "mce_cluster": "<mce>", "namespace": "inventory"}
   -> remove-server-from-mce-ocp-c200-metal-abc123: power off, NMStateConfig, BMH, Agent deleted, PXE cleared
4. The pool's demand fell with the scale-down: herzi orders nothing back.
```

---

# Part B — Demand of a NodePool with several InfraEnvs

## B.1 Selectors

A nodepool may now draw from several InfraEnvs (Wingman 3.47.0, `additionalInfraEnvNames`). The chart renders
`matchExpressions: [{key: infraenvs.agent-install.openshift.io, operator: In, values: [...]}]`, and Part A adds a
`DoesNotExist`. Today herzi drops the first with "not tied to an InfraEnv" and blocks the pool for the second.

| Selector | Read as |
|---|---|
| the one InfraEnv `matchLabels` (as today) | group `{X}` |
| one `In` on the InfraEnv key, no InfraEnv `matchLabels` | group `{X, Y, ...}` |
| either, plus any `DoesNotExist` expressions | the same group. An Agent carrying a key that every NodePool of its InfraEnv excludes is not supply |
| anything else | problem, pool blocked (unchanged): herzi cannot tell which Agents match |

A group that names an ignored or unknown InfraEnv is skipped with a hub warning, as an unknown InfraEnv is today. A
NodePool event maps to every InfraEnv of its group; an Agent event maps to its own InfraEnv and to every InfraEnv
that shares a group with it.

## B.2 Whose demand is whose, and where herzi orders

```text
for each NodePool N with group G and replicas r:
  bound(N, I)  = Agents of I bound into N's control plane namespace (<HostedCluster ns>-<HostedCluster name>)
  demand(N, I) = bound(N, I)                     # where its nodes are
  held         = sum over G of bound(N, I)
  if held > r: take (held - r) off, best InfraEnv first      # a scale-down still releasing
  if held < r: give (r - held) to the InfraEnvs of G, worst first, each up to what it can still supply;
               what is left goes to the worst InfraEnv that can grow, else to the worst
demand(I) = sum over N of demand(N, I)
```

- **What an InfraEnv can still supply:** its fresh claimable Agents and open runs, plus what can still be added.
  Without new inputs, herzi knows "can still be added" only as yes or no: no while the pool is in probe mode
  (hardware missing), paused, in dry run or invalid. With a server-scan read (open question 7) it becomes a count. A
  count is used up NodePool by NodePool, in a fixed order (namespace/name), so two NodePools that share an InfraEnv
  never both count the same free Agent or the same available server.
- **No double counting:** every Agent is in one InfraEnv and one control plane. Every NodePool's `r` is split over its
  group, never copied: `sum over G of demand(N, I) = r`. So `demand(I)` equals `assigned(I)` when every NodePool is
  full, and `HerziAssignedExceedsDemand` keeps its meaning. A single-InfraEnv NodePool gives today's `r`.
- **One ambiguity:** NodePools of one hosted cluster share a control plane namespace. If two of them share an InfraEnv
  and one draws from several, herzi cannot tell their Agents apart. It adds a problem on the shared InfraEnvs (fail
  closed). Wingman can refuse that file instead.
- Decide, Plan, the formula and probe mode do not change; only the demand they receive does.

## B.3 Which InfraEnv is the worst

The goal is to order the worst (cheapest) eligible server and save the premium InfraEnvs. herzi has no notion of
"worst" today. The simple options:

| Option | How | For | Against |
|---|---|---|---|
| 1. Operator order in herzi's config | `pools.<name>.rank: <int>`, lower is worse; unranked InfraEnvs come last | one place, live reload, no write on someone else's object | per-hub config kept in step with the InfraEnvs by hand |
| 2. A rank on the InfraEnv | label `herzi.redbull.io/rank: "<int>"`, set where the InfraEnv is created | travels with the InfraEnv; other tools (Wingman) can read it | one more field on an object herzi does not own; needs its owner's process |

Without a rank, herzi has no preference: it splits the missing part evenly over the InfraEnvs that can grow. Either
option only decides **where herzi orders**. CAPI still takes the first free Agent of any listed InfraEnv, so a
premium spare can still be used, and the premium pool then rebuilds its buffer. This proposal deliberately adds no
steering around CAPI.

---

## Needs from others

| From | What | Status |
|---|---|---|
| hostedclusters-setup chart | `In` selector from `additionalInfraEnvNames`; excluded Agent labels rendered as `DoesNotExist` | drafted / in progress, not merged |
| Wingman | `additionalInfraEnvNames` (3.47.0, off by default) | shipped |
| Wingman | `DAY1_EXCLUDED_AGENT_LABELS` on every nodepool, one merge request per cluster | in progress |
| Operator | `redbull.io/claimed` in `DAY1_EXCLUDED_AGENT_LABELS` | after B.1 and herzi's first labelling pass |
| workflows | `remove-server-from-mce` route, deterministic ids, the bound-Agent refusal | on `main` and `feat/update-nodepool` |

To verify on a real hub: a bound Agent's `spec.clusterDeploymentName.namespace` is the control plane namespace; a
label added to a bound Agent leaves its node alone; assisted-service never removes a label it did not set; the
unbinding states and `deprovision_info` appear as described in A.5.

## Open questions

1. **Add-only.** Does the owner accept that herzi removes used servers, through remove-server-from-mce (an amendment
   to the hard rule and to AD26)?
2. **The last gap.** Should a claim-time admission policy (Kyverno) label Agents at bind, with herzi as the backstop?
3. **Scale-down.** update-nodepool documents that "the servers that leave stay in the MCE". Should it remove them
   itself (the AD23 assessment), with herzi as the backstop, or leave the removal to herzi?
4. **VMs.** remove-server-from-mce takes a VM out of the MCE but does not destroy it. Who does?
5. **Wipe.** A bare-metal reinstall overwrites the install disk only. Is that enough, or does removal need a wipe?
6. **Worst InfraEnv.** Option 1, option 2, or something else (server-scan's hardware data)?
7. **How many can still be added.** Should herzi read server-scan's available servers per InfraEnv (a new read-only
   port, adapter and fake), or keep the yes/no it infers from probe mode?
