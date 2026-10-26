# RBAC

herzi owns no resource, so its permissions are small and all but one are reads. The controller role is
**generated** from the `+kubebuilder:rbac` markers in `internal/controller` by `make manifests` into
`config/rbac/role.yaml`; never edit it by hand. `config/rbac/kustomization.yaml` includes it and its binding,
and two guards keep that true:

- `make check-rbac` fails unless the rendered default install contains the controller ClusterRole, its
  binding in `herzi-system`, and the `agents`, `infraenvs` and `nodepools` rules. Before it existed the role
  was missing from the install: the pod started, the informers got 403 and it crash-looped.
- `make manifests-drift` fails when the committed `role.yaml` differs from what the markers generate.

Both run in CI.

## What the controller is granted

| API group | Resource | Verbs | Why |
|---|---|---|---|
| agent-install.openshift.io | agents | get, list, watch | supply (the Agent informer, plus the uncached recheck) |
| agent-install.openshift.io | infraenvs | get, list, watch, patch | the pools; patch writes only the `herzi.redbull.io/*` annotations (block marker, pause, durable state) |
| hypershift.openshift.io | nodepools | get, list, watch | demand, across all tenant namespaces |

Leader election needs `leases` (and `events`) in the controller's own namespace; the scaffold's
`leader-election-role` supplies them. The metrics endpoint is authenticated with `tokenreviews` and
`subjectaccessreviews` (`metrics-auth-role`, bound to the controller's ServiceAccount); `metrics-reader` allows
`GET /metrics` and must be bound to the Prometheus ServiceAccount, never to anonymous users.

Config is a mounted file that herzi polls, so **no ConfigMap permission is needed**. No Agent approval,
status, finalizer, delete, NodePool write or CRD permission exists.

## Known breadth

The generated role is a **ClusterRole**. NodePools genuinely need a cluster-wide read, but Agents and InfraEnvs
live in one namespace (`agentNamespace`), so reading them and patching InfraEnvs cluster-wide is broader than
necessary. Kubernetes RBAC also cannot restrict an InfraEnv patch to one annotation key. A site that wants the
tighter shape can replace the Agent/InfraEnv rules with a namespaced Role and RoleBinding in `agentNamespace`
(keep the NodePool ClusterRole) in its own overlay. It is not the default because the agent namespace is
configurable and a missing binding is exactly the failure above.

## Confirm on the real hub

```bash
SA=system:serviceaccount:herzi-system:controller-manager
kubectl auth can-i list agents.agent-install.openshift.io -n inventory --as=$SA
kubectl auth can-i watch infraenvs.agent-install.openshift.io -n inventory --as=$SA
kubectl auth can-i patch infraenvs.agent-install.openshift.io -n inventory --as=$SA
kubectl auth can-i list nodepools.hypershift.openshift.io --all-namespaces --as=$SA
kubectl auth can-i create leases.coordination.k8s.io -n herzi-system --as=$SA
kubectl auth can-i delete agents.agent-install.openshift.io -n inventory --as=$SA
```

All but the last must say `yes`; deleting Agents must say `no`. Review the rendered roles after every marker change.
