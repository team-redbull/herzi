# RBAC review contract

Design Part 2, Framework: “The controller owns no resource.” This table is the
minimum application permission contract for reviewing WP6's kubebuilder markers.
Generate `config/rbac/role.yaml` with `make manifests`; never hand-edit its rules.
The current file is the unmodified scaffold Pod-reader placeholder. It and its
binding are deliberately excluded from `config/rbac/kustomization.yaml` until
WP6 generates and reviews the real role. Add both resources there after that gate.
No controller operation is functional until this is completed.

| API group | Resource | Verbs | Scope / purpose |
|---|---|---|---|
| agent-install.openshift.io | infraenvs | get, list, watch | agentNamespace; pool observation |
| agent-install.openshift.io | agents | get, list, watch | agentNamespace; inventory observation |
| hypershift.openshift.io | nodepools | get, list, watch | all tenant namespaces; demand |
| core (`""`) | configmaps | get, list, watch | controller namespace; herzi-config reload |
| agent-install.openshift.io | infraenvs | get, patch | agentNamespace; blocked-until annotation |
| coordination.k8s.io | leases | get, create, update | controller namespace; manager election Lease |
| core (`""`) | events | create | controller namespace; event recorder |

InfraEnv patch is the only inventory mutation. No Agent approval writes, status,
finalizer, delete, NodePool mutation or CRD permissions are required. The plan's
broader get/update/patch prose permits update, but this package follows the
requested minimal patch-only annotation contract; WP6 should use Patch. If its
recorder uses events.k8s.io instead of core events, use that group deliberately;
event aggregation can require patch and must be reviewed before enabling it.

Keep the scaffold leader-election Role and binding as supplied for compatibility.
They grant a superset (ConfigMap access, Lease list/watch/patch/delete, events
patch). The metrics-auth ClusterRole is also retained: create tokenreviews and
subjectaccessreviews authenticates/authorizes HTTPS scrapes. `metrics-reader`
grants GET `/metrics`; bind it to the Prometheus ServiceAccount, never anonymous
users. These are separate from the generated controller role.

ConfigMap watches must use a metadata.name field selector when resourceNames
restrictions are added: Kubernetes cannot authorize unrestricted list/watch
against only one resourceName. Namespace-restrict the ConfigMap cache. A generated
ClusterRole bound cluster-wide is broader than the namespace constraints above;
use scoped Roles/RoleBindings in a site overlay where possible. NodePools need
cluster-wide reads unless every tenant namespace is enumerated. Kubernetes RBAC
cannot restrict an InfraEnv patch to one annotation key.

Confirm on the real hub (examples use the supplied ServiceAccount):

```bash
kubectl auth can-i list agents.agent-install.openshift.io -n inventory --as=system:serviceaccount:herzi-system:controller-manager
kubectl auth can-i watch infraenvs.agent-install.openshift.io -n inventory --as=system:serviceaccount:herzi-system:controller-manager
kubectl auth can-i patch infraenvs.agent-install.openshift.io -n inventory --as=system:serviceaccount:herzi-system:controller-manager
kubectl auth can-i list nodepools.hypershift.openshift.io --all-namespaces --as=system:serviceaccount:herzi-system:controller-manager
kubectl auth can-i watch configmaps -n herzi-system --as=system:serviceaccount:herzi-system:controller-manager
kubectl auth can-i create leases.coordination.k8s.io -n herzi-system --as=system:serviceaccount:herzi-system:controller-manager
kubectl auth can-i delete agents.agent-install.openshift.io -n inventory --as=system:serviceaccount:herzi-system:controller-manager
```

After integration all reads/patch/election checks should say yes; Agent deletion
must say no. Repeat for get/update of the actual Lease and create events. Review
the rendered roles after every marker change, including unexpected Pod access.
