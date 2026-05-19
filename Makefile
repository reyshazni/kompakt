IMG ?= ghcr.io/reyshazni/kompakt:latest
ENVTEST_K8S_VERSION = 1.31.0

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: generate
generate: ## Generate code (deepcopy, CRDs)
	go generate ./...
	controller-gen object paths="./api/..."
	controller-gen crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

.PHONY: manifests
manifests: ## Generate RBAC and CRD manifests
	controller-gen rbac:roleName=kompakt-manager crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

.PHONY: fmt
fmt: ## Run go fmt
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: deps
deps: ## Download dependencies
	go mod download
	go mod tidy

##@ Testing

.PHONY: test
test: ## Run unit tests
	go test ./... -coverprofile cover.out

.PHONY: test-e2e
test-e2e: ## Run e2e tests (requires kind cluster)
	go test ./test/e2e/... -v -count=1

##@ Build

.PHONY: build
build: fmt vet ## Build manager binary
	go build -o bin/manager ./cmd/manager

.PHONY: run
run: fmt vet ## Run against the configured cluster
	go run ./cmd/manager

.PHONY: docker-build
docker-build: ## Build docker image
	docker build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image
	docker push ${IMG}

##@ Deployment

.PHONY: install
install: manifests ## Install CRDs into the cluster
	kubectl apply -f config/crd/bases

.PHONY: uninstall
uninstall: ## Uninstall CRDs from the cluster
	kubectl delete -f config/crd/bases

.PHONY: deploy
deploy: manifests ## Deploy controller to the cluster
	cd config/manager && kustomize edit set image controller=${IMG}
	kustomize build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the cluster
	kustomize build config/default | kubectl delete -f -

##@ Kind

.PHONY: kind-create
kind-create: ## Create a kind cluster for development
	kind create cluster --name kompakt-dev --image kindest/node:v1.31.0

.PHONY: kind-delete
kind-delete: ## Delete the kind cluster
	kind delete cluster --name kompakt-dev

.PHONY: kind-load
kind-load: ## Load docker image into kind
	kind load docker-image ${IMG} --name kompakt-dev

##@ Docs

.PHONY: open-docs
open-docs: ## Serve docs locally at http://localhost:4400
	.venv/bin/mkdocs serve -a localhost:4400
