# RBAC

The controller ClusterRole is **generated** from the `+kubebuilder:rbac` markers in
`internal/controller` by `make manifests` (`hack/gen-rbac.sh`) into
`charts/herzi/files/rbac-rules.yaml`; never edit the generated file by hand.
The chart renders those rules in `herzi-manager` and binds the release ServiceAccount
(`herzi` for release `herzi`, unless overridden).

Two CI guards protect this contract:

- `make manifests-drift` regenerates the file and checks for committed-file drift.
- `make helm-test` asserts that rendered manager rules match the generated file,
  contain no create/update/delete/deletecollection verbs and contain no wildcards.

These restrictions apply to the controller ClusterRole; leader election and metrics
authentication need the write permissions described below. `rbac.create: false`
suppresses **all** chart roles and bindings; provision replacements externally.
ServiceAccount creation is controlled separately by `serviceAccount.create` and `name`.

## What the controller is granted

| API group | Resource | Verbs | Why |
|---|---|---|---|
| agent-install.openshift.io | agents | get, list, watch | supply (the Agent informer, plus the uncached recheck) |
| agent-install.openshift.io | infraenvs | get, list, watch, patch | the pools; patch writes only the `herzi.redbull.io/*` annotations (block marker, pause, durable state) |
| hypershift.openshift.io | nodepools | get, list, watch | demand, across all tenant namespaces |

The namespaced `herzi-leader-election` Role grants get/list/watch/create/update/patch/delete
on leases and create/patch on core events in the release namespace. Its RoleBinding
selects the controller ServiceAccount. The `herzi-metrics-auth` ClusterRole and binding
allow that account to create TokenReviews and SubjectAccessReviews. The
`herzi-metrics-reader` ClusterRole grants only GET `/metrics`; its binding is rendered
only when `rbac.metricsReader.subjects` is nonempty. Add the Prometheus ServiceAccount
there; never grant anonymous access. Names derive from the release name.

Config is a mounted file that herzi polls, so **no ConfigMap permission is needed**. No Agent approval,
status, finalizer, delete, NodePool write or CRD permission exists.

## Known breadth

The generated role is a **ClusterRole**. NodePools genuinely need a cluster-wide read, but Agents and InfraEnvs
live in one namespace (`agentNamespace`), so reading them and patching InfraEnvs cluster-wide is broader than
necessary. Kubernetes RBAC also cannot restrict an InfraEnv patch to one annotation key. A site that wants the
tighter shape can replace the Agent/InfraEnv rules with a namespaced Role and RoleBinding in `agentNamespace`
(keep the NodePool ClusterRole) using site-managed RBAC and `rbac.create: false` (also provide leader-election and metrics roles). It is not the default because the agent namespace is
configurable and a missing binding is exactly the failure above.

## Confirm on the real hub

```bash
SA=system:serviceaccount:herzi-system:herzi
kubectl auth can-i list agents.agent-install.openshift.io -n inventory --as=$SA
kubectl auth can-i watch infraenvs.agent-install.openshift.io -n inventory --as=$SA
kubectl auth can-i patch infraenvs.agent-install.openshift.io -n inventory --as=$SA
kubectl auth can-i list nodepools.hypershift.openshift.io --all-namespaces --as=$SA
kubectl auth can-i create leases.coordination.k8s.io -n herzi-system --as=$SA
kubectl auth can-i delete agents.agent-install.openshift.io -n inventory --as=$SA
```

All but the last must say `yes`; deleting Agents must say `no`. Review the rendered roles after every marker change.
