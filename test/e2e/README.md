# End-to-end validation

Requires Docker and an isolated Kind cluster; never use a dev/production cluster.
The CRD-owning scaffold suite and automatic cert-manager installation were removed.
No executable e2e suite is delivered yet. Future tests must deploy herzi with the
external CRD fixtures, mock workflows/Temporal endpoints and provisioned TLS Secrets;
cover grow, probe recovery, blocked/paused/unmanaged pools, strict config reload,
secure metrics, lease loss/restart and persisted ambiguity blocks, and upgrade
without overlapping active Pods. Real hub checks remain in the operating guide.

Envtest uses `test/crds` (not real operator CRDs); WP6 must set its suite's
`CRDDirectoryPaths` to that directory and `ErrorIfCRDPathMissing: true`.
The current read-only suite still points at the removed scaffold CRD directory.

Scaffold extension markers retained for tooling:

```go
// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness
// +kubebuilder:scaffold:e2e-webhooks-checks
```
