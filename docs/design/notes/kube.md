# Kube implementation notes
These are implementation decisions moved out of the code.

- InfraEnv, Agent, NodePool: Small views over unstructured objects avoid the large dependency and binary-size cost of importing external typed API modules for a few fields; keep raw field-path knowledge in this package.
- reader: Missing fields retain zero values because Kubernetes admission checks required fields; malformed fields preserve the first error and its full path for diagnostics.
- reader.objectAt, reader.slice: Inspect containers without deep-copying them because NestedMap and NestedSlice can panic on non-JSON Go values; malformed input must produce parser errors instead.
- ParseAgent: The newest condition transition wins regardless of condition order or connection status, even when older than creation time; creation time is only the fallback when no transition exists.
