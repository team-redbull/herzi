# Kube implementation notes
These are implementation decisions moved out of the code.

- InfraEnv, Agent, NodePool: Small views over unstructured objects avoid the large dependency and binary-size cost of importing external typed API modules for a few fields; keep raw field-path knowledge in this package.
- reader: Missing fields retain zero values because Kubernetes admission checks required fields; malformed fields preserve the first error and its full path for diagnostics.
- reader.objectAt, reader.slice: Inspect containers without deep-copying them because NestedMap and NestedSlice can panic on non-JSON Go values; malformed input must produce parser errors instead.
- ParseAgent: The newest condition transition wins regardless of condition order or connection status, even when older than creation time; creation time is only the fallback when no transition exists.
- Selector: matchExpressions are parsed in full (key, operator, values) so the pool rule can accept exactly the ever-installed exclusion and the retirer can evaluate a selector with metav1.LabelSelectorAsSelector, the same way CAPI does; an empty selector matches every Agent.
- LabelBMH: The `agent-install.openshift.io/bmh` label is the only server identity sent to remove-server-from-mce. The Agent name is a host UUID and a hostname may name another server, so a missing label means no removal.
