# herzi - AI Agent Guide

## Project Structure

herzi observes external Agent, InfraEnv and NodePool APIs as unstructured objects;
it owns no CRDs or webhooks. The current layout is:

```text
cmd/main.go                         Manager entry, flags and wiring
internal/controller/*               Reconciliation, watches and RBAC markers
internal/config                     Controller configuration loader and live store
internal/pipeline/*                 Pure decision stages
charts/herzi/templates/*            Helm deployment templates
charts/herzi/values.yaml            Deployment defaults and configuration
charts/herzi/values.schema.json     Helm values validation
charts/herzi/files/rbac-rules.yaml  Generated RBAC (DO NOT EDIT)
charts/herzi/files/alerts.rules.yaml Native Prometheus rules
hack/chart/alerts.test.yaml promtool cases
hack/chart/*-values.yaml        Example Helm values
hack/                              Build and verification helpers
docs/design/notes/*.md             Package implementation decisions
Makefile                           Build/test/deploy commands
PROJECT                            Kubebuilder metadata (DO NOT EDIT manually)
```

The API/webhook guidance below applies only if those features are added through
the CLI in future; it does not describe current deployment requirements.

## Comments

Code carries only necessary comments: markers, the package line, contracts,
invariants with a one-line why, and nolint reasons. Decisions and rationale live
in docs; `docs/design/notes/<package>.md` holds implementation decisions moved
out of code. Add or update a note when a change introduces a decision worth
preserving; follow the [notes index](docs/design/notes/README.md).

## Critical Rules

### Never Edit These (Auto-Generated)
- `charts/herzi/files/rbac-rules.yaml` - from `make manifests`
- `**/zz_generated.*.go` - from `make generate`
- `PROJECT` - from `kubebuilder [OPTIONS]`

### Never Remove Scaffold Markers
Do NOT delete `// +kubebuilder:scaffold:*` comments. CLI injects code at these markers.

### Keep Project Structure
Do not move files around. The CLI expects files in specific locations.

### Always Use CLI Commands
Always use `kubebuilder create api` and `kubebuilder create webhook` to scaffold. Do NOT create files manually.

### E2E Tests Require an Isolated Kind Cluster
Future e2e coverage described in `test/e2e/README.md` must use an isolated environment.
Ensure you run them against a dedicated [Kind](https://kind.sigs.k8s.io/) cluster (not your “real” dev/prod cluster).

## After Making Changes

**After editing `*_types.go` or markers:**
```
make manifests  # Regenerate chart manager RBAC from markers (no owned CRDs)
make generate   # Regenerate DeepCopy methods
```

**After editing `*.go` files:**
```
make lint-fix   # Auto-fix code style
make test       # Run unit tests
```

## CLI Commands Cheat Sheet

### Create API (your own types)
```bash
kubebuilder create api --group <group> --version <version> --kind <Kind>
```

### Deploy Image Plugin (scaffold to deploy/manage ANY container image)

Generate a controller that deploys and manages a container image (nginx, redis, memcached, your app, etc.):

```bash
# Example: deploying memcached
kubebuilder create api --group example.com --version v1alpha1 --kind Memcached \
  --image=memcached:alpine \
  --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

Scaffolds good-practice code: reconciliation logic, status conditions, finalizers, RBAC. Use as a reference implementation.


### Create Webhooks
```bash
# Validation + defaulting
kubebuilder create webhook --group <group> --version <version> --kind <Kind> \
  --defaulting --programmatic-validation

# Conversion webhook (for multi-version APIs)
kubebuilder create webhook --group <group> --version v1 --kind <Kind> \
  --conversion --spoke v2
```

### Controller for Core Kubernetes Types
```bash
# Watch Pods
kubebuilder create api --group core --version v1 --kind Pod \
  --controller=true --resource=false

# Watch Deployments
kubebuilder create api --group apps --version v1 --kind Deployment \
  --controller=true --resource=false
```

### Controller for External Types (e.g., from other operators)

Watch resources from external APIs (cert-manager, Argo CD, Istio, etc.):

```bash
# Example: watching cert-manager Certificate resources
kubebuilder create api \
  --group cert-manager --version v1 --kind Certificate \
  --controller=true --resource=false \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

**Note:** Use `--external-api-module=<module>@<version>` only if you need a specific version. Otherwise, omit `@<version>` to use what's in go.mod.

### Webhook for External Types

```bash
# Example: validating external resources
kubebuilder create webhook \
  --group cert-manager --version v1 --kind Issuer \
  --defaulting \
  --external-api-path=github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1 \
  --external-api-domain=io \
  --external-api-module=github.com/cert-manager/cert-manager
```

## Testing & Development

```bash
make test              # Unit race tests; no envtest
make test-integration  # envtest: real Kubernetes API server and etcd
make ci                # lint, vet, test, vuln, helm-lint, helm-test, test-alerts, manifests-drift
make run               # Run locally (uses current kubeconfig context)
```

Unit tests use standard Go testing. The controller envtest suite uses Ginkgo + Gomega; check its `suite_test.go` for setup. CI runs integration and image builds separately from `make ci`.

## Deployment Workflow

```bash
# 1. Regenerate manifests
make manifests generate

# 2. Build & deploy
export IMG=registry.example.com/herzi:0.1.0
make docker-build docker-push IMG="$IMG" VERSION=0.1.0 COMMIT=unknown
make deploy VALUES=site-values.yaml IMG_REPO=registry.example.com/herzi IMG_TAG=0.1.0

# 3. Test
helm template herzi charts/herzi -f hack/chart/example-values.yaml

# 4. Debug
kubectl logs -n herzi-system deployment/herzi -c manager -f
```

### API Design

**Key markers for** `api/<version>/*_types.go`:

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// On fields:
// +kubebuilder:validation:Required
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:MaxLength=100
// +kubebuilder:validation:Pattern="^[a-z]+$"
// +kubebuilder:default="value"
```

- **Use** `metav1.Condition` for status (not custom string fields)
- **Use predefined types**: `metav1.Time` instead of `string` for dates
- **Follow K8s API conventions**: Standard field names (`spec`, `status`, `metadata`)

### Controller Design

**RBAC markers in** `internal/controller/*_controller.go`:

```go
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mygroup.example.com,resources=mykinds/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
```

**Implementation rules:**
- **Idempotent reconciliation**: Safe to run multiple times
- **Re-fetch before updates**: `r.Get(ctx, req.NamespacedName, obj)` before `r.Update` to avoid conflicts
- **Structured logging**: `log := log.FromContext(ctx); log.Info("msg", "key", val)`
- **Owner references**: Enable automatic garbage collection (`SetControllerReference`)
- **Watch secondary resources**: Use `.Owns()` or `.Watches()`, not just `RequeueAfter`
- **Finalizers**: Clean up external resources (buckets, VMs, DNS entries)

### Logging

**Follow Kubernetes logging message style guidelines:**

- Start from a capital letter
- Do not end the message with a period
- Active voice: subject present (`"Deployment could not create Pod"`) or omitted (`"Could not create Pod"`)
- Past tense: `"Could not delete Pod"` not `"Cannot delete Pod"`
- Specify object type: `"Deleted Pod"` not `"Deleted"`
- Balanced key-value pairs

```go
log.Info("Starting reconciliation")
log.Info("Created Deployment", "name", deploy.Name)
log.Error(err, "Failed to create Pod", "name", name)
```

**Reference:** https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines

### Webhooks
- **Create all types together**: `--defaulting --programmatic-validation --conversion`
- **When`--force`is used**: Backup custom logic first, then restore after scaffolding
- **For multi-version APIs**: Use hub-and-spoke pattern (`--conversion --spoke v2`)
  - Hub version: Usually oldest stable version (v1)
  - Spoke versions: Newer versions that convert to/from hub (v2, v3)
  - Example: `--group crew --version v1 --kind Captain --conversion --spoke v2` (v1 is hub, v2 is spoke)

### Learning from Examples

The **deploy-image plugin** scaffolds a complete controller following good practices. Use it as a reference implementation:

```bash
kubebuilder create api --group example --version v1alpha1 --kind MyApp \
  --image=<your-image> --plugins=deploy-image.go.kubebuilder.io/v1-alpha
```

Generated code includes: status conditions (`metav1.Condition`), finalizers, owner references, events, idempotent reconciliation.

## Distribution Options

### Helm Chart

The deployment contract is `charts/herzi/values.yaml`, `charts/herzi/values.schema.json`
and `docs/ops/operating.md`. The packaged chart contains exactly 19 files: metadata,
defaults, schema, `.helmignore`, 13 template/helper files and two rules files.
Examples and alert tests live in `hack/chart/` outside the package.
Copy the example values and replace hub identity, install endpoint, Temporal address,
image repository and pools. Pools start in dry run (`dryRunGrow` defaults to true); set it false per pool to go live. The
chart renders the ConfigMap and validates required values before installation.
It creates no Secrets, CRDs or Namespace templates. Helm can create the namespace;
uninstall leaves that namespace and external Secrets intact. Pool and live timing
edits reload without restarting the pod; startup-bound config changes alter the
pod checksum. Never broaden the generated manager role beyond read/InfraEnv patch
permissions without reviewing the controller contract.
Run `make helm-lint helm-test test-alerts` before shipping chart changes.
Install with `make deploy VALUES=site-values.yaml IMG_REPO=registry.example.com/herzi IMG_TAG=0.1.0`.
Package with `make helm-package`.

### Publish Container Image

```bash
export IMG=registry.example.com/herzi:0.1.0
make docker-build docker-push IMG=$IMG
```

## References

### Essential Reading
- **Kubebuilder Book**: https://book.kubebuilder.io (comprehensive guide)
- **controller-runtime FAQ**: https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md (common patterns and questions)
- **Good Practices**: https://book.kubebuilder.io/reference/good-practices.html (why reconciliation is idempotent, status conditions, etc.)
- **Logging Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md#message-style-guidelines (message style, verbosity levels)

### API Design & Implementation
- **API Conventions**: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- **Operator Pattern**: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
- **Markers Reference**: https://book.kubebuilder.io/reference/markers.html

### Tools & Libraries
- **controller-runtime**: https://github.com/kubernetes-sigs/controller-runtime
- **controller-tools**: https://github.com/kubernetes-sigs/controller-tools
- **Kubebuilder Repo**: https://github.com/kubernetes-sigs/kubebuilder
