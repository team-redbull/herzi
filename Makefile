# Design Part 2, Framework: no CRD ownership; manifests generates RBAC only.
SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := build
export PATH := /home/linuxbrew/.linuxbrew/bin:$(PATH)
IMG ?= herzi:dev
VERSION ?= dev
COMMIT ?= unknown
CONTAINER_TOOL ?= docker
KUBECTL ?= kubectl
LOCALBIN ?= $(CURDIR)/bin
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.22.0
ENVTEST_VERSION ?= v0.25.0
ENVTEST_K8S_VERSION ?= 1.37.0
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION ?= v1.9.0
YEAR ?= $(shell date +%Y)
CONFIG ?= config/samples/herzi-config.yaml
GO_PACKAGES ?= ./cmd/... ./internal/...
# Exclude the controller's envtest suite from unit tests. No ./... across work packages.
UNIT_PACKAGES = $$(go list ./internal/... | sed '\|/internal/controller\(/\|$$\)|d')
INTEGRATION_PACKAGES ?= ./internal/controller/...
ignore-not-found ?= false

.PHONY: all help manifests generate fmt vet test test-integration lint lint-fix lint-config vuln ci build run docker-build docker-push docker-buildx build-installer deploy undeploy lint-manifests test-alerts check-rbac manifests-drift kustomize controller-gen envtest setup-envtest golangci-lint govulncheck
all: build
help: ## Display targets.
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "%-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
manifests: controller-gen ## Generate controller RBAC from WP6 markers; never edit role.yaml.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./internal/controller/..." output:rbac:artifacts:config=config/rbac
generate: controller-gen ## Generate DeepCopy methods if API objects are added.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./internal/..."
fmt: ## Format Go sources.
	go fmt $(GO_PACKAGES)
vet: ## Vet Go sources.
	go vet $(GO_PACKAGES)
test: ## Unit tests with race detection and coverage (no envtest).
	go test -race -covermode=atomic -coverprofile=cover.out $(UNIT_PACKAGES)
test-integration: setup-envtest ## Run WP6 envtest suite; see test/e2e/README.md for CRD path gate.
	KUBEBUILDER_ASSETS="$$("$(ENVTEST)" use -i $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test -race -count=1 -covermode=atomic -coverprofile=cover-integration.out $(INTEGRATION_PACKAGES)
lint: golangci-lint ## Run standard golangci-lint.
	"$(GOLANGCI_LINT)" run $(GO_PACKAGES)
lint-fix: golangci-lint ## Apply lint fixes.
	"$(GOLANGCI_LINT)" run --fix $(GO_PACKAGES)
lint-config: golangci-lint ## Validate lint configuration.
	"$(GOLANGCI_LINT)" config verify
vuln: govulncheck ## Check reachable Go vulnerabilities.
	"$(GOVULNCHECK)" $(GO_PACKAGES)
ci: lint vet test vuln ## Local CI checks.
build: ## Build the static controller binary.
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)' -o bin/manager ./cmd
run: ## Run against the current kubeconfig with the sample config.
	go run ./cmd --leader-elect=false --config="$(CONFIG)" --health-probe-bind-address=:8081 --metrics-bind-address=:8443
docker-build: ## Build an image without pushing.
	$(CONTAINER_TOOL) build --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" -t "$(IMG)" .
docker-push: ## Push an explicitly selected image.
	$(CONTAINER_TOOL) push "$(IMG)"
PLATFORMS ?= linux/amd64,linux/arm64
docker-buildx: ## Build and push multi-platform images using buildx.
	$(CONTAINER_TOOL) buildx build --platform="$(PLATFORMS)" --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --push -t "$(IMG)" .
build-installer: manifests kustomize ## Render an installation bundle (no CRDs).
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller="$(IMG)"
	"$(KUSTOMIZE)" build config/default > dist/install.yaml
deploy: manifests kustomize ## Deploy to the current kubeconfig context after the WP6 RBAC gate.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller="$(IMG)"
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -
undeploy: kustomize ## Delete the installation, including its namespace; back up secrets first.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -
lint-manifests: ## Render all trees; validate known Kubernetes schemas when kubeconform exists.
	@for tree in default manager rbac samples prometheus network-policy overlays/tls overlays/mtls overlays/example; do \
	  output=$$(mktemp); \
	  "$(KUBECTL)" kustomize config/$$tree > "$$output"; \
	  if command -v kubeconform >/dev/null 2>&1; then \
	    kubeconform -strict -summary -skip ServiceMonitor,PrometheusRule "$$output"; \
	  else echo "kubeconform unavailable: rendered $$tree only"; fi; \
	  rm -f "$$output"; \
	done

test-alerts: ## Unit-test the shipped alert rules with promtool.
	./hack/test-alerts.sh

check-rbac: ## Fail unless the default install grants the controller what it reads and patches.
	@out=$$(mktemp); "$(KUBECTL)" kustomize config/default > "$$out"; status=0; \
	for need in 'name: manager-role' 'name: manager-rolebinding' '- agents' '- infraenvs' '- nodepools' 'namespace: herzi-system'; do \
	  grep -q -- "$$need" "$$out" || { echo "check-rbac: rendered config/default lacks '$$need'"; status=1; }; \
	done; \
	grep -q 'kind: ClusterRole$$' "$$out" || { echo "check-rbac: no ClusterRole rendered"; status=1; }; \
	rm -f "$$out"; exit $$status

manifests-drift: manifests ## Fail when the committed role.yaml differs from what the markers generate.
	@git diff --exit-code -- config/rbac/role.yaml || { echo "manifests-drift: run 'make manifests' and commit config/rbac/role.yaml"; exit 1; }

$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"
kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): | $(LOCALBIN)
	GOBIN="$(LOCALBIN)" go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): | $(LOCALBIN)
	GOBIN="$(LOCALBIN)" go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)
envtest: $(ENVTEST)
$(ENVTEST): | $(LOCALBIN)
	GOBIN="$(LOCALBIN)" go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)
setup-envtest: envtest ## Download a pinned envtest control plane into bin/.
	"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): | $(LOCALBIN)
	GOBIN="$(LOCALBIN)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
govulncheck: $(GOVULNCHECK)
$(GOVULNCHECK): | $(LOCALBIN)
	GOBIN="$(LOCALBIN)" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
