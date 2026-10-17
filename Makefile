# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# VERSION is stamped into the binary and logged at startup.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate the CRDs and the controller's ClusterRole, and copy them into the chart.
	"$(CONTROLLER_GEN)" rbac:roleName=controller crd paths="./..." output:crd:artifacts:config=config/crd/bases
	./hack/sync-chart.sh

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The e2e tests build the image, load it into their own Kind cluster, and
# reach that cluster only through E2E_KUBECONFIG, so they never touch the
# cluster your kubectl points at. kubectl kuberc is disabled by default for
# test isolation; enable it with KUBECTL_KUBERC=true.
KIND_CLUSTER ?= mode-operator-test-e2e
E2E_KUBECONFIG ?= $(LOCALBIN)/e2e.kubeconfig
# The chart test refuses to run if a cluster with this name already exists.
CHART_TEST_CLUSTER ?= mode-operator-test-chart

.PHONY: setup-test-e2e
setup-test-e2e: $(LOCALBIN) ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@if $(KIND) get clusters | grep -qx "$(KIND_CLUSTER)"; then \
		echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation."; \
	else \
		echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
		$(KIND) create cluster --name $(KIND_CLUSTER) --kubeconfig "$(E2E_KUBECONFIG)"; \
	fi
	@$(KIND) export kubeconfig --name $(KIND_CLUSTER) --kubeconfig "$(E2E_KUBECONFIG)"

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests on their own Kind cluster.
	KUBECONFIG="$(E2E_KUBECONFIG)" KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER) --kubeconfig "$(E2E_KUBECONFIG)"
	@rm -f "$(E2E_KUBECONFIG)"

.PHONY: chart-check
chart-check: manifests ## Fail if the generated config/ or dist/chart differs from what is committed.
	@test -z "$$(git status --porcelain -- config dist/chart)" || { \
		echo "config/ or dist/chart is out of date; run make manifests and commit the result:"; \
		git status --short -- config dist/chart; git diff -- config dist/chart; exit 1; }

.PHONY: helm-lint
helm-lint: helm ## Lint the Helm chart and render it with its main options.
	"$(HELM)" lint dist/chart
	"$(HELM)" template mode-operator dist/chart >/dev/null
	"$(HELM)" template mode-operator dist/chart \
		--set metrics.enabled=false --set crds.install=false --set modeManager.clusterModes=null >/dev/null
	"$(HELM)" template mode-operator dist/chart --set metrics.serviceMonitor.enabled=true --set networkPolicy.enabled=true \
		--set metrics.certSecret=metrics-tls --set rbac.maintainers.groups[0]=maintainers \
		--set rbac.definitionAdmins.serviceAccounts[0].name=gitops \
		--set rbac.definitionAdmins.serviceAccounts[0].namespace=argocd --set definitions[0].name=standard \
		--set definitions[0].modes[0]=normal --set definitions[0].defaultMode=normal >/dev/null
	@! "$(HELM)" template mode-operator dist/chart --set controller.leaderElection=false >/dev/null 2>&1 || { \
		echo "2 replicas without leader election should fail to render"; exit 1; }

.PHONY: test-chart
test-chart: helm docker-build ## Install the chart on a new Kind cluster and check that it works.
	CHART_TEST_CLUSTER=$(CHART_TEST_CLUSTER) IMG=$(IMG) HELM="$(HELM)" KIND=$(KIND) KUBECTL=$(KUBECTL) ./hack/test-chart.sh

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

.PHONY: shellcheck
shellcheck: shellcheck-install ## Lint the shell scripts with shellcheck
	"$(SHELLCHECK)" hack/*.sh .devcontainer/*.sh

# The standard library govulncheck checks is the running Go's, so run it under
# the Go version that the Dockerfile builds the image with.
BUILD_GO_VERSION ?= $(shell hack/build-go-version.sh)
.PHONY: vulncheck
vulncheck: govulncheck ## Check for known vulnerabilities that the code calls, under the image's Go version
	@[ -n "$(BUILD_GO_VERSION)" ] || { echo "Could not read the Go version from the Dockerfile's golang image"; exit 1; }
	GOTOOLCHAIN=go$(BUILD_GO_VERSION) "$(GOVULNCHECK)" ./...

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build the mode-operator binary.
	go build -ldflags "-X main.version=$(VERSION)" -o bin/mode-operator cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build the container image.
	$(CONTAINER_TOOL) build --build-arg VERSION=$(VERSION) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push the container image.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the image to be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push the container image for several platforms
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name mode-operator-builder
	$(CONTAINER_TOOL) buildx use mode-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --build-arg VERSION=$(VERSION) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm mode-operator-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	"$(KUSTOMIZE)" build config/default | sed "s|image: controller:latest|image: ${IMG}|" > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	"$(KUSTOMIZE)" build config/default | sed "s|image: controller:latest|image: ${IMG}|" | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
# shellcheck isn't a Go tool, so shellcheck-install downloads a pinned release.
# Set SHELLCHECK to use another copy.
SHELLCHECK ?= $(LOCALBIN)/shellcheck
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOVULNCHECK ?= $(LOCALBIN)/govulncheck
HELM ?= $(LOCALBIN)/helm

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.22.0

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION ?= v1.9.0
# The sha256 of each release tarball, as GitHub reports it for the asset.
SHELLCHECK_VERSION ?= v0.11.0
SHELLCHECK_SHA256_x86_64 ?= b7af85e41cc99489dcc21d66c6d5f3685138f06d34651e6d34b42ec6d54fe6f6
SHELLCHECK_SHA256_aarch64 ?= 68a8133197a50beb8803f8d42f9908d1af1c5540d4bb05fdfca8c1fa47decefc
HELM_VERSION ?= v4.3.1
KIND_VERSION ?= v0.33.0
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK) ## Download govulncheck locally if necessary.
$(GOVULNCHECK): $(LOCALBIN)
	$(call go-install-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

.PHONY: shellcheck-install
shellcheck-install: $(LOCALBIN) ## Download shellcheck locally if necessary, checking its sha256.
	@target="$(LOCALBIN)/shellcheck"; versioned="$$target-$(SHELLCHECK_VERSION)"; \
	[ "$(SHELLCHECK)" = "$$target" ] || exit 0; \
	[ -f "$$versioned" ] && [ "$$(readlink -- "$$target" 2>/dev/null)" = "$$versioned" ] && exit 0; \
	set -e; \
	case "$$(uname -sm)" in \
	  "Linux x86_64") arch=x86_64; sum=$(SHELLCHECK_SHA256_x86_64) ;; \
	  "Linux aarch64" | "Linux arm64") arch=aarch64; sum=$(SHELLCHECK_SHA256_aarch64) ;; \
	  *) echo "No pinned shellcheck for $$(uname -sm); install one and set SHELLCHECK"; exit 1 ;; \
	esac; \
	tarball="shellcheck-$(SHELLCHECK_VERSION).linux.$$arch.tar.gz"; \
	tmp="$$(mktemp -d)"; \
	echo "Downloading $$tarball"; \
	curl -fsSL -o "$$tmp/$$tarball" "https://github.com/koalaman/shellcheck/releases/download/$(SHELLCHECK_VERSION)/$$tarball"; \
	echo "$$sum  $$tmp/$$tarball" | sha256sum -c --quiet -; \
	tar -xzf "$$tmp/$$tarball" -C "$$tmp"; \
	mv "$$tmp/shellcheck-$(SHELLCHECK_VERSION)/shellcheck" "$$versioned"; \
	rm -rf "$$tmp"; \
	ln -sf "$$versioned" "$$target"

.PHONY: helm
helm: $(HELM) ## Download helm locally if necessary.
$(HELM): $(LOCALBIN)
	$(call go-install-tool,$(HELM),helm.sh/helm/v4/cmd/helm,$(HELM_VERSION))

.PHONY: kind
kind: $(LOCALBIN) ## Download kind locally, for CI; locally, the kind on PATH is used.
	$(call go-install-tool,$(LOCALBIN)/kind,sigs.k8s.io/kind,$(KIND_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
