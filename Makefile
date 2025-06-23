# VERSION defines the project version for the bundle.
# Update this value when you upgrade the version of your project.
# To re-generate a bundle for another specific version without changing the standard setup, you can:
# - use the VERSION as arg of the bundle target (e.g make bundle VERSION=0.0.2)
# - use environment variables to overwrite this value (e.g export VERSION=0.0.2)
VERSION ?= 0.0.1

TARGETOS ?= linux
TARGETARCH ?= amd64

# REGISTRY_BASE
# defines the container registry and organization for the bundle and operator container images.
REGISTRY_BASE ?= quay.io/stolostron
IMG_NAME ?= $(REGISTRY_BASE)/mtv-integrations

# Image URL to use all building/pushing image targets
IMG ?= $(IMG_NAME):$(VERSION)

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
CONTAINER_TOOL ?= $(shell which podman 2>/dev/null || which docker 2>/dev/null)

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# GO_ENV_PREFIX is used to set the environment variables for the go commands.
GO_ENV_PREFIX ?= CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}

# ID is used to set the image ID for the docker push command.
ID ?= $($(CONTAINER_TOOL) images --format '{{.ID}}' ${IMG_NAME})

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

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -v -json $$(go list ./... | grep -v /e2e) -coverprofile coverage_unit.out | tee report_unit.json

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

##@ Build

.PHONY: build
build:  fmt vet ## Build manager binary.
	$(GO_ENV_PREFIX) go build -a -o bin/manager cmd/main.go


.PHONY: run
run: fmt vet ## Run a controller from your host.
	$(GO_ENV_PREFIX) go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(GO_ENV_PREFIX) go mod vendor
	$(GO_ENV_PREFIX) go mod tidy
	$(CONTAINER_TOOL) build -t ${IMG} .
	rm -rf vendor

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${ID} ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name mtv-integrations-builder
	$(CONTAINER_TOOL) buildx use mtv-integrations-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm mtv-integrations-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: deploy
deploy: kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.5.0
CONTROLLER_TOOLS_VERSION ?= v0.17.1
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= v1.64.3

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef

############################################################
# Webhook test
############################################################
KIND_NAME ?= kind-cluster
KIND_VERSION ?= v1.31.0
KIND_CLUSTER_NAME = kind-$(KIND_NAME)
USER_TEST ?= test-user
GINKGO = $(LOCALBIN)/ginkgo
CONTEXT_NAME = test-context

.PHONY: kind-create-cluster
kind-create-cluster:
	# Ensuring cluster $(KIND_NAME)
	kind create cluster --name $(KIND_NAME) --image kindest/node:$(KIND_VERSION) --retain --wait 5m
	kubectl config use-context $(KIND_CLUSTER_NAME)
	kind get kubeconfig --name $(KIND_NAME) > kubeconfig_e2e

.PHONY: create-user
create-user:
	$(CONTAINER_TOOL) cp $(KIND_NAME)-control-plane:/etc/kubernetes/pki/ca.crt .
	$(CONTAINER_TOOL) cp $(KIND_NAME)-control-plane:/etc/kubernetes/pki/ca.key .
	openssl genrsa -out user1.key 2048
	openssl req -new -key user1.key -out user1.csr -subj "/CN=user1/O=tenant1"
	openssl x509 -req -in user1.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out user1.crt -days 360

add-user:
	yq -i '.clusters[0].cluster.certificate-authority-data = "$(shell base64 -w 0 < ca.crt)"' kubeconfig_e2e
	yq -i '.contexts[0].context.user = "$(USER_TEST)"' kubeconfig_e2e
	yq -i '.contexts[0].name = "$(CONTEXT_NAME)"' kubeconfig_e2e
	yq -i '.current-context = "$(CONTEXT_NAME)"' kubeconfig_e2e
	yq -i '.users += [{"name": "$(USER_TEST)", "user": {"client-certificate-data": "$(shell base64 -w 0 < user1.crt)", "client-key-data": "$(shell base64 -w 0 < user1.key)"}}]' kubeconfig_e2e

cert-manager:
	@echo Installing cert-manager
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.12.0/cert-manager.yaml
	@echo "Waiting until the pods are up"
	kubectl wait deployment -n cert-manager cert-manager --for condition=Available=True --timeout=180s
	kubectl wait deployment -n cert-manager cert-manager-webhook --for condition=Available=True --timeout=180s

install-resources:
	-kubectl create ns open-cluster-management
	kubectl apply -f ./config/webhook_test/
	kubectl apply -f https://raw.githubusercontent.com/kubev2v/forklift/refs/heads/main/operator/config/crd/bases/forklift.konveyor.io_plans.yaml
	kubectl apply -f https://raw.githubusercontent.com/open-cluster-management-io/api/main/cluster/v1/0000_00_clusters.open-cluster-management.io_managedclusters.crd.yaml
	kubectl apply -f https://raw.githubusercontent.com/open-cluster-management-io/multicloud-integrations/refs/heads/main/deploy/crds/clusters.open-cluster-management.io_managedserviceaccounts.crd.yaml

kind-load-image: docker-build
	kind load image-archive <($(CONTAINER_TOOL) save $(IMG)) --name $(KIND_NAME)

prepare-webhook-test: kind-create-cluster create-user add-user cert-manager kind-load-image install-resources deploy

prepare-e2e-test: kind-create-cluster cert-manager install-resources deploy

e2e-dependencies:
	GOBIN=$(LOCALBIN) go install github.com/onsi/ginkgo/v2/ginkgo@$(shell awk '/github.com\/onsi\/ginkgo\/v2/ {print $$2}' go.mod)

SECRET_NAME="mtv-plan-webhook-server-cert"
NAMESPACE="open-cluster-management"
run-instrument:
	kubectl get secret ${SECRET_NAME} -n ${NAMESPACE} -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
	kubectl get secret ${SECRET_NAME} -n ${NAMESPACE} -o jsonpath='{.data.tls\.crt}' | base64 -d > tls.crt
	kubectl get secret ${SECRET_NAME} -n ${NAMESPACE} -o jsonpath='{.data.tls\.key}' | base64 -d > tls.key
	go build -cover -o mtv_integrations_instrumented cmd/main.go
	mkdir -p coverage_profiles
	GOCOVERDIR=coverage_profiles nohup ./mtv_integrations_instrumented --webhook-cert-path=. >> test/e2e/e2e.log  2>&1 &

exit-instrument:
	# Exit the instrumented process
	pgrep mtv_integrations_instrumented | xargs kill -9 || true
	-rm test/e2e/e2e.log 

run-webhook-test: e2e-dependencies
	# Run the webhook test
	kubectl wait deployment -n open-cluster-management mtv-integrations-controller --for condition=Available=True --timeout=180s
	$(GINKGO) -v --fail-fast --label-filter="webhook" --json-report=report_webhook.json test/e2e

run-e2e-test: e2e-dependencies run-instrument
	$(GINKGO) -v --fail-fast --label-filter="!webhook" --json-report=report_e2e.json test/e2e
	-go tool covdata textfmt -i=coverage_profiles -o=coverage_e2e.out
	$(MAKE) exit-instrument

delete-cluster:
	# Delete the kind cluster
	-kind delete cluster --name $(KIND_NAME)
	-rm kubeconfig_e2e ca.crt ca.key user1.crt user1.key user1.csr ca.srl
