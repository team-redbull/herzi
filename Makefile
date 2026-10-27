# Design Part 2, Framework: no CRD ownership; manifests generates RBAC only.
SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := build
export PATH := /home/linuxbrew/.linuxbrew/bin:$(PATH)
IMG ?= herzi:dev
VERSION ?= dev
COMMIT ?= unknown
CONTAINER_TOOL ?= docker
HELM ?= helm
NAMESPACE ?= herzi-system
VALUES ?=
IMG_REPO ?=
IMG_TAG ?=
LOCALBIN ?= $(CURDIR)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
CONTROLLER_TOOLS_VERSION ?= v0.22.0
ENVTEST_VERSION ?= v0.25.0
ENVTEST_K8S_VERSION ?= 1.37.0
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION ?= v1.9.0
YEAR ?= $(shell date +%Y)
CONFIG ?= /tmp/herzi-dev-config.yaml
GO_PACKAGES ?= ./cmd/... ./internal/...
# Exclude the controller's envtest suite from unit tests. No ./... across work packages.
UNIT_PACKAGES = $$(go list ./internal/... | sed '\|/internal/controller\(/\|$$\)|d')
INTEGRATION_PACKAGES ?= ./internal/controller/...

.PHONY: all help manifests generate fmt vet test test-integration lint lint-fix lint-config vuln ci build run docker-build docker-push docker-buildx deploy undeploy helm-lint helm-test helm-package test-alerts manifests-drift controller-gen envtest setup-envtest golangci-lint govulncheck
all: build
help: ## Display targets.
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "%-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
manifests: controller-gen ## Generate chart manager RBAC from controller markers.
	./hack/gen-rbac.sh
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
ci: lint vet test vuln helm-lint helm-test test-alerts manifests-drift ## Local CI checks.
build: ## Build the static controller binary.
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)' -o bin/manager ./cmd
run: ## Run against the current kubeconfig with the sample config.
	./hack/dev-config.sh > "$(CONFIG)"
	go run ./cmd --leader-elect=false --config="$(CONFIG)" --health-probe-bind-address=:8081 --metrics-bind-address=:8443
docker-build: ## Build an image without pushing.
	$(CONTAINER_TOOL) build --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" -t "$(IMG)" .
docker-push: ## Push an explicitly selected image.
	$(CONTAINER_TOOL) push "$(IMG)"
PLATFORMS ?= linux/amd64,linux/arm64
docker-buildx: ## Build and push multi-platform images using buildx.
	$(CONTAINER_TOOL) buildx build --platform="$(PLATFORMS)" --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --push -t "$(IMG)" .
helm-lint: ## Strict lint of all chart examples.
	@for values in charts/herzi/ci/*-values.yaml; do "$(HELM)" lint --strict charts/herzi -f "$$values"; done
helm-test: ## Assert rendered chart behavior and validation.
	HELM="$(HELM)" ./hack/test-chart.sh
helm-package: helm-lint ## Package the chart into bin/.
	mkdir -p bin
	"$(HELM)" package charts/herzi --destination bin

deploy: ## Install or upgrade with explicitly selected site values and image.
	@test -n "$(VALUES)" -a -n "$(IMG_REPO)" -a -n "$(IMG_TAG)" || { echo "deploy requires VALUES, IMG_REPO and IMG_TAG" >&2; exit 1; }
	"$(HELM)" upgrade --install herzi charts/herzi -n "$(NAMESPACE)" --create-namespace -f "$(VALUES)" --set-string image.repository="$(IMG_REPO)" --set-string image.tag="$(IMG_TAG)"
undeploy: ## Uninstall the Helm release; leave the namespace and external secrets intact.
	"$(HELM)" uninstall herzi -n "$(NAMESPACE)"
test-alerts: ## Test the shipped native alert rules.
	./hack/test-alerts.sh
manifests-drift: manifests ## Compare generated RBAC with the committed file.
	git diff --exit-code -- charts/herzi/files/rbac-rules.yaml

$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"
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
